package capturesupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/hookcapture"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func Run(ctx context.Context, store *ledger.Store, options RunOptions) (result RunResult, returnErr error) {
	result = RunResult{SchemaVersion: RunSchemaVersion, Sources: []SourceResult{}, Privacy: LocalPrivacy}
	if store == nil {
		return result, errors.New("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	startedAt := options.Now().UTC()
	result.StartedAt = startedAt
	lock, err := acquireOperationLock(store, startedAt)
	if err != nil {
		return result, err
	}
	defer func() {
		if err := lock.Release(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("capture run completed but lock cleanup failed: %w", err)
		}
	}()
	state, err := replayAudit(store)
	if err != nil {
		return result, err
	}
	if state.ActiveRunID != "" {
		return result, errors.New("an incomplete capture run requires explicit recovery")
	}
	config, err := loadActiveConfig(store, state)
	if err != nil {
		return result, err
	}
	result.FullReconcile = state.RunsCompleted == 0 ||
		(state.RunsCompleted+1)%uint64(config.FullReconcileEveryRuns) == 0
	runID, err := ledger.NewEventID(startedAt)
	if err != nil {
		return result, err
	}
	result.RunID = "capture-run-" + runID
	state, err = appendEvent(store, Event{
		RunID: result.RunID, ObservedAt: startedAt, Action: "run_started", Outcome: "started",
		ConfigSHA256: state.ConfigSHA256, FullReconcile: result.FullReconcile,
	}, state)
	if err != nil {
		return result, err
	}

	canceled := false
	for _, source := range config.Sources {
		if ctx.Err() != nil {
			canceled = true
		}
		base, err := newSourceResult(source)
		if err != nil {
			return result, err
		}
		if canceled {
			base.Outcome, base.ErrorCode = "skipped", "capture_canceled"
			state, err = appendSourceEvent(store, state, result.RunID, base, options.Now())
			if err != nil {
				return result, err
			}
			result.Sources = append(result.Sources, base)
			continue
		}
		sourceContext, cancel := context.WithTimeout(ctx,
			time.Duration(config.SourceTimeoutSeconds)*time.Second)
		prior, priorErr := priorInventory(store, state, source)
		if priorErr != nil {
			cancel()
			return result, priorErr
		}

		var prepared opencode.PreparedCapture
		preparedReady := false
		var inventory Inventory
		requireFull := false
		var sourceErr error
		if source.Kind == SourceOpenCodeNative {
			var listed opencode.CaptureResult
			prepared, listed, sourceErr = opencode.PrepareCapture(sourceContext, store,
				opencode.CaptureOptions{Binary: source.BinaryPath, StagingRoot: source.StagingRoot,
					Now: func() time.Time { return options.Now().UTC() }})
			inventory = buildSessionInventory(source, result.RunID, options.Now(),
				listed.SessionIdentitySHA256, prior, sourceErr == nil)
			addOpenCodeResult(&base, listed)
			preparedReady = sourceErr == nil
		} else {
			inventory, requireFull, sourceErr = buildFileInventory(sourceContext, store, source,
				result.RunID, options.Now().UTC(), prior)
		}
		inventory, state, err = recordInventory(store, state, source, result.RunID, inventory,
			options.Now())
		if err != nil {
			cancel()
			return result, err
		}
		preInventory := inventory
		expectedSources := inventoryExpectations(preInventory)
		expectedTracker := adapterjsonl.NewExpectedSourceTracker()
		base.InventorySHA256 = inventory.InventorySHA256
		gaps, gapErr := appendInventoryGaps(sourceContext, store,
			missingGapEvents(store, source, inventory, options.Now()))
		if gapErr != nil {
			sourceErr = errors.Join(sourceErr, gapErr)
		} else {
			base.GapsAppended += gaps[source.ID]
		}

		full := result.FullReconcile || requireFull
		if sourceContext.Err() == nil && (source.Kind != SourceOpenCodeNative || preparedReady) {
			executed, executeErr := executeSource(sourceContext, store, source, full, expectedSources,
				expectedTracker, &prepared,
				func() time.Time { return options.Now().UTC() })
			base = mergeSourceResult(base, executed)
			sourceErr = errors.Join(sourceErr, executeErr)
		}
		sourceErr = errors.Join(sourceErr, expectedSourceCoverageError(expectedTracker, expectedSources))

		stableInventory := true
		if source.Kind != SourceOpenCodeNative {
			postInventory, _, postErr := buildFileInventory(sourceContext, store, source,
				result.RunID, options.Now().UTC(), &preInventory)
			postInventory.Phase = "post_capture"
			postInventory, state, err = recordInventory(store, state, source, result.RunID,
				postInventory, options.Now())
			if err != nil {
				cancel()
				return result, err
			}
			base.InventorySHA256 = postInventory.InventorySHA256
			postGaps, postGapErr := appendInventoryGaps(sourceContext, store,
				missingGapEvents(store, source, postInventory, options.Now()))
			if postGapErr != nil {
				sourceErr = errors.Join(sourceErr, postGapErr)
			} else {
				base.GapsAppended += postGaps[source.ID]
			}
			stableInventory = inventoriesEquivalent(preInventory, postInventory)
			if !stableInventory {
				sourceErr = errors.Join(sourceErr, errors.New("source changed during capture"))
			}
			sourceErr = errors.Join(sourceErr, postErr)
			inventory = postInventory
		}

		contextErr := sourceContext.Err()
		cancel()
		if contextErr != nil {
			sourceErr = errors.Join(sourceErr, contextErr)
		}
		completeInventory := stableInventory && preInventory.Status == "available" &&
			preInventory.Available > 0 && inventory.Status == "available"
		setSourceOutcome(&base, sourceErr, completeInventory)
		state, err = appendSourceEvent(store, state, result.RunID, base, options.Now())
		if err != nil {
			return result, err
		}
		result.Sources = append(result.Sources, base)
		if ctx.Err() != nil {
			canceled = true
		}
	}

	result.Outcome = runOutcome(result.Sources)
	result.FinishedAt = options.Now().UTC()
	state, err = appendEvent(store, Event{
		RunID: result.RunID, ObservedAt: result.FinishedAt, Action: "run_finished",
		Outcome: result.Outcome, ConfigSHA256: state.ConfigSHA256,
		FullReconcile: result.FullReconcile,
	}, state)
	if err != nil {
		return result, err
	}
	result.AuditSequence = state.LastSequence
	if result.Outcome != "success" {
		return result, errors.New("capture run did not completely preserve every configured source")
	}
	return result, nil
}

func recordInventory(
	store *ledger.Store, state replayState, source Source, runID string, inventory Inventory,
	observedAt time.Time,
) (Inventory, replayState, error) {
	written, err := writeInventory(store, inventory)
	if err != nil {
		return inventory, state, err
	}
	state, err = appendEvent(store, Event{
		RunID: runID, ObservedAt: observedAt, Action: "inventory_recorded",
		Outcome: "recorded", SourceID: source.ID, InventorySHA256: written.InventorySHA256,
		ConfigSHA256: state.ConfigSHA256,
	}, state)
	return written, state, err
}

func executeSource(
	ctx context.Context, store *ledger.Store, source Source, full bool,
	expectedSources map[string]adapterjsonl.ExpectedSource,
	expectedTracker *adapterjsonl.ExpectedSourceTracker, prepared *opencode.PreparedCapture,
	now func() time.Time,
) (SourceResult, error) {
	result, err := newSourceResult(source)
	if err != nil {
		return result, err
	}
	issues := []string{}
	switch source.Kind {
	case SourceCodexRollouts, SourceClaudeHome:
		reconciled, captureErr := hookcapture.Reconcile(store, source.Agent,
			hookcapture.ReconcileOptions{SourcePath: source.Path, FullReconcile: full, Now: now,
				Context: ctx, ExpectedSources: expectedSources, ExpectedTracker: expectedTracker})
		if reconciled.HookSpool != nil {
			addJSONLResult(&result, *reconciled.HookSpool)
		}
		if reconciled.Codex != nil {
			addJSONLResult(&result, *reconciled.Codex)
		}
		if reconciled.ClaudeCode != nil {
			addJSONLResult(&result, reconciled.ClaudeCode.Transcripts)
			addJSONLResult(&result, reconciled.ClaudeCode.PromptHistory)
			result.FilesExamined += reconciled.ClaudeCode.Companion.FilesExamined
			result.EventsAppended += reconciled.ClaudeCode.Companion.SnapshotsAppended
			result.GapsAppended += reconciled.ClaudeCode.Companion.GapsAppended
			result.BytesCaptured += reconciled.ClaudeCode.Companion.BytesRead
		}
		result.GapsAppended += reconciled.GapsAppended
		issues = append(issues, reconciled.Issues...)
		err = captureErr
	case SourceOpenCodeEvents:
		captured, captureErr := opencode.ImportEventPath(store, source.Path,
			opencode.EventOptions{FullReconcile: full, Now: now, Context: ctx,
				ExpectedSources: expectedSources, ExpectedTracker: expectedTracker})
		addJSONLResult(&result, captured)
		err = captureErr
	case SourceOpenCodeNative:
		if prepared == nil {
			return result, errors.New("OpenCode native capture was not prepared")
		}
		captured, captureErr := opencode.CapturePrepared(ctx, store, *prepared)
		addOpenCodeResult(&result, captured)
		err = captureErr
	default:
		err = errors.New("unsupported capture source kind")
	}
	if err == nil && (len(issues) > 0 || result.GapsAppended > 0) {
		result.Outcome = "partial"
		result.ErrorCode = "capture_gaps_observed"
		result.ErrorDetailSHA256 = hashString(strings.Join(issues, "\x00"))
	}
	return result, err
}

func inventoryExpectations(inventory Inventory) map[string]adapterjsonl.ExpectedSource {
	result := map[string]adapterjsonl.ExpectedSource{}
	for _, item := range inventory.Items {
		if item.Type != "file" || item.Status != "available" || !validSHA256(item.ContentSHA256) {
			continue
		}
		result[item.IdentitySHA256] = adapterjsonl.ExpectedSource{
			ContentSHA256: item.ContentSHA256, Bytes: item.Bytes,
		}
	}
	return result
}

func expectedSourceCoverageError(
	tracker *adapterjsonl.ExpectedSourceTracker, expected map[string]adapterjsonl.ExpectedSource,
) error {
	if missing := tracker.MissingCount(expected); missing > 0 {
		return fmt.Errorf("%d pre-inventory source files were not preserved", missing)
	}
	return nil
}

func addOpenCodeResult(target *SourceResult, captured opencode.CaptureResult) {
	target.FilesExamined += captured.Import.FilesExamined + captured.Metadata.FilesExamined
	target.FilesChanged += captured.Import.FilesChanged
	target.EventsAppended += captured.Import.EventsAppended + captured.Metadata.SnapshotsAppended
	target.GapsAppended += captured.GapsAppended + captured.Import.GapsAppended + captured.Metadata.GapsAppended
	target.BytesCaptured += captured.Import.BytesCaptured + captured.Metadata.BytesRead
}

func addJSONLResult(target *SourceResult, source adapterjsonl.Result) {
	target.FilesExamined += source.FilesExamined
	target.FilesChanged += source.FilesChanged
	target.EventsAppended += source.EventsAppended
	target.GapsAppended += source.GapsAppended
	target.BytesCaptured += source.BytesCaptured
}

func newSourceResult(source Source) (SourceResult, error) {
	hash, err := sourceSHA256(source)
	return SourceResult{
		SourceID: source.ID, Agent: source.Agent, Kind: source.Kind,
		Required: source.Required, SourceConfigSHA256: hash,
	}, err
}

func mergeSourceResult(base, executed SourceResult) SourceResult {
	base.FilesExamined += executed.FilesExamined
	base.FilesChanged += executed.FilesChanged
	base.EventsAppended += executed.EventsAppended
	base.GapsAppended += executed.GapsAppended
	base.BytesCaptured += executed.BytesCaptured
	base.Outcome = executed.Outcome
	base.ErrorCode = executed.ErrorCode
	base.ErrorDetailSHA256 = executed.ErrorDetailSHA256
	return base
}

func setSourceOutcome(result *SourceResult, err error, completeInventory bool) {
	if err != nil {
		result.Outcome = "failed"
		result.ErrorCode = classifyRunError(err)
		result.ErrorDetailSHA256 = hashString(err.Error())
		return
	}
	if !completeInventory {
		result.Outcome = "partial"
		result.ErrorCode = "source_inventory_incomplete"
		return
	}
	if result.Outcome == "partial" || result.GapsAppended > 0 {
		result.Outcome = "partial"
		if result.ErrorCode == "" {
			result.ErrorCode = "capture_gaps_observed"
		}
		return
	}
	result.Outcome = "success"
	result.ErrorCode = ""
	result.ErrorDetailSHA256 = ""
}

func appendSourceEvent(
	store *ledger.Store, state replayState, runID string, source SourceResult, now time.Time,
) (replayState, error) {
	action := "source_failed"
	if source.Outcome == "success" {
		action = "source_succeeded"
	} else if source.Outcome == "skipped" {
		action = "source_skipped"
	}
	return appendEvent(store, Event{
		RunID: runID, ObservedAt: now, Action: action, Outcome: source.Outcome,
		SourceID: source.SourceID, Source: &source, InventorySHA256: source.InventorySHA256,
		ConfigSHA256: state.ConfigSHA256,
	}, state)
}

func priorInventory(store *ledger.Store, state replayState, source Source) (*Inventory, error) {
	hash := state.LastInventories[source.ID]
	if hash == "" {
		return nil, nil
	}
	inventory, err := loadInventory(store, hash)
	if err != nil {
		return nil, err
	}
	sourceConfigSHA256, err := sourceSHA256(source)
	if err != nil {
		return nil, err
	}
	if inventory.SourceConfigSHA256 != sourceConfigSHA256 {
		return nil, nil
	}
	return &inventory, nil
}

func runOutcome(sources []SourceResult) string {
	for _, source := range sources {
		if source.ErrorCode == "canceled" || source.ErrorCode == "capture_canceled" {
			return "canceled"
		}
	}
	partial := false
	for _, source := range sources {
		if source.Required && (source.Outcome == "failed" || source.Outcome == "skipped") {
			return "failed"
		}
		if source.Outcome != "success" {
			partial = true
		}
	}
	if partial {
		return "partial"
	}
	return "success"
}

func classifyRunError(err error) string {
	switch {
	case err == nil:
		return "unknown"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ledger.ErrWriterLocked):
		return "writer_locked"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "other"
	}
}

