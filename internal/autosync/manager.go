package autosync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

var ErrAttemptFailed = errors.New("automatic synchronization attempt failed; inspect status and local audit")

type syncRunner func(context.Context, gitsync.SyncOptions) (gitsync.Result, error)
type exporter func(string, string) (ExportSummary, error)
type repositoryVerifier func(context.Context, string) error

type manager struct {
	scheduler Scheduler
	now       func() time.Time
	runSync   syncRunner
	export    exporter
	verify    repositoryVerifier
}

func newManager() *manager {
	return &manager{
		scheduler: newPlatformScheduler(),
		now:       time.Now,
		runSync:   gitsync.Run,
		export:    exportPromotedMemory,
		verify:    verifyPortableRepository,
	}
}

func Enable(ctx context.Context, options EnableOptions) (Status, error) {
	return newManager().enable(ctx, options)
}

func Disable(ctx context.Context, repositoryRoot string) (Status, error) {
	return newManager().disable(ctx, repositoryRoot)
}

func Run(ctx context.Context, repositoryRoot string) (RunResult, error) {
	return newManager().run(ctx, repositoryRoot)
}

func Recover(ctx context.Context, repositoryRoot string) (Status, error) {
	return newManager().recover(ctx, repositoryRoot)
}

func GetStatus(ctx context.Context, repositoryRoot string) (Status, error) {
	return newManager().status(ctx, repositoryRoot)
}

func ClearStaleLock(repositoryRoot string) (bool, error) {
	root, err := canonicalDirectory(repositoryRoot, "portable memory repository")
	if err != nil {
		return false, err
	}
	return clearOperationLock(root)
}

func (service *manager) enable(ctx context.Context, options EnableOptions) (status Status, returnErr error) {
	prepared, err := service.prepareConfig(ctx, options)
	if err != nil {
		return Status{}, err
	}
	root := prepared.RepositoryRoot
	lock, err := acquireOperationLock(root, service.now())
	if err != nil {
		return Status{}, err
	}
	defer releaseOperationLock(lock, &returnErr)
	priorConfig, hadPrior, err := loadConfig(root)
	if err != nil {
		return Status{}, err
	}
	priorState, err := replayEvents(root)
	if err != nil {
		return Status{}, err
	}
	if hadPrior {
		if err := validateConfig(priorConfig, root); err != nil {
			return Status{}, err
		}
		if priorConfig.Scheduler != service.scheduler.Kind() {
			return Status{}, errors.New("automatic synchronization config targets another scheduler")
		}
		priorHash, hashErr := configSHA256(priorConfig)
		if hashErr != nil || priorState.ConfigSHA256 != priorHash {
			return Status{}, errors.New("automatic synchronization config is not bound to the audit history")
		}
	}
	registered, err := service.scheduler.Exists(ctx, prepared.TaskID)
	if err != nil {
		return Status{}, err
	}
	if hadPrior && priorConfig.Enabled && sameConfigSettings(priorConfig, prepared) &&
		registered && priorState.State != StateDisabled {
		return service.finishLockedStatus(ctx, root, lock)
	}
	if registered && (!hadPrior || !priorConfig.Enabled) {
		return Status{}, errors.New("an unbound automatic synchronization schedule already exists; disable it explicitly before enabling")
	}
	if hadPrior && priorConfig.Enabled && priorState.State == StateSuspended {
		return Status{}, errors.New("automatic synchronization is suspended; fix the fault and run recover before changing its schedule")
	}
	registration := registrationFromConfig(prepared)
	if err := service.scheduler.Install(ctx, registration); err != nil {
		return Status{}, err
	}
	if err := writeConfig(root, prepared); err != nil {
		service.rollbackScheduler(context.Background(), priorConfig, hadPrior, prepared.TaskID)
		return Status{}, err
	}
	hash, err := configSHA256(prepared)
	if err != nil {
		service.rollbackEnable(context.Background(), root, priorConfig, hadPrior, prepared.TaskID)
		return Status{}, err
	}
	event := Event{
		ObservedAt: service.now().UTC(), Action: "enable", Outcome: "enabled",
		ResultingState: StateReady, ConfigSHA256: hash,
		LastSuccessAt: priorState.LastSuccessAt, LastSyncOutcome: priorState.LastSyncOutcome,
	}
	if registered || hadPrior {
		event.Outcome = "updated"
	}
	if _, err := appendEvent(root, event, priorState); err != nil {
		service.rollbackEnable(context.Background(), root, priorConfig, hadPrior, prepared.TaskID)
		return Status{}, err
	}
	return service.finishLockedStatus(ctx, root, lock)
}

