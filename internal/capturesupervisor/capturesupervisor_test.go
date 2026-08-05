package capturesupervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestConfigureIsLocalOnlyAndCaptureReadinessIsExplicit(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configPath := writeConfigFile(t, fixture.base, Config{
		SchemaVersion: ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []Source{{
			ID: "codex", Agent: ledger.AgentCodex, Kind: SourceCodexRollouts,
			Required: true, Path: fixture.codex,
		}}, Privacy: LocalPrivacy,
	})
	configured, err := ConfigureFile(fixture.store, configPath, fixture.clock())
	if err != nil {
		t.Fatal(err)
	}
	if configured.Ready || !hasStatusIssue(configured, "capture_source_never_attempted") ||
		!hasStatusWarning(configured, "coverage_unproven") {
		t.Fatalf("configured but unobserved capture was reported ready: %+v", configured)
	}
	result, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" || len(result.Sources) != 1 ||
		result.Sources[0].Outcome != "success" {
		t.Fatalf("complete local source did not pass capture: %+v", result)
	}
	status, err := GetStatus(fixture.store, StatusOptions{
		RequireConfigured: true, RequiredAgents: []ledger.Agent{ledger.AgentCodex},
		MaximumAge: time.Hour, Now: fixture.clock,
	})
	if err != nil || !status.Ready || !hasStatusWarning(status, "coverage_unproven") {
		t.Fatalf("successful capture status is incorrect: status=%+v err=%v", status, err)
	}

	data, err := os.ReadFile(filepath.Join(stateRoot(fixture.store), "configs", status.ConfigSHA256+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	expectedSourcePath, err := canonicalPath(fixture.codex)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Sources) != 1 || persisted.Sources[0].Path != expectedSourcePath {
		t.Fatal("local config did not retain its exact source location")
	}
	for _, directory := range []string{auditRoot(fixture.store), inventoryRoot(fixture.store)} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			state, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			rawPath := filepath.Clean(fixture.codex)
			escapedPath := strings.ReplaceAll(rawPath, `\`, `\\`)
			canonicalEscaped := strings.ReplaceAll(expectedSourcePath, `\`, `\\`)
			if strings.Contains(string(state), rawPath) || strings.Contains(string(state), escapedPath) ||
				strings.Contains(string(state), expectedSourcePath) || strings.Contains(string(state), canonicalEscaped) {
				t.Fatalf("raw source path leaked into hashed state %s", entry.Name())
			}
		}
	}
}

func TestInventoryForcesSameSizeReplacementAndRetainsMissingSource(t *testing.T) {
	fixture := newFixture(t)
	rollout := writeCodexRollout(t, fixture.codex, "alpha")
	configureCodex(t, fixture)
	first, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || first.Sources[0].Outcome != "success" {
		t.Fatalf("first capture failed: result=%+v err=%v", first, err)
	}
	writeRolloutContent(t, rollout, "omega")
	second, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	if second.FullReconcile || second.Sources[0].FilesChanged == 0 ||
		second.Sources[0].EventsAppended == 0 {
		t.Fatalf("same-size replacement was not re-read: %+v", second)
	}
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	third, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err == nil || third.Outcome == "success" || third.Sources[0].Outcome == "success" {
		t.Fatalf("deleted observed source was accepted: result=%+v err=%v", third, err)
	}
	state := mustReplay(t, fixture.store)
	last, err := priorInventory(fixture.store, state, state.ActiveSources["codex"])
	if err != nil || last == nil || last.Missing != 1 || last.Items[0].Status != "missing" ||
		last.Items[0].LastSeenRunID == "" {
		t.Fatalf("missing inventory history was not retained: inventory=%+v err=%v", last, err)
	}
	verification := fixture.store.Verify()
	if len(verification.Issues) != 0 {
		t.Fatalf("inventory gap damaged the evidence ledger: %+v", verification)
	}
}

func TestRunContinuesAfterRequiredSourceFailure(t *testing.T) {
	fixture := newFixture(t)
	events := filepath.Join(fixture.base, "opencode-events")
	if err := os.MkdirAll(events, 0o700); err != nil {
		t.Fatal(err)
	}
	eventData := `{"captured_at":"2027-02-01T00:00:00Z","event":{"type":"session.created","properties":{"id":"session-one"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(events, "events.jsonl"), []byte(eventData), 0o600); err != nil {
		t.Fatal(err)
	}
	missingCodex := filepath.Join(fixture.base, "missing-codex")
	configPath := writeConfigFile(t, fixture.base, Config{
		SchemaVersion: ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []Source{
			{ID: "codex", Agent: ledger.AgentCodex, Kind: SourceCodexRollouts, Required: true, Path: missingCodex},
			{ID: "opencode-events", Agent: ledger.AgentOpenCode, Kind: SourceOpenCodeEvents, Required: true, Path: events},
		}, Privacy: LocalPrivacy,
	})
	if _, err := ConfigureFile(fixture.store, configPath, fixture.clock()); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err == nil || result.Outcome != "failed" || len(result.Sources) != 2 {
		t.Fatalf("mixed capture outcome is incorrect: result=%+v err=%v", result, err)
	}
	byID := map[string]SourceResult{}
	for _, source := range result.Sources {
		byID[source.SourceID] = source
	}
	if byID["codex"].Outcome == "success" || byID["opencode-events"].Outcome != "success" {
		t.Fatalf("a failing source stopped an independent source: %+v", result)
	}
	status, statusErr := GetStatus(fixture.store, StatusOptions{
		RequireHealthy: true, RequiredAgents: []ledger.Agent{ledger.AgentOpenCode},
		Now: fixture.clock,
	})
	if statusErr != nil || !status.Ready || !status.CaptureReady {
		t.Fatalf("unrelated required-agent failure polluted scoped readiness: status=%+v err=%v", status, statusErr)
	}
}

func TestMissingSourceReappearanceForcesReconcileAcrossMultipleRuns(t *testing.T) {
	fixture := newFixture(t)
	rollout := writeCodexRollout(t, fixture.codex, "alpha")
	configureCodex(t, fixture)
	if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err == nil {
			t.Fatal("missing source was accepted")
		}
	}
	state := mustReplay(t, fixture.store)
	missing, err := priorInventory(fixture.store, state, state.ActiveSources["codex"])
	if err != nil || missing == nil || len(missing.Items) != 1 ||
		!validSHA256(missing.Items[0].ContentSHA256) || missing.Items[0].Bytes == 0 {
		t.Fatalf("missing inventory discarded the last observed content: inventory=%+v err=%v", missing, err)
	}
	writeRolloutContent(t, rollout, "omega")
	reappeared, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || reappeared.Sources[0].FilesChanged == 0 ||
		reappeared.Sources[0].EventsAppended == 0 {
		t.Fatalf("same-size reappearance was not fully reconciled: result=%+v err=%v", reappeared, err)
	}
}

func TestMissingSourceRootDoesNotLeavePhantomTombstoneAfterRecovery(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "alpha")
	configureCodex(t, fixture)
	if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.codex); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err == nil {
		t.Fatal("missing source root was accepted")
	}
	if err := os.MkdirAll(fixture.codex, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, fixture.codex, "omega")
	recovered, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || recovered.Outcome != "success" {
		t.Fatalf("restored source root retained a phantom tombstone: result=%+v err=%v", recovered, err)
	}
}

func TestIncompleteDiscoveryCarriesPriorItemsAsUnverified(t *testing.T) {
	modified := time.Unix(1800000000, 0).UTC()
	prior := InventoryItem{
		IdentitySHA256: strings.Repeat("a", 64), Type: "file", Status: "available",
		ContentSHA256: strings.Repeat("b", 64), Bytes: 17, ModifiedAt: &modified,
		LastSeenRunID: "capture-run-prior",
	}
	manifest := Inventory{Items: []InventoryItem{}}
	mergeUnavailable(&manifest, map[string]InventoryItem{prior.IdentitySHA256: prior}, false)
	if manifest.Unverified != 1 || manifest.Missing != 0 || len(manifest.Items) != 1 ||
		manifest.Items[0].Status != "unverified" ||
		manifest.Items[0].ContentSHA256 != prior.ContentSHA256 || manifest.Items[0].Bytes != prior.Bytes {
		t.Fatalf("incomplete discovery tombstoned or discarded prior evidence: %+v", manifest)
	}
}

func TestFailedNativeListingDoesNotTombstoneKnownSessions(t *testing.T) {
	fixture := newFixture(t)
	source := Source{ID: "opencode", Agent: ledger.AgentOpenCode, Kind: SourceOpenCodeNative}
	prior := Inventory{
		Items: []InventoryItem{{
			IdentitySHA256: strings.Repeat("c", 64), Type: "session", Status: "available",
			LastSeenRunID: "capture-run-prior",
		}},
	}
	inventory := buildSessionInventory(source, "capture-run-current", fixture.clock(), nil, &prior, false)
	if inventory.Missing != 0 || inventory.Unverified != 1 || inventory.Unreadable != 1 {
		t.Fatalf("failed native listing produced false tombstones: %+v", inventory)
	}
}

func TestReconfiguredSourceDoesNotInheritPriorInventory(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err != nil {
		t.Fatal(err)
	}
	secondRoot := filepath.Join(fixture.base, "second-codex-sessions")
	if err := os.MkdirAll(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, secondRoot, "second")
	secondSource := Source{ID: "codex", Agent: ledger.AgentCodex, Kind: SourceCodexRollouts,
		Required: true, Path: secondRoot}
	configPath := writeConfigFile(t, fixture.base, Config{
		SchemaVersion: ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []Source{secondSource}, Privacy: LocalPrivacy,
	})
	if _, err := ConfigureFile(fixture.store, configPath, fixture.clock()); err != nil {
		t.Fatal(err)
	}
	state := mustReplay(t, fixture.store)
	prior, err := priorInventory(fixture.store, state, state.ActiveSources["codex"])
	if err != nil || prior != nil {
		t.Fatalf("reconfigured source inherited a different source lineage: inventory=%+v err=%v", prior, err)
	}
	result, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || result.Outcome != "success" || result.Sources[0].GapsAppended != 0 {
		t.Fatalf("reconfigured source produced inherited gaps: result=%+v err=%v", result, err)
	}
}

func TestReplayRejectsSuccessWithoutCompleteInventoriesAndIncompleteRun(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	state := mustReplay(t, fixture.store)
	runID := "capture-run-forged-success"
	state, err := appendEvent(fixture.store, Event{
		RunID: runID, ObservedAt: fixture.clock(), Action: "run_started", Outcome: "started",
		ConfigSHA256: state.ConfigSHA256,
	}, state)
	if err != nil {
		t.Fatal(err)
	}
	source := state.ActiveSources["codex"]
	empty := newInventory(source, runID, fixture.clock(), "pre_capture")
	finalizeInventory(&empty)
	empty, state, err = recordInventory(fixture.store, state, source, runID, empty, fixture.clock())
	if err != nil {
		t.Fatal(err)
	}
	result, err := newSourceResult(source)
	if err != nil {
		t.Fatal(err)
	}
	result.Outcome = "success"
	result.InventorySHA256 = empty.InventorySHA256
	if _, err := appendSourceEvent(fixture.store, state, runID, result, fixture.clock()); err == nil ||
		!strings.Contains(err.Error(), "incomplete inventory") {
		t.Fatalf("empty inventory was accepted as a successful source: %v", err)
	}
	if _, err := appendEvent(fixture.store, Event{
		RunID: runID, ObservedAt: fixture.clock(), Action: "run_finished", Outcome: "success",
		ConfigSHA256: state.ConfigSHA256,
	}, state); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("run finished without one terminal result per configured source: %v", err)
	}
}

