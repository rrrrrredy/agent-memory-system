package capturesupervisor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	stateDirectoryName = "capture-supervisor"
	maximumStateBytes  = 64 * 1024 * 1024
)

var auditNamePattern = regexp.MustCompile(`^([0-9]{20})-([0-9a-f]{64})\.json$`)

type replayState struct {
	LastSequence        uint64
	LastEventSHA256     string
	ConfigSHA256        string
	RunsCompleted       uint64
	ActiveRunID         string
	ActiveRunStarted    *time.Time
	LastRunID           string
	LastRunStartedAt    *time.Time
	LastRunFinishedAt   *time.Time
	LastRunOutcome      string
	Sources             map[string]SourceStatus
	LastInventories     map[string]string
	LastInventoryDetail map[string]Inventory
	ConfigReferences    map[string]struct{}
	InventoryReferences map[string]struct{}
	ActiveSources       map[string]Source
	ActiveInventories   map[string][]Inventory
	ActiveResults       map[string]SourceResult
	ActiveFullReconcile bool
	Warnings            []Issue
}

var errOperationLocked = errors.New("capture supervisor operation lock is held")

type operationLock struct {
	path string
	file *os.File
}

func stateRoot(store *ledger.Store) string {
	return filepath.Join(store.Root(), "state", stateDirectoryName)
}

func auditRoot(store *ledger.Store) string {
	return filepath.Join(stateRoot(store), "audit")
}

func configRoot(store *ledger.Store) string {
	return filepath.Join(stateRoot(store), "configs")
}

func inventoryRoot(store *ledger.Store) string {
	return filepath.Join(stateRoot(store), "inventories")
}

func operationLockPath(store *ledger.Store) string {
	return filepath.Join(stateRoot(store), "operation.lock")
}

func acquireOperationLock(store *ledger.Store, now time.Time) (*operationLock, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	if err := ensureStateSafe(store); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateRoot(store), 0o700); err != nil {
		return nil, fmt.Errorf("create capture supervisor state: %w", err)
	}
	path := operationLockPath(store)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire capture supervisor lock: %w", err)
	}
	if err := validateOpenedLockFile(path, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := tryLockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errOperationLocked) {
			return nil, errors.New("capture supervisor is locked; verify no run is active before explicit recovery")
		}
		return nil, fmt.Errorf("acquire capture supervisor operating-system lock: %w", err)
	}
	lockID, err := ledger.NewEventID(now)
	if err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	metadata := struct {
		SchemaVersion string    `json:"schema_version"`
		LockID        string    `json:"lock_id"`
		PID           int       `json:"pid"`
		CreatedAt     time.Time `json:"created_at"`
	}{SchemaVersion: "capture-supervisor-lock/v1alpha1", LockID: lockID,
		PID: os.Getpid(), CreatedAt: now.UTC()}
	if err := file.Truncate(0); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("truncate capture supervisor lock: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("rewind capture supervisor lock: %w", err)
	}
	writeErr := json.NewEncoder(file).Encode(metadata)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr != nil {
		_ = unlockFile(file)
		_ = file.Close()
		if writeErr != nil {
			return nil, fmt.Errorf("write capture supervisor lock: %w", writeErr)
		}
	}
	return &operationLock{path: path, file: file}, nil
}