func Recover(store *ledger.Store, now time.Time) (status Status, returnErr error) {
	if store == nil {
		return Status{}, errors.New("store is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	lock, err := acquireOperationLock(store, now)
	if err != nil {
		return Status{}, err
	}
	defer func() {
		if err := lock.Release(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("capture recovery completed but lock cleanup failed: %w", err)
		}
	}()
	state, err := replayAudit(store)
	if err != nil {
		return Status{}, err
	}
	config, err := loadActiveConfig(store, state)
	if err != nil {
		return Status{}, err
	}
	if state.ActiveRunID != "" {
		activeRunID := state.ActiveRunID
		for _, source := range config.Sources {
			if _, completed := state.ActiveResults[source.ID]; completed {
				continue
			}
			result, resultErr := newSourceResult(source)
			if resultErr != nil {
				return Status{}, resultErr
			}
			result.Outcome = "skipped"
			result.ErrorCode = "recovery_abandoned"
			if hash := state.LastInventories[source.ID]; hash != "" {
				inventory, inventoryErr := loadInventory(store, hash)
				if inventoryErr != nil {
					return Status{}, inventoryErr
				}
				if inventory.RunID == activeRunID {
					result.InventorySHA256 = hash
				}
			}
			state, err = appendSourceEvent(store, state, activeRunID, result, now)
			if err != nil {
				return Status{}, err
			}
		}
		state, err = appendEvent(store, Event{
			RunID: activeRunID, ObservedAt: now, Action: "run_abandoned",
			Outcome: "abandoned", ConfigSHA256: state.ConfigSHA256,
		}, state)
		if err != nil {
			return Status{}, err
		}
	}
	return statusFromState(store, config, state, StatusOptions{RequireHealthy: true}), nil
}

func sortedSourceResults(results []SourceResult) []SourceResult {
	result := append([]SourceResult(nil), results...)
	sort.Slice(result, func(left, right int) bool { return result[left].SourceID < result[right].SourceID })
	return result
}