func (service *manager) disable(ctx context.Context, repositoryRoot string) (status Status, returnErr error) {
	root, err := service.validateRepository(ctx, repositoryRoot)
	if err != nil {
		return Status{}, err
	}
	lock, err := acquireOperationLock(root, service.now())
	if err != nil {
		return Status{}, err
	}
	defer releaseOperationLock(lock, &returnErr)
	config, exists, err := loadConfig(root)
	if err != nil {
		return Status{}, err
	}
	state, err := replayEvents(root)
	if err != nil {
		return Status{}, err
	}
	id := taskID(root)
	if exists {
		if err := validateConfig(config, root); err != nil {
			return Status{}, err
		}
		if config.Scheduler != service.scheduler.Kind() {
			return Status{}, errors.New("automatic synchronization config targets another scheduler")
		}
		id = config.TaskID
		hash, hashErr := configSHA256(config)
		if hashErr != nil || state.ConfigSHA256 != hash {
			return Status{}, errors.New("automatic synchronization config is not bound to the audit history")
		}
	}
	registered, err := service.scheduler.Exists(ctx, id)
	if err != nil {
		return Status{}, err
	}
	if exists && !config.Enabled && !registered && state.State == StateDisabled {
		return service.finishLockedStatus(ctx, root, lock)
	}
	if err := service.scheduler.Remove(ctx, id); err != nil {
		return Status{}, err
	}
	previous := config
	if !exists {
		config = defaultDisabledConfig(root, service.scheduler.Kind(), id, service.now())
	} else {
		config.Enabled = false
		config.UpdatedAt = service.now().UTC()
	}
	if err := writeConfig(root, config); err != nil {
		if registered && exists && previous.Enabled {
			_ = service.scheduler.Install(context.Background(), registrationFromConfig(previous))
		}
		return Status{}, err
	}
	hash, err := configSHA256(config)
	if err != nil {
		return Status{}, err
	}
	event := Event{
		ObservedAt: service.now().UTC(), Action: "disable", Outcome: "disabled",
		ResultingState: StateDisabled, ConfigSHA256: hash,
		LastAttemptAt: state.LastAttemptAt, LastSuccessAt: state.LastSuccessAt,
		LastSyncOutcome: state.LastSyncOutcome,
	}
	if _, err := appendEvent(root, event, state); err != nil {
		if exists {
			_ = writeConfig(root, previous)
			if registered && previous.Enabled {
				_ = service.scheduler.Install(context.Background(), registrationFromConfig(previous))
			}
		}
		return Status{}, err
	}
	return service.finishLockedStatus(ctx, root, lock)
}