func (lock *operationLock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unlockFile(lock.file)
	closeErr := lock.file.Close()
	lock.file = nil
	lock.path = ""
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func ClearStaleLock(store *ledger.Store) (bool, error) {
	if store == nil {
		return false, errors.New("store is required")
	}
	if err := ensureStateSafe(store); err != nil {
		return false, err
	}
	path := operationLockPath(store)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect capture supervisor lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("capture supervisor lock is not a regular local file")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("open capture supervisor lock: %w", err)
	}
	defer file.Close()
	if err := validateOpenedLockFile(path, file); err != nil {
		return false, err
	}
	if err := tryLockFile(file); err != nil {
		if errors.Is(err, errOperationLocked) {
			return false, errors.New("capture supervisor lock is active and cannot be cleared")
		}
		return false, err
	}
	defer unlockFile(file)
	if err := file.Truncate(0); err != nil {
		return false, fmt.Errorf("clear stale capture supervisor lock metadata: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("rewind capture supervisor lock metadata: %w", err)
	}
	metadata := struct {
		SchemaVersion string    `json:"schema_version"`
		ClearedAt     time.Time `json:"cleared_at"`
	}{SchemaVersion: "capture-supervisor-lock-cleared/v1alpha1", ClearedAt: time.Now().UTC()}
	if err := json.NewEncoder(file).Encode(metadata); err != nil {
		return false, fmt.Errorf("write cleared capture supervisor lock metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync cleared capture supervisor lock metadata: %w", err)
	}
	return true, nil
}

func operationLockHeld(store *ledger.Store) (bool, error) {
	if err := ensureStateSafe(store); err != nil {
		return false, err
	}
	path := operationLockPath(store)
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	if err := validateOpenedLockFile(path, file); err != nil {
		return false, err
	}
	if err := tryLockFile(file); err != nil {
		if errors.Is(err, errOperationLocked) {
			return true, nil
		}
		return false, err
	}
	if err := unlockFile(file); err != nil {
		return false, err
	}
	return false, nil
}

func validateOpenedLockFile(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened capture supervisor lock: %w", err)
	}
	named, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect named capture supervisor lock: %w", err)
	}
	if named.Mode()&os.ModeSymlink != 0 || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		return errors.New("capture supervisor lock identity changed during acquisition")
	}
	return nil
}

func replayAudit(store *ledger.Store) (replayState, error) {
	state := replayState{
		Sources: map[string]SourceStatus{}, LastInventories: map[string]string{},
		LastInventoryDetail: map[string]Inventory{}, ActiveInventories: map[string][]Inventory{},
		ActiveResults:    map[string]SourceResult{},
		ConfigReferences: map[string]struct{}{}, InventoryReferences: map[string]struct{}{},
		ActiveSources: map[string]Source{}, Warnings: []Issue{},
	}
	if err := ensureStateSafe(store); err != nil {
		return state, err
	}
	entries, err := os.ReadDir(auditRoot(store))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read capture supervisor audit: %w", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			return state, errors.New("capture supervisor audit contains an unexpected directory")
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			state.Warnings = appendIssueOnce(state.Warnings, Issue{
				Code:    "capture_audit_temp_orphan",
				Message: "capture supervisor audit contains an uncommitted temporary file",
			})
			continue
		}
		match := auditNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return state, errors.New("capture supervisor audit contains an unexpected file")
		}
		data, err := readImmutable(store, filepath.Join(auditRoot(store), entry.Name()))
		if err != nil {
			return state, err
		}
		var event Event
		if err := decodeStrict(data, &event); err != nil {
			return state, errors.New("capture supervisor audit contains an invalid event")
		}
		if fmt.Sprintf("%020d", event.Sequence) != match[1] || event.EventSHA256 != match[2] {
			return state, errors.New("capture supervisor audit filename does not match its event")
		}
		var configured Config
		if event.Action == "configure" {
			configured, err = loadConfigForAudit(store, event.ConfigSHA256)
			if err != nil {
				return state, fmt.Errorf("verify referenced capture config: %w", err)
			}
		}
		if err := validateEvent(event, state); err != nil {
			return state, err
		}
		if event.Action == "configure" {
			state.ActiveSources = sourceMap(configured)
		}
		if event.Action == "inventory_recorded" {
			inventory, err := loadInventory(store, event.InventorySHA256)
			if err != nil {
				return state, fmt.Errorf("verify referenced capture inventory: %w", err)
			}
			if err := validateInventoryEventBinding(inventory, event, state.ActiveSources); err != nil {
				return state, errors.New("capture inventory does not match its audit event")
			}
			state.LastInventoryDetail[event.SourceID] = inventory
			state.ActiveInventories[event.SourceID] = append(state.ActiveInventories[event.SourceID], inventory)
		}
		applyEvent(&state, event)
	}
	if err := verifyReferencedObjects(store, state); err != nil {
		return state, err
	}
	state.Warnings = append(state.Warnings,
		inspectStateOrphans(configRoot(store), state.ConfigReferences, "config")...)
	state.Warnings = append(state.Warnings,
		inspectStateOrphans(inventoryRoot(store), state.InventoryReferences, "inventory")...)
	return state, nil
}