func TestExpectedSourceCoverageRequiresEveryPreInventoryFile(t *testing.T) {
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	expected := map[string]adapterjsonl.ExpectedSource{
		first:  {ContentSHA256: strings.Repeat("c", 64), Bytes: 10},
		second: {ContentSHA256: strings.Repeat("d", 64), Bytes: 20},
	}
	tracker := adapterjsonl.NewExpectedSourceTracker()
	tracker.Observe(first)
	if err := expectedSourceCoverageError(tracker, expected); err == nil {
		t.Fatal("partial pre-inventory consumption was accepted")
	}
	tracker.Observe(second)
	if err := expectedSourceCoverageError(tracker, expected); err != nil {
		t.Fatalf("complete pre-inventory consumption was rejected: %v", err)
	}
}

func TestLateCancellationAfterSuccessfulSourceRemainsReplayable(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	ctx := auditCancelContext{Context: context.Background(), auditRoot: auditRoot(fixture.store)}
	result, err := Run(ctx, fixture.store, RunOptions{Now: fixture.clock})
	if err != nil || result.Outcome != "success" {
		t.Fatalf("late cancellation rewrote committed successful results: result=%+v err=%v", result, err)
	}
	state := mustReplay(t, fixture.store)
	if state.ActiveRunID != "" || state.LastRunOutcome != "success" {
		t.Fatalf("late cancellation left an unreplayable run: %+v", state)
	}
}