func (service *manager) run(ctx context.Context, repositoryRoot string) (result RunResult, returnErr error) {
	result = RunResult{SchemaVersion: ResultSchemaVersion, Outcome: "failed", Privacy: LocalPrivacy}
	root, err := service.validateRepository(ctx, repositoryRoot)
	if err != nil {
		return result, err
	}
	lock, err := acquireOperationLock(root, service.now())
	if err != nil {
		return result, err
	}
	defer releaseOperationLock(lock, &returnErr)
	config, exists, err := loadConfig(root)
	if err != nil {
		return result, err
	}
	if !exists || !config.Enabled {
		result.Outcome = "disabled"
		result.State = StateDisabled
		return result, nil
	}
	if err := validateConfig(config, root); err != nil {
		return result, err
	}
	if config.Scheduler != service.scheduler.Kind() {
		return result, errors.New("automatic synchronization config targets another scheduler")
	}
	state, err := replayEvents(root)
	if err != nil {
		return result, err
	}
	hash, err := configSHA256(config)
	if err != nil || state.ConfigSHA256 != hash {
		return result, errors.New("automatic synchronization config is not bound to the audit history")
	}
	if state.State == StateDisabled {
		return result, errors.New("automatic synchronization config and audit state disagree")
	}
	result.State = state.State
	if state.State == StateSuspended {
		result.Outcome = "suspended"
		result.ErrorCode = state.ErrorCode
		return result, nil
	}
	now := service.now().UTC()
	if state.NextAttemptAt != nil && now.Before(*state.NextAttemptAt) {
		result.Outcome = "retry_deferred"
		result.State = StateRetrying
		result.NextAttemptAt = state.NextAttemptAt
		return result, nil
	}
	exportSummary, exportErr := service.export(config.EvidenceRoot, root)
	result.Export = &exportSummary
	var syncResult gitsync.Result
	var syncErr error
	if exportErr == nil {
		syncResult, syncErr = service.runSync(ctx, gitsync.SyncOptions{
			RepositoryRoot: root, RemoteName: config.Remote, NonInteractive: true,
		})
		result.Sync = &syncResult
	}
	if exportErr == nil && syncErr == nil {
		event := Event{
			ObservedAt: now, Action: "attempt", Outcome: "succeeded", ResultingState: StateReady,
			LastAttemptAt: &now, LastSuccessAt: &now, LastSyncOutcome: syncResult.Outcome,
			Export: result.Export, Sync: result.Sync, ConfigSHA256: hash,
		}
		if _, err := appendEvent(root, event, state); err != nil {
			return result, err
		}
		result.Outcome = "succeeded"
		result.State = StateReady
		return result, nil
	}
	errorCode := classifyFailure(exportErr, syncResult)
	consecutive := state.ConsecutiveErrors + 1
	resultingState := StateRetrying
	var next *time.Time
	if errorCode == "conflict" || errorCode == "remote_rejected" ||
		consecutive >= config.MaximumConsecutiveErrors {
		resultingState = StateSuspended
		if consecutive >= config.MaximumConsecutiveErrors && errorCode != "conflict" && errorCode != "remote_rejected" {
			errorCode = "retry_limit_reached"
		}
	} else {
		nextAt := now.Add(retryDelay(config, consecutive))
		next = &nextAt
	}
	lastOutcome := syncResult.Outcome
	if exportErr != nil {
		lastOutcome = "export_failed"
	}
	event := Event{
		ObservedAt: now, Action: "attempt", Outcome: "failed", ResultingState: resultingState,
		ConsecutiveErrors: consecutive, NextAttemptAt: next, LastAttemptAt: &now,
		LastSuccessAt: state.LastSuccessAt, LastSyncOutcome: lastOutcome,
		ErrorCode: errorCode, ErrorDetail: attemptErrorDetail(exportErr, syncErr),
		Export: result.Export, Sync: result.Sync, ConfigSHA256: hash,
	}
	if _, err := appendEvent(root, event, state); err != nil {
		return result, err
	}
	result.Outcome = "retry_scheduled"
	if resultingState == StateSuspended {
		result.Outcome = "suspended"
	}
	result.State = resultingState
	result.NextAttemptAt = next
	result.ErrorCode = errorCode
	return result, ErrAttemptFailed
}