func appendEvent(store *ledger.Store, event Event, prior replayState) (replayState, error) {
	if event.EventID == "" {
		id, err := ledger.NewEventID(event.ObservedAt)
		if err != nil {
			return prior, err
		}
		event.EventID = "capture-supervisor-" + id
	}
	event.SchemaVersion = EventSchemaVersion
	event.Sequence = prior.LastSequence + 1
	event.ObservedAt = event.ObservedAt.UTC()
	event.PreviousEventSHA256 = prior.LastEventSHA256
	event.Privacy = LocalPrivacy
	event.EventSHA256 = ""
	hash, err := eventSHA256(event)
	if err != nil {
		return prior, err
	}
	event.EventSHA256 = hash
	var inventory *Inventory
	if event.Action == "inventory_recorded" {
		loaded, loadErr := loadInventory(store, event.InventorySHA256)
		if loadErr != nil {
			return prior, loadErr
		}
		if err := validateInventoryEventBinding(loaded, event, prior.ActiveSources); err != nil {
			return prior, err
		}
		inventory = &loaded
	}
	if err := validateEvent(event, prior); err != nil {
		return prior, err
	}
	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return prior, fmt.Errorf("encode capture supervisor event: %w", err)
	}
	data = append(data, '\n')
	directory := auditRoot(store)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return prior, fmt.Errorf("create capture supervisor audit: %w", err)
	}
	name := fmt.Sprintf("%020d-%s.json", event.Sequence, event.EventSHA256)
	if err := writeImmutable(store, filepath.Join(directory, name), data); err != nil {
		return prior, err
	}
	if inventory != nil {
		prior.LastInventoryDetail[event.SourceID] = *inventory
		prior.ActiveInventories[event.SourceID] = append(prior.ActiveInventories[event.SourceID], *inventory)
	}
	applyEvent(&prior, event)
	return prior, nil
}