func TestRecoverNeverClearsAnExistingOperationLock(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	lock, err := acquireOperationLock(fixture.store, fixture.clock())
	if err != nil {
		t.Fatal(err)
	}
	lockedStatus, err := GetStatus(fixture.store, StatusOptions{})
	if err != nil || !lockedStatus.OperationLocked || lockedStatus.IntegrityReady || lockedStatus.Ready {
		t.Fatalf("active operation lock did not fail integrity readiness: status=%+v err=%v", lockedStatus, err)
	}
	if _, err := Recover(fixture.store, fixture.clock()); err == nil {
		t.Fatal("recovery cleared a possibly live operation lock")
	}
	if _, err := ClearStaleLock(fixture.store); err == nil {
		t.Fatal("explicit stale-lock clearing ignored an active operating-system lock")
	}
	if _, err := os.Stat(operationLockPath(fixture.store)); err != nil {
		t.Fatalf("recovery removed the existing operation lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	cleared, err := ClearStaleLock(fixture.store)
	if err != nil || !cleared {
		t.Fatalf("released stale lock metadata could not be cleared safely: cleared=%v err=%v", cleared, err)
	}
}

func TestRecoverMarksEveryUnfinishedSourceAndKeepsAbandonedRunUnready(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	state := mustReplay(t, fixture.store)
	runID := "capture-run-unfinished"
	state, err := appendEvent(fixture.store, Event{
		RunID: runID, ObservedAt: fixture.clock(), Action: "run_started", Outcome: "started",
		ConfigSHA256: state.ConfigSHA256,
	}, state)
	if err != nil || state.ActiveRunID != runID {
		t.Fatalf("could not create unfinished run: state=%+v err=%v", state, err)
	}
	status, err := Recover(fixture.store, fixture.clock())
	if err != nil {
		t.Fatal(err)
	}
	if status.CaptureReady || status.Ready || status.LastRunOutcome != "abandoned" ||
		!hasStatusIssue(status, "capture_last_run_abandoned") || len(status.Sources) != 1 ||
		status.Sources[0].LastRunID != runID || status.Sources[0].LastErrorCode != "recovery_abandoned" {
		t.Fatalf("abandoned run was allowed to inherit old readiness: %+v", status)
	}
}

func TestAuditTempFileIsReportedWithoutBreakingCommittedReplay(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	if err := os.WriteFile(filepath.Join(auditRoot(fixture.store), ".capture-supervisor-orphan.tmp"),
		[]byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, StatusOptions{})
	if err != nil || !status.IntegrityReady || !hasStatusWarning(status, "capture_audit_temp_orphan") {
		t.Fatalf("uncommitted audit temp was treated as committed corruption: status=%+v err=%v", status, err)
	}
}

func TestAlreadyOpenedEvidenceRootFailsAfterEnteringGit(t *testing.T) {
	fixture := newFixture(t)
	if err := os.Mkdir(filepath.Join(fixture.store.Root(), ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, StatusOptions{})
	if err != nil || status.IntegrityReady || status.Ready ||
		!hasStatusIssue(status, "capture_supervisor_audit_invalid") {
		t.Fatalf("evidence root entering Git did not fail closed: status=%+v err=%v", status, err)
	}
}

func TestStrictHealthUsesConfiguredDefaultFreshness(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	if _, err := Run(context.Background(), fixture.store, RunOptions{Now: fixture.clock}); err != nil {
		t.Fatal(err)
	}
	state := mustReplay(t, fixture.store)
	status, err := GetStatus(fixture.store, StatusOptions{
		RequireHealthy: true,
		Now:            func() time.Time { return state.Sources["codex"].LastSuccessAt.Add(121 * time.Second) },
	})
	if err != nil || status.CaptureReady || status.Ready || !hasStatusIssue(status, "capture_agent_stale") {
		t.Fatalf("strict health without max-age never expired: status=%+v err=%v", status, err)
	}
}

func TestImmutableStateRejectsOversizeBeforeCommit(t *testing.T) {
	fixture := newFixture(t)
	path := filepath.Join(inventoryRoot(fixture.store), strings.Repeat("d", 64)+".json")
	if err := writeImmutable(fixture.store, path, make([]byte, maximumStateBytes+1)); err == nil {
		t.Fatal("oversized immutable state was committed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized state left a committed object: %v", err)
	}
}

func TestConfigAndAuditTamperingFailClosed(t *testing.T) {
	fixture := newFixture(t)
	writeCodexRollout(t, fixture.codex, "first")
	configureCodex(t, fixture)
	state := mustReplay(t, fixture.store)
	configPath := filepath.Join(configRoot(fixture.store), state.ConfigSHA256+".json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, append(configData, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, StatusOptions{RequireConfigured: true})
	if err != nil || status.Ready || !hasStatusIssue(status, "capture_supervisor_audit_invalid") {
		t.Fatalf("config tampering did not fail closed: status=%+v err=%v", status, err)
	}

	other := newFixture(t)
	writeCodexRollout(t, other.codex, "first")
	configureCodex(t, other)
	entries, err := os.ReadDir(auditRoot(other.store))
	if err != nil || len(entries) == 0 {
		t.Fatal("capture audit is absent")
	}
	auditPath := filepath.Join(auditRoot(other.store), entries[0].Name())
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, []byte(strings.Replace(string(auditData), "configured", "tamperedxx", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = GetStatus(other.store, StatusOptions{RequireConfigured: true})
	if err != nil || status.Ready || !hasStatusIssue(status, "capture_supervisor_audit_invalid") {
		t.Fatalf("audit tampering did not fail closed: status=%+v err=%v", status, err)
	}
}

func TestConfigInputInsideGitIsRejected(t *testing.T) {
	fixture := newFixture(t)
	gitRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(gitRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfigFile(t, gitRoot, Config{
		SchemaVersion: ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []Source{{
			ID: "codex", Agent: ledger.AgentCodex, Kind: SourceCodexRollouts,
			Required: true, Path: fixture.codex,
		}}, Privacy: LocalPrivacy,
	})
	if _, err := ConfigureFile(fixture.store, configPath, fixture.clock()); err == nil ||
		!strings.Contains(err.Error(), "outside every Git worktree") {
		t.Fatalf("Git-contained config input was accepted: %v", err)
	}
}

type testFixture struct {
	base  string
	store *ledger.Store
	codex string
	now   time.Time
}

type auditCancelContext struct {
	context.Context
	auditRoot string
}

func (ctx auditCancelContext) Err() error {
	entries, err := os.ReadDir(ctx.auditRoot)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(ctx.auditRoot, entry.Name()))
		if readErr == nil && strings.Contains(string(data), `"source_succeeded"`) {
			return context.Canceled
		}
	}
	return nil
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(base, "codex-sessions")
	if err := os.MkdirAll(codex, 0o700); err != nil {
		t.Fatal(err)
	}
	return &testFixture{base: base, store: store, codex: codex, now: time.Unix(1800000000, 0).UTC()}
}

func (fixture *testFixture) clock() time.Time {
	fixture.now = fixture.now.Add(time.Second)
	return fixture.now
}

func configureCodex(t *testing.T, fixture *testFixture) {
	t.Helper()
	configPath := writeConfigFile(t, fixture.base, Config{
		SchemaVersion: ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []Source{{
			ID: "codex", Agent: ledger.AgentCodex, Kind: SourceCodexRollouts,
			Required: true, Path: fixture.codex,
		}}, Privacy: LocalPrivacy,
	})
	if _, err := ConfigureFile(fixture.store, configPath, fixture.clock()); err != nil {
		t.Fatal(err)
	}
}

func writeConfigFile(t *testing.T, root string, config Config) string {
	t.Helper()
	path := filepath.Join(root, "capture-config.json")
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCodexRollout(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root,
		"rollout-2027-01-01T00-00-00-00000000-0000-0000-0000-000000000111.jsonl")
	writeRolloutContent(t, path, content)
	return path
}

func writeRolloutContent(t *testing.T, path, content string) {
	t.Helper()
	line := `{"timestamp":"2027-01-01T00:00:00Z","type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000111"}}` + "\n" +
		`{"timestamp":"2027-01-01T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + content + `"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReplay(t *testing.T, store *ledger.Store) replayState {
	t.Helper()
	state, err := replayAudit(store)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func hasStatusIssue(status Status, code string) bool {
	for _, issue := range status.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func hasStatusWarning(status Status, code string) bool {
	for _, issue := range status.Warnings {
		if issue.Code == code {
			return true
		}
	}
	return false
}