func (service *manager) recover(ctx context.Context, repositoryRoot string) (status Status, returnErr error) {
	root, err := service.validateRepository(ctx, repositoryRoot)
	if err != nil {
		return Status{}, err
	}
	lock, err := acquireOperationLock(root, service.now())
	if err != nil {
		return Status{}, err
	}
	defer releaseOperationLock(lock, &returnErr)
	config, exists, err := loadConfig(root)
	if err != nil {
		return Status{}, err
	}
	if !exists || !config.Enabled {
		return Status{}, errors.New("automatic synchronization is not enabled")
	}
	if err := validateConfig(config, root); err != nil {
		return Status{}, err
	}
	if config.Scheduler != service.scheduler.Kind() {
		return Status{}, errors.New("automatic synchronization config targets another scheduler")
	}
	state, err := replayEvents(root)
	if err != nil {
		return Status{}, err
	}
	hash, err := configSHA256(config)
	if err != nil || state.ConfigSHA256 != hash {
		return Status{}, errors.New("automatic synchronization config is not bound to the audit history")
	}
	if state.State == StateDisabled {
		return Status{}, errors.New("automatic synchronization config and audit state disagree")
	}
	if state.State == StateReady {
		return service.finishLockedStatus(ctx, root, lock)
	}
	event := Event{
		ObservedAt: service.now().UTC(), Action: "recover", Outcome: "recovered",
		ResultingState: StateReady, LastAttemptAt: state.LastAttemptAt,
		LastSuccessAt: state.LastSuccessAt, LastSyncOutcome: state.LastSyncOutcome,
		ConfigSHA256: hash,
	}
	if _, err := appendEvent(root, event, state); err != nil {
		return Status{}, err
	}
	return service.finishLockedStatus(ctx, root, lock)
}