func validateEvent(event Event, prior replayState) error {
	if event.SchemaVersion != EventSchemaVersion || event.Privacy != LocalPrivacy {
		return errors.New("capture supervisor event has an unsupported contract")
	}
	if event.Sequence != prior.LastSequence+1 || event.PreviousEventSHA256 != prior.LastEventSHA256 {
		return errors.New("capture supervisor audit sequence is broken")
	}
	if event.EventID == "" || event.ObservedAt.IsZero() || !validSHA256(event.ConfigSHA256) {
		return errors.New("capture supervisor event is incomplete")
	}
	expected, err := eventSHA256(event)
	if err != nil || expected != event.EventSHA256 {
		return errors.New("capture supervisor event hash verification failed")
	}
	switch event.Action {
	case "configure":
		if event.RunID != "" || event.Outcome != "configured" || prior.ActiveRunID != "" {
			return errors.New("capture supervisor configure event is invalid")
		}
	case "run_started":
		if event.RunID == "" || event.Outcome != "started" || prior.ActiveRunID != "" ||
			event.ConfigSHA256 != prior.ConfigSHA256 {
			return errors.New("capture supervisor run start event is invalid")
		}
	case "inventory_recorded":
		if event.RunID == "" || event.RunID != prior.ActiveRunID || event.SourceID == "" ||
			event.Outcome != "recorded" || !validSHA256(event.InventorySHA256) ||
			event.ConfigSHA256 != prior.ConfigSHA256 || prior.sourceCompleted(event.SourceID) {
			return errors.New("capture supervisor inventory event is invalid")
		}
	case "source_succeeded", "source_failed", "source_skipped":
		if event.RunID == "" || event.RunID != prior.ActiveRunID || event.SourceID == "" ||
			event.Source == nil || event.Source.SourceID != event.SourceID ||
			event.Outcome != event.Source.Outcome || event.ConfigSHA256 != prior.ConfigSHA256 ||
			prior.sourceCompleted(event.SourceID) {
			return errors.New("capture supervisor source event is invalid")
		}
		if err := validateSourceResult(event.Action, *event.Source); err != nil {
			return err
		}
		configured, exists := prior.ActiveSources[event.SourceID]
		if !exists || !sourceResultMatchesConfig(*event.Source, configured) {
			return errors.New("capture supervisor source result does not match the active configuration")
		}
		if event.InventorySHA256 != event.Source.InventorySHA256 ||
			(event.Source.InventorySHA256 != "" &&
				event.Source.InventorySHA256 != prior.LastInventories[event.SourceID]) {
			return errors.New("capture supervisor source event has an invalid inventory binding")
		}
		if event.Action == "source_succeeded" {
			if err := validateSuccessfulInventories(configured, prior.ActiveInventories[event.SourceID]); err != nil {
				return err
			}
		}
	case "run_finished":
		if event.RunID == "" || event.RunID != prior.ActiveRunID ||
			!oneOf(event.Outcome, "success", "partial", "failed", "canceled") ||
			event.ConfigSHA256 != prior.ConfigSHA256 {
			return errors.New("capture supervisor run finish event is invalid")
		}
		if event.FullReconcile != prior.ActiveFullReconcile ||
			runOutcomeFromReplay(prior) != event.Outcome {
			return errors.New("capture supervisor run finish outcome does not match its source results")
		}
	case "run_abandoned":
		if event.RunID == "" || event.RunID != prior.ActiveRunID || event.Outcome != "abandoned" ||
			event.ConfigSHA256 != prior.ConfigSHA256 {
			return errors.New("capture supervisor recovery event is invalid")
		}
	default:
		return errors.New("capture supervisor event action is unsupported")
	}
	return nil
}

func validateSourceResult(action string, source SourceResult) error {
	if !sourceIDPattern.MatchString(source.SourceID) || !validSHA256(source.SourceConfigSHA256) ||
		!oneOf(source.Outcome, "success", "partial", "failed", "skipped") ||
		source.FilesExamined < 0 || source.FilesChanged < 0 || source.EventsAppended < 0 ||
		source.GapsAppended < 0 || source.BytesCaptured < 0 {
		return errors.New("capture supervisor source result is invalid")
	}
	if source.InventorySHA256 != "" && !validSHA256(source.InventorySHA256) {
		return errors.New("capture supervisor source result has an invalid inventory hash")
	}
	if source.ErrorDetailSHA256 != "" && !validSHA256(source.ErrorDetailSHA256) {
		return errors.New("capture supervisor source result has an invalid error hash")
	}
	expectedAgent := ledger.AgentUnknown
	switch source.Kind {
	case SourceCodexRollouts:
		expectedAgent = ledger.AgentCodex
	case SourceClaudeHome:
		expectedAgent = ledger.AgentClaudeCode
	case SourceOpenCodeNative, SourceOpenCodeEvents:
		expectedAgent = ledger.AgentOpenCode
	}
	if source.Agent != expectedAgent {
		return errors.New("capture supervisor source result agent does not match its kind")
	}
	switch action {
	case "source_succeeded":
		if source.Outcome != "success" || source.InventorySHA256 == "" ||
			source.ErrorCode != "" || source.ErrorDetailSHA256 != "" {
			return errors.New("capture supervisor successful source result is inconsistent")
		}
	case "source_failed":
		if !oneOf(source.Outcome, "partial", "failed") || source.ErrorCode == "" {
			return errors.New("capture supervisor failed source result is inconsistent")
		}
	case "source_skipped":
		if source.Outcome != "skipped" || source.ErrorCode == "" {
			return errors.New("capture supervisor skipped source result is inconsistent")
		}
	}
	return nil
}

func applyEvent(state *replayState, event Event) {
	if state.Sources == nil {
		state.Sources = map[string]SourceStatus{}
	}
	if state.LastInventories == nil {
		state.LastInventories = map[string]string{}
	}
	if state.LastInventoryDetail == nil {
		state.LastInventoryDetail = map[string]Inventory{}
	}
	if state.ActiveInventories == nil {
		state.ActiveInventories = map[string][]Inventory{}
	}
	if state.ActiveResults == nil {
		state.ActiveResults = map[string]SourceResult{}
	}
	if state.ConfigReferences == nil {
		state.ConfigReferences = map[string]struct{}{}
	}
	if state.InventoryReferences == nil {
		state.InventoryReferences = map[string]struct{}{}
	}
	state.LastSequence = event.Sequence
	state.LastEventSHA256 = event.EventSHA256
	switch event.Action {
	case "configure":
		state.ConfigSHA256 = event.ConfigSHA256
		state.ConfigReferences[event.ConfigSHA256] = struct{}{}
	case "run_started":
		state.ActiveRunID = event.RunID
		state.ActiveFullReconcile = event.FullReconcile
		state.ActiveInventories = map[string][]Inventory{}
		state.ActiveResults = map[string]SourceResult{}
		started := event.ObservedAt
		state.ActiveRunStarted = &started
		state.LastRunID = event.RunID
		state.LastRunStartedAt = &started
	case "inventory_recorded":
		state.LastInventories[event.SourceID] = event.InventorySHA256
		state.InventoryReferences[event.InventorySHA256] = struct{}{}
	case "source_succeeded", "source_failed", "source_skipped":
		source := event.Source
		state.ActiveResults[event.SourceID] = *source
		status := state.Sources[event.SourceID]
		status.SourceID, status.Agent, status.Kind, status.Required =
			source.SourceID, source.Agent, source.Kind, source.Required
		status.LastRunID = event.RunID
		status.ConfigSHA256 = event.ConfigSHA256
		attempt := event.ObservedAt
		status.LastAttemptAt = &attempt
		status.LastOutcome = source.Outcome
		status.LastErrorCode = source.ErrorCode
		if source.Outcome == "success" {
			success := event.ObservedAt
			status.LastSuccessAt = &success
		}
		state.Sources[event.SourceID] = status
	case "run_finished", "run_abandoned":
		finished := event.ObservedAt
		state.LastRunFinishedAt = &finished
		state.LastRunOutcome = event.Outcome
		state.ActiveRunID = ""
		state.ActiveRunStarted = nil
		state.ActiveFullReconcile = false
		state.ActiveInventories = map[string][]Inventory{}
		state.ActiveResults = map[string]SourceResult{}
		if event.Action == "run_finished" {
			state.RunsCompleted++
		}
	}
}

func validateInventoryEventBinding(inventory Inventory, event Event, configured map[string]Source) error {
	source, exists := configured[event.SourceID]
	sourceConfigSHA256, err := sourceSHA256(source)
	if !exists || inventory.RunID != event.RunID || inventory.SourceID != event.SourceID ||
		inventory.Agent != source.Agent || inventory.Kind != source.Kind || err != nil ||
		inventory.SourceConfigSHA256 != sourceConfigSHA256 {
		return errors.New("capture inventory binding is invalid")
	}
	return nil
}

func (state replayState) sourceCompleted(sourceID string) bool {
	_, completed := state.ActiveResults[sourceID]
	return completed
}