func (service *manager) status(ctx context.Context, repositoryRoot string) (Status, error) {
	report := Status{
		SchemaVersion: StatusSchemaVersion, State: StateDisabled,
		Scheduler: service.scheduler.Kind(), Issues: []Issue{}, Privacy: LocalPrivacy,
	}
	root, err := canonicalDirectory(repositoryRoot, "portable memory repository")
	if err != nil {
		return report, err
	}
	if verificationErr := service.verify(ctx, root); verificationErr != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "repository_invalid", Message: "portable memory repository failed verification",
			RecoverBy: "run sync verify and resolve every reported issue",
		})
	}
	config, exists, configErr := loadConfig(root)
	if configErr != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "config_invalid", Message: "automatic synchronization config failed verification",
			RecoverBy: "disable and enable automatic synchronization after inspecting local state",
		})
	}
	state, auditErr := replayEvents(root)
	if auditErr != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "audit_invalid", Message: "automatic synchronization audit failed verification",
			RecoverBy: "preserve the audit and repair it explicitly before running synchronization",
		})
	} else {
		report.State = state.State
		report.ConsecutiveErrors = state.ConsecutiveErrors
		report.NextAttemptAt = state.NextAttemptAt
		report.LastAttemptAt = state.LastAttemptAt
		report.LastSuccessAt = state.LastSuccessAt
		report.LastSyncOutcome = state.LastSyncOutcome
		report.LastErrorCode = state.ErrorCode
		report.EventsChecked = state.LastSequence
	}
	id := taskID(root)
	if exists && configErr == nil {
		report.Enabled = config.Enabled
		report.Scheduler = config.Scheduler
		report.IntervalSeconds = config.IntervalSeconds
		id = config.TaskID
		if err := validateConfig(config, root); err != nil {
			report.Issues = append(report.Issues, Issue{
				Code: "config_invalid", Message: "automatic synchronization config failed verification",
				RecoverBy: "disable and enable automatic synchronization with valid settings",
			})
		} else if config.Scheduler != service.scheduler.Kind() {
			report.Issues = append(report.Issues, Issue{
				Code: "scheduler_mismatch", Message: "automatic synchronization config targets another scheduler",
				RecoverBy: "disable and enable automatic synchronization on this device",
			})
		} else if auditErr == nil {
			hash, hashErr := configSHA256(config)
			if hashErr != nil || state.ConfigSHA256 != hash {
				report.Issues = append(report.Issues, Issue{
					Code: "config_audit_mismatch", Message: "automatic synchronization config is not bound to the latest audit event",
					RecoverBy: "disable and enable automatic synchronization after inspecting local state",
				})
			}
		}
	} else if auditErr == nil && state.LastSequence != 0 {
		report.Issues = append(report.Issues, Issue{
			Code: "config_missing", Message: "automatic synchronization audit exists without its local config",
			RecoverBy: "disable and enable automatic synchronization to rebuild local config",
		})
	}
	registered, schedulerErr := service.scheduler.Exists(ctx, id)
	if schedulerErr != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "scheduler_unavailable", Message: "automatic synchronization scheduler could not be inspected",
			RecoverBy: "restore the user scheduler service, then run status again",
		})
	} else {
		report.SchedulerRegistered = registered
		if report.Enabled && !registered {
			report.Issues = append(report.Issues, Issue{
				Code: "scheduler_missing", Message: "automatic synchronization is enabled but its schedule is absent",
				RecoverBy: "run sync auto enable again with the intended settings",
			})
		}
		if !report.Enabled && registered {
			report.Issues = append(report.Issues, Issue{
				Code: "orphaned_schedule", Message: "an automatic synchronization schedule exists while local config is disabled",
				RecoverBy: "run sync auto disable to remove the orphaned schedule",
			})
		}
	}
	if report.Enabled && report.State == StateDisabled {
		report.Issues = append(report.Issues, Issue{
			Code: "state_mismatch", Message: "automatic synchronization config and audit state disagree",
			RecoverBy: "disable and enable automatic synchronization after inspecting local state",
		})
	}
	if !report.Enabled && report.State != StateDisabled && auditErr == nil {
		report.Issues = append(report.Issues, Issue{
			Code: "state_mismatch", Message: "disabled automatic synchronization retains an active audit state",
			RecoverBy: "run sync auto disable again",
		})
	}
	if _, lockErr := os.Stat(filepath.Join(stateDirectory(root), lockFileName)); lockErr == nil {
		report.Issues = append(report.Issues, Issue{
			Code: "operation_locked", Message: "an automatic synchronization operation is active or left a stale lock",
			RecoverBy: "wait for the active run, or verify no run is active before clearing the stale lock",
		})
	} else if !errors.Is(lockErr, os.ErrNotExist) {
		report.Issues = append(report.Issues, Issue{
			Code: "lock_unavailable", Message: "automatic synchronization lock state could not be inspected",
			RecoverBy: "restore access to the local .agentmem state directory",
		})
	}
	sort.Slice(report.Issues, func(left, right int) bool {
		return report.Issues[left].Code < report.Issues[right].Code
	})
	return report, nil
}

func (service *manager) finishLockedStatus(
	ctx context.Context, repositoryRoot string, lock *operationLock,
) (Status, error) {
	if err := lock.Release(); err != nil {
		return Status{}, fmt.Errorf("automatic synchronization completed but lock cleanup failed: %w", err)
	}
	return service.status(ctx, repositoryRoot)
}

func releaseOperationLock(lock *operationLock, returnErr *error) {
	if lock == nil {
		return
	}
	if err := lock.Release(); err != nil && *returnErr == nil {
		*returnErr = fmt.Errorf("automatic synchronization completed but lock cleanup failed: %w", err)
	}
}

func (service *manager) prepareConfig(ctx context.Context, options EnableOptions) (Config, error) {
	root, err := service.validateRepository(ctx, options.RepositoryRoot)
	if err != nil {
		return Config{}, err
	}
	evidence, err := canonicalDirectory(options.EvidenceRoot, "local evidence root")
	if err != nil {
		return Config{}, err
	}
	if _, err := ledger.Open(evidence); err != nil {
		return Config{}, errors.New("local evidence root is not a valid evidence ledger")
	}
	if pathsOverlap(root, evidence) {
		return Config{}, errors.New("portable memory repository and local evidence root must be physically separate")
	}
	executable, err := canonicalFile(options.ExecutablePath)
	if err != nil {
		return Config{}, err
	}
	remote := options.Remote
	if remote == "" {
		remote = gitsync.DefaultRemote
	}
	if !validSimpleName(remote) {
		return Config{}, errors.New("Git remote name is invalid")
	}
	interval := options.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	maximumRetry := options.MaximumRetry
	if maximumRetry == 0 {
		maximumRetry = DefaultMaximumRetry
	}
	maximumErrors := options.MaximumConsecutiveErrors
	if maximumErrors == 0 {
		maximumErrors = DefaultMaximumConsecutiveErrors
	}
	config := Config{
		SchemaVersion: ConfigSchemaVersion, Enabled: true,
		EvidenceRoot: evidence, RepositoryRoot: root, ExecutablePath: executable,
		Remote: remote, IntervalSeconds: int64(interval / time.Second),
		MaximumRetrySeconds:      int64(maximumRetry / time.Second),
		MaximumConsecutiveErrors: maximumErrors, Scheduler: service.scheduler.Kind(),
		TaskID: taskID(root), UpdatedAt: service.now().UTC(), Privacy: LocalPrivacy,
	}
	if err := validateConfig(config, root); err != nil {
		return Config{}, err
	}
	if service.scheduler.Kind() == "unsupported" {
		return Config{}, errors.New("automatic synchronization scheduling is supported on Windows and macOS")
	}
	return config, nil
}

func (service *manager) validateRepository(ctx context.Context, repositoryRoot string) (string, error) {
	root, err := canonicalDirectory(repositoryRoot, "portable memory repository")
	if err != nil {
		return "", err
	}
	if err := service.verify(ctx, root); err != nil {
		return "", err
	}
	return root, nil
}

func verifyPortableRepository(ctx context.Context, repositoryRoot string) error {
	report := gitsync.Verify(ctx, repositoryRoot)
	if len(report.Issues) != 0 {
		return errors.New("portable memory repository failed verification")
	}
	return nil
}

func validateConfig(config Config, repositoryRoot string) error {
	if config.SchemaVersion != ConfigSchemaVersion || config.Privacy != LocalPrivacy {
		return errors.New("automatic synchronization config has an unsupported contract")
	}
	if !samePathString(config.RepositoryRoot, repositoryRoot) || config.TaskID != taskID(repositoryRoot) {
		return errors.New("automatic synchronization config is bound to another repository")
	}
	interval := time.Duration(config.IntervalSeconds) * time.Second
	maximumRetry := time.Duration(config.MaximumRetrySeconds) * time.Second
	if interval < minimumInterval || interval > maximumInterval || interval%time.Minute != 0 {
		return errors.New("automatic synchronization interval is invalid")
	}
	if maximumRetry < interval || maximumRetry > 7*24*time.Hour || maximumRetry%time.Minute != 0 {
		return errors.New("automatic synchronization retry limit is invalid")
	}
	if config.MaximumConsecutiveErrors < 1 || config.MaximumConsecutiveErrors > 20 ||
		!validSimpleName(config.Remote) || !validScheduler(config.Scheduler) || config.UpdatedAt.IsZero() {
		return errors.New("automatic synchronization config contains invalid settings")
	}
	if config.Enabled {
		if !filepath.IsAbs(config.EvidenceRoot) || !filepath.IsAbs(config.ExecutablePath) {
			return errors.New("automatic synchronization config paths must be absolute")
		}
		evidence, err := canonicalDirectory(config.EvidenceRoot, "automatic synchronization evidence root")
		if err != nil || !samePathString(evidence, config.EvidenceRoot) || pathsOverlap(repositoryRoot, evidence) {
			return errors.New("automatic synchronization evidence root is unavailable")
		}
		if _, err := ledger.Open(evidence); err != nil {
			return errors.New("automatic synchronization evidence root is unavailable")
		}
		executable, err := canonicalFile(config.ExecutablePath)
		if err != nil {
			return err
		}
		if !samePathString(executable, config.ExecutablePath) {
			return errors.New("agentmem executable path is not canonical")
		}
	}
	return nil
}