func validateSuccessfulInventories(source Source, inventories []Inventory) error {
	for _, inventory := range inventories {
		if inventory.Status != "available" || inventory.Available == 0 {
			return errors.New("capture supervisor successful source has an incomplete inventory")
		}
	}
	switch source.Kind {
	case SourceOpenCodeNative:
		if len(inventories) != 1 || inventories[0].Phase != "pre_capture" {
			return errors.New("capture supervisor successful native source requires one complete inventory")
		}
	default:
		if len(inventories) != 2 || inventories[0].Phase != "pre_capture" ||
			inventories[1].Phase != "post_capture" || !inventoriesEquivalent(inventories[0], inventories[1]) {
			return errors.New("capture supervisor successful file source requires equivalent pre and post inventories")
		}
	}
	return nil
}

func runOutcomeFromReplay(state replayState) string {
	if len(state.ActiveResults) != len(state.ActiveSources) {
		return "incomplete"
	}
	results := make([]SourceResult, 0, len(state.ActiveSources))
	for sourceID := range state.ActiveSources {
		result, exists := state.ActiveResults[sourceID]
		if !exists {
			return "incomplete"
		}
		results = append(results, result)
	}
	return runOutcome(results)
}

func sourceResultMatchesConfig(result SourceResult, configured Source) bool {
	hash, err := sourceSHA256(configured)
	return err == nil && result.SourceID == configured.ID && result.Agent == configured.Agent &&
		result.Kind == configured.Kind && result.Required == configured.Required &&
		result.SourceConfigSHA256 == hash
}

func sourceMap(config Config) map[string]Source {
	result := make(map[string]Source, len(config.Sources))
	for _, source := range config.Sources {
		result[source.ID] = source
	}
	return result
}