func exportPromotedMemory(evidenceRoot, repositoryRoot string) (ExportSummary, error) {
	store, err := ledger.Open(evidenceRoot)
	if err != nil {
		return ExportSummary{}, err
	}
	result, err := portable.Export(store, repositoryRoot, portable.ExportOptions{})
	summary := ExportSummary{
		MemoriesSelected: result.MemoriesSelected, RevisionsProjected: result.RevisionsProjected,
		RevisionsWritten: result.RevisionsWritten,
	}
	return summary, err
}

func retryDelay(config Config, consecutive int) time.Duration {
	delay := time.Duration(config.IntervalSeconds) * time.Second
	maximum := time.Duration(config.MaximumRetrySeconds) * time.Second
	for count := 1; count < consecutive && delay < maximum; count++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func classifyFailure(exportErr error, result gitsync.Result) string {
	if exportErr != nil {
		return "export_failed"
	}
	switch result.Outcome {
	case "conflict":
		return "conflict"
	case "remote_rejected":
		return "remote_rejected"
	case "retry_required":
		return "remote_changed"
	default:
		return "sync_failed"
	}
}

func attemptErrorDetail(exportErr, syncErr error) string {
	if exportErr != nil {
		return exportErr.Error()
	}
	if syncErr != nil {
		return syncErr.Error()
	}
	return "automatic synchronization failed without an error detail"
}

func registrationFromConfig(config Config) Registration {
	return Registration{
		TaskID: config.TaskID, RepositoryRoot: config.RepositoryRoot,
		ExecutablePath: config.ExecutablePath,
		Interval:       time.Duration(config.IntervalSeconds) * time.Second,
	}
}

func sameConfigSettings(left, right Config) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return left == right
}

func defaultDisabledConfig(root, scheduler, id string, now time.Time) Config {
	return Config{
		SchemaVersion: ConfigSchemaVersion, RepositoryRoot: root,
		Remote: gitsync.DefaultRemote, IntervalSeconds: int64(DefaultInterval / time.Second),
		MaximumRetrySeconds:      int64(DefaultMaximumRetry / time.Second),
		MaximumConsecutiveErrors: DefaultMaximumConsecutiveErrors,
		Scheduler:                scheduler, TaskID: id, UpdatedAt: now.UTC(), Privacy: LocalPrivacy,
	}
}

func (service *manager) rollbackScheduler(ctx context.Context, prior Config, hadPrior bool, currentID string) {
	if hadPrior && prior.Enabled {
		_ = service.scheduler.Install(ctx, registrationFromConfig(prior))
		return
	}
	_ = service.scheduler.Remove(ctx, currentID)
}

func (service *manager) rollbackEnable(ctx context.Context, root string, prior Config, hadPrior bool, currentID string) {
	service.rollbackScheduler(ctx, prior, hadPrior, currentID)
	if hadPrior {
		_ = writeConfig(root, prior)
	} else {
		_ = os.Remove(configPath(root))
	}
}

func canonicalDirectory(value, label string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is unavailable", label)
	}
	return filepath.Clean(resolved), nil
}

func canonicalFile(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("agentmem executable path is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve agentmem executable: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errors.New("agentmem executable is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("agentmem executable is unavailable")
	}
	return filepath.Clean(resolved), nil
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return contains(left, right) || contains(right, left)
}

func validSimpleName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || len(value) > 255 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func validScheduler(value string) bool {
	return value == "windows_task_scheduler" || value == "launchd"
}

func samePathString(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