func verifyReferencedObjects(store *ledger.Store, state replayState) error {
	for hash := range state.ConfigReferences {
		data, err := readImmutable(store, filepath.Join(configRoot(store), hash+".json"))
		if err != nil {
			return fmt.Errorf("read referenced capture config: %w", err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != hash {
			return errors.New("referenced capture config hash verification failed")
		}
	}
	return nil
}

func loadConfigForAudit(store *ledger.Store, hash string) (Config, error) {
	if !validSHA256(hash) {
		return Config{}, errors.New("capture config hash is invalid")
	}
	data, err := readImmutable(store, filepath.Join(configRoot(store), hash+".json"))
	if err != nil {
		return Config{}, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != hash {
		return Config{}, errors.New("capture config hash verification failed")
	}
	var config Config
	if err := decodeStrict(data, &config); err != nil {
		return Config{}, errors.New("capture config object is invalid")
	}
	if config.SchemaVersion != ConfigSchemaVersion || config.Privacy != LocalPrivacy ||
		config.IntervalSeconds < 5 || config.IntervalSeconds > 86400 ||
		config.FullReconcileEveryRuns < 1 || config.FullReconcileEveryRuns > 10000 ||
		config.SourceTimeoutSeconds < 10 || config.SourceTimeoutSeconds > 86400 ||
		len(config.Sources) == 0 || len(config.Sources) > 64 {
		return Config{}, errors.New("capture config has an unsupported contract")
	}
	seen := map[string]struct{}{}
	required := 0
	for _, source := range config.Sources {
		if !sourceIDPattern.MatchString(source.ID) {
			return Config{}, errors.New("capture config contains an invalid source id")
		}
		if _, exists := seen[source.ID]; exists {
			return Config{}, errors.New("capture config repeats a source id")
		}
		seen[source.ID] = struct{}{}
		if source.Required {
			required++
		}
		if !storedSourceContractValid(source) {
			return Config{}, errors.New("capture config contains an invalid source")
		}
	}
	if required == 0 {
		return Config{}, errors.New("capture config has no required source")
	}
	return config, nil
}

func storedSourceContractValid(source Source) bool {
	expected := ledger.AgentUnknown
	switch source.Kind {
	case SourceCodexRollouts:
		expected = ledger.AgentCodex
	case SourceClaudeHome:
		expected = ledger.AgentClaudeCode
	case SourceOpenCodeNative, SourceOpenCodeEvents:
		expected = ledger.AgentOpenCode
	default:
		return false
	}
	if source.Agent != expected {
		return false
	}
	if source.Kind == SourceOpenCodeNative {
		return source.Path == "" && filepath.IsAbs(source.BinaryPath) && filepath.IsAbs(source.StagingRoot)
	}
	return filepath.IsAbs(source.Path) && source.BinaryPath == "" && source.StagingRoot == ""
}

func inspectStateOrphans(root string, referenced map[string]struct{}, label string) []Issue {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []Issue{{
			Code:    "capture_state_orphan_scan_failed",
			Message: "capture supervisor " + label + " state could not be checked for orphans",
		}}
	}
	warnings := []Issue{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".tmp") {
			warnings = appendIssueOnce(warnings, Issue{
				Code:    "capture_state_temp_orphan",
				Message: "capture supervisor " + label + " state contains an uncommitted temporary file",
			})
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || !validSHA256(strings.TrimSuffix(name, ".json")) {
			warnings = appendIssueOnce(warnings, Issue{
				Code:    "capture_state_unexpected_orphan",
				Message: "capture supervisor " + label + " state contains an unexpected unreferenced entry",
			})
			continue
		}
		hash := strings.TrimSuffix(name, ".json")
		if _, exists := referenced[hash]; !exists {
			warnings = appendIssueOnce(warnings, Issue{
				Code:    "capture_state_object_orphan",
				Message: "capture supervisor " + label + " state contains an unreferenced immutable object",
			})
		}
	}
	return warnings
}

func appendIssueOnce(issues []Issue, candidate Issue) []Issue {
	for _, issue := range issues {
		if issue.Code == candidate.Code && issue.SourceID == candidate.SourceID && issue.Message == candidate.Message {
			return issues
		}
	}
	return append(issues, candidate)
}

func eventSHA256(event Event) (string, error) {
	event.EventSHA256 = ""
	data, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func writeImmutable(store *ledger.Store, path string, data []byte) error {
	if len(data) > maximumStateBytes {
		return errors.New("capture supervisor state exceeds the maximum record size")
	}
	if err := ensureStateSafe(store); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create capture supervisor state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".capture-supervisor-*.tmp")
	if err != nil {
		return fmt.Errorf("create capture supervisor temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect capture supervisor state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write capture supervisor state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync capture supervisor state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close capture supervisor state: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("capture supervisor state already exists")
		}
		return fmt.Errorf("commit immutable capture supervisor state: %w", err)
	}
	_ = os.Remove(temporaryPath)
	keep = true
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(path))
		if err == nil {
			_ = directory.Sync()
			_ = directory.Close()
		}
	}
	return nil
}

func readLimited(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open capture supervisor state: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read capture supervisor state: %w", err)
	}
	if len(data) > maximumStateBytes {
		return nil, errors.New("capture supervisor state exceeds the maximum record size")
	}
	return data, nil
}

func readImmutable(store *ledger.Store, path string) ([]byte, error) {
	if err := ensureStateSafe(store); err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if !pathWithin(clean, stateRoot(store)) {
		return nil, errors.New("capture supervisor immutable state path is outside its local state root")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return nil, fmt.Errorf("inspect capture supervisor immutable state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("capture supervisor immutable state must be a regular local file")
	}
	return readLimited(clean)
}

func ensureStateSafe(store *ledger.Store) error {
	if store == nil {
		return errors.New("store is required")
	}
	if err := store.ValidateLocation(); err != nil {
		return err
	}
	root, err := canonicalPath(store.Root())
	if err != nil {
		return fmt.Errorf("resolve capture supervisor evidence root: %w", err)
	}
	for _, path := range []string{
		stateRoot(store), auditRoot(store), configRoot(store), inventoryRoot(store), operationLockPath(store),
	} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect capture supervisor state boundary: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("capture supervisor state must not contain symbolic links")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve capture supervisor state boundary: %w", err)
		}
		if !pathWithin(resolved, root) {
			return errors.New("capture supervisor state resolves outside the evidence root")
		}
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
