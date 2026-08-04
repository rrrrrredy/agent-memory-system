package autosync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type fakeScheduler struct {
	registrations map[string]Registration
	installErr    error
	removeErr     error
	inspectErr    error
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{registrations: map[string]Registration{}}
}

func (*fakeScheduler) Kind() string { return "windows_task_scheduler" }

func (scheduler *fakeScheduler) Install(_ context.Context, registration Registration) error {
	if scheduler.installErr != nil {
		return scheduler.installErr
	}
	scheduler.registrations[registration.TaskID] = registration
	return nil
}

func (scheduler *fakeScheduler) Remove(_ context.Context, id string) error {
	if scheduler.removeErr != nil {
		return scheduler.removeErr
	}
	delete(scheduler.registrations, id)
	return nil
}

func (scheduler *fakeScheduler) Exists(_ context.Context, id string) (bool, error) {
	if scheduler.inspectErr != nil {
		return false, scheduler.inspectErr
	}
	_, exists := scheduler.registrations[id]
	return exists, nil
}

type autoFixture struct {
	service    *manager
	scheduler  *fakeScheduler
	repository string
	evidence   string
	executable string
	now        time.Time
}

func TestAutomaticSyncLifecycleUsesNonInteractiveVerifiedPath(t *testing.T) {
	fixture := newAutoFixture(t)
	status, err := fixture.service.status(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.State != StateDisabled || len(status.Issues) != 0 {
		t.Fatalf("unexpected initial status: %+v", status)
	}

	status = fixture.enable(t, EnableOptions{})
	if !status.Enabled || status.State != StateReady || !status.SchedulerRegistered || status.EventsChecked != 1 {
		t.Fatalf("unexpected enabled status: %+v", status)
	}

	fixture.service.export = func(evidenceRoot, repositoryRoot string) (ExportSummary, error) {
		if evidenceRoot != fixture.evidence || repositoryRoot != fixture.repository {
			t.Fatalf("unexpected export roots: %q %q", evidenceRoot, repositoryRoot)
		}
		return ExportSummary{MemoriesSelected: 2, RevisionsProjected: 3, RevisionsWritten: 1}, nil
	}
	fixture.service.runSync = func(_ context.Context, options gitsync.SyncOptions) (gitsync.Result, error) {
		if options.RepositoryRoot != fixture.repository || options.RemoteName != gitsync.DefaultRemote || !options.NonInteractive {
			t.Fatalf("automatic run bypassed expected sync options: %+v", options)
		}
		return gitsync.Result{SchemaVersion: gitsync.ResultSchemaVersion, Outcome: "up_to_date", Privacy: "private_git"}, nil
	}
	result, err := fixture.service.run(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "succeeded" || result.State != StateReady || result.Export.RevisionsWritten != 1 ||
		result.Sync == nil || result.Sync.Outcome != "up_to_date" {
		t.Fatalf("unexpected run result: %+v", result)
	}
	status, err = fixture.service.status(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.EventsChecked != 2 || status.LastSuccessAt == nil || status.ConsecutiveErrors != 0 {
		t.Fatalf("success was not observable: %+v", status)
	}

	status, err = fixture.service.disable(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.State != StateDisabled || status.SchedulerRegistered || status.EventsChecked != 3 {
		t.Fatalf("unexpected disabled status: %+v", status)
	}
}

func TestAutomaticSyncRetriesSuspendsAndRecovers(t *testing.T) {
	fixture := newAutoFixture(t)
	fixture.enable(t, EnableOptions{
		Interval: time.Minute, MaximumRetry: 4 * time.Minute, MaximumConsecutiveErrors: 3,
	})
	fixture.service.export = func(string, string) (ExportSummary, error) { return ExportSummary{}, nil }
	fixture.service.runSync = func(context.Context, gitsync.SyncOptions) (gitsync.Result, error) {
		return gitsync.Result{SchemaVersion: gitsync.ResultSchemaVersion, Outcome: "failed", Privacy: "private_git"},
			errors.New("synthetic network failure")
	}

	first, err := fixture.service.run(context.Background(), fixture.repository)
	if !errors.Is(err, ErrAttemptFailed) || first.State != StateRetrying || first.NextAttemptAt == nil ||
		!first.NextAttemptAt.Equal(fixture.now.Add(time.Minute)) {
		t.Fatalf("unexpected first retry: result=%+v err=%v", first, err)
	}
	audit, err := os.ReadFile(eventsPath(fixture.repository))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "synthetic network failure") ||
		!strings.Contains(string(audit), `"sync":{"schema_version":"git-sync-result/v1alpha1"`) ||
		!strings.Contains(string(audit), `"export":{"memories_selected":0`) {
		t.Fatalf("failed attempt evidence was incomplete: %s", audit)
	}
	deferred, err := fixture.service.run(context.Background(), fixture.repository)
	if err != nil || deferred.Outcome != "retry_deferred" {
		t.Fatalf("retry was not deferred: result=%+v err=%v", deferred, err)
	}
	status, _ := fixture.service.status(context.Background(), fixture.repository)
	if status.EventsChecked != 2 {
		t.Fatalf("deferred run wrote an audit event: %+v", status)
	}

	fixture.advance(time.Minute)
	second, err := fixture.service.run(context.Background(), fixture.repository)
	if !errors.Is(err, ErrAttemptFailed) || second.NextAttemptAt == nil ||
		!second.NextAttemptAt.Equal(fixture.now.Add(2*time.Minute)) {
		t.Fatalf("unexpected second retry: result=%+v err=%v", second, err)
	}
	fixture.advance(2 * time.Minute)
	third, err := fixture.service.run(context.Background(), fixture.repository)
	if !errors.Is(err, ErrAttemptFailed) || third.State != StateSuspended ||
		third.ErrorCode != "retry_limit_reached" {
		t.Fatalf("retry limit did not suspend: result=%+v err=%v", third, err)
	}
	suspended, err := fixture.service.run(context.Background(), fixture.repository)
	if err != nil || suspended.Outcome != "suspended" {
		t.Fatalf("suspended run was not inert: result=%+v err=%v", suspended, err)
	}

	status, err = fixture.service.recover(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateReady || status.ConsecutiveErrors != 0 || status.EventsChecked != 5 {
		t.Fatalf("unexpected recovered status: %+v", status)
	}
	fixture.service.runSync = func(context.Context, gitsync.SyncOptions) (gitsync.Result, error) {
		return gitsync.Result{SchemaVersion: gitsync.ResultSchemaVersion, Outcome: "up_to_date", Privacy: "private_git"}, nil
	}
	if result, err := fixture.service.run(context.Background(), fixture.repository); err != nil || result.State != StateReady {
		t.Fatalf("recovered synchronization did not succeed: result=%+v err=%v", result, err)
	}
}

func TestAutomaticSyncConflictSuspendsImmediately(t *testing.T) {
	fixture := newAutoFixture(t)
	fixture.enable(t, EnableOptions{})
	fixture.service.export = func(string, string) (ExportSummary, error) { return ExportSummary{}, nil }
	fixture.service.runSync = func(context.Context, gitsync.SyncOptions) (gitsync.Result, error) {
		return gitsync.Result{SchemaVersion: gitsync.ResultSchemaVersion, Outcome: "conflict", Privacy: "private_git"},
			errors.New("synthetic conflict")
	}
	result, err := fixture.service.run(context.Background(), fixture.repository)
	if !errors.Is(err, ErrAttemptFailed) || result.State != StateSuspended || result.ErrorCode != "conflict" {
		t.Fatalf("conflict did not suspend: result=%+v err=%v", result, err)
	}
}

func TestAutomaticSyncRejectsTamperedAuditAndConfig(t *testing.T) {
	t.Run("audit", func(t *testing.T) {
		fixture := newAutoFixture(t)
		fixture.enable(t, EnableOptions{})
		path := eventsPath(fixture.repository)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"outcome":"enabled"`, `"outcome":"changed"`, 1))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		status, err := fixture.service.status(context.Background(), fixture.repository)
		if err != nil || !hasIssue(status.Issues, "audit_invalid") {
			t.Fatalf("tampered audit was not reported: status=%+v err=%v", status, err)
		}
		if _, err := fixture.service.run(context.Background(), fixture.repository); err == nil {
			t.Fatal("tampered audit was accepted by automatic run")
		}
	})

	t.Run("config", func(t *testing.T) {
		fixture := newAutoFixture(t)
		fixture.enable(t, EnableOptions{})
		config, _, err := loadConfig(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		config.IntervalSeconds += 60
		if err := writeConfig(fixture.repository, config); err != nil {
			t.Fatal(err)
		}
		status, err := fixture.service.status(context.Background(), fixture.repository)
		if err != nil || !hasIssue(status.Issues, "config_audit_mismatch") {
			t.Fatalf("tampered config was not reported: status=%+v err=%v", status, err)
		}
		if _, err := fixture.service.run(context.Background(), fixture.repository); err == nil {
			t.Fatal("tampered config was accepted by automatic run")
		}
	})
}

func TestEnableFailureDoesNotLeaveLocalState(t *testing.T) {
	fixture := newAutoFixture(t)
	fixture.scheduler.installErr = errors.New("synthetic scheduler failure")
	_, err := fixture.service.enable(context.Background(), fixture.options(EnableOptions{}))
	if err == nil {
		t.Fatal("scheduler failure was accepted")
	}
	if _, err := os.Stat(configPath(fixture.repository)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enable left config: %v", err)
	}
	if _, err := os.Stat(eventsPath(fixture.repository)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed enable left audit: %v", err)
	}
}

func TestEnableDoesNotOverwriteOrphanedSchedule(t *testing.T) {
	fixture := newAutoFixture(t)
	id := taskID(fixture.repository)
	original := Registration{
		TaskID: id, RepositoryRoot: fixture.repository,
		ExecutablePath: fixture.executable, Interval: time.Minute,
	}
	fixture.scheduler.registrations[id] = original
	if _, err := fixture.service.enable(context.Background(), fixture.options(EnableOptions{})); err == nil {
		t.Fatal("orphaned schedule was silently overwritten")
	}
	if current := fixture.scheduler.registrations[id]; current != original {
		t.Fatalf("orphaned schedule changed: got=%+v want=%+v", current, original)
	}
	if _, err := os.Stat(configPath(fixture.repository)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected enable left config: %v", err)
	}
}

func TestExplicitStaleLockRecovery(t *testing.T) {
	fixture := newAutoFixture(t)
	lock, err := acquireOperationLock(fixture.repository, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	lock.path = ""
	status, err := fixture.service.status(context.Background(), fixture.repository)
	if err != nil || !hasIssue(status.Issues, "operation_locked") {
		t.Fatalf("stale lock was not observable: status=%+v err=%v", status, err)
	}
	cleared, err := ClearStaleLock(fixture.repository)
	if err != nil || !cleared {
		t.Fatalf("stale lock was not cleared: cleared=%v err=%v", cleared, err)
	}
	cleared, err = ClearStaleLock(fixture.repository)
	if err != nil || cleared {
		t.Fatalf("second lock recovery was not idempotent: cleared=%v err=%v", cleared, err)
	}
}

func newAutoFixture(t *testing.T) *autoFixture {
	t.Helper()
	base := t.TempDir()
	repository := filepath.Join(base, "portable")
	evidence := filepath.Join(base, "evidence")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Init(evidence); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	scheduler := newFakeScheduler()
	fixture := &autoFixture{
		scheduler: scheduler, repository: canonicalForTest(t, repository),
		evidence: canonicalForTest(t, evidence), executable: canonicalFileForTest(t, executable),
		now: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}
	fixture.service = &manager{
		scheduler: scheduler,
		now:       func() time.Time { return fixture.now },
		runSync: func(context.Context, gitsync.SyncOptions) (gitsync.Result, error) {
			return gitsync.Result{SchemaVersion: gitsync.ResultSchemaVersion, Outcome: "up_to_date", Privacy: "private_git"}, nil
		},
		export: func(string, string) (ExportSummary, error) { return ExportSummary{}, nil },
		verify: func(context.Context, string) error { return nil },
	}
	return fixture
}

func TestDefaultRepositoryVerifierAcceptsCanonicalRepository(t *testing.T) {
	repository := newVerifiedRepository(t)
	if err := verifyPortableRepository(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDirectory(repository), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath(repository), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output := runGit(t, repository, "status", "--porcelain"); strings.TrimSpace(output) != "" {
		t.Fatalf("local automatic state entered Git: %q", output)
	}
}

func newVerifiedRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "portable")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "--initial-branch", gitsync.DefaultBranch)
	runGit(t, repository, "config", "user.name", "Test User")
	runGit(t, repository, "config", "user.email", "test@example.invalid")
	if _, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: repository, InstallHooks: false,
	}); err != nil {
		t.Fatal(err)
	}
	return canonicalForTest(t, repository)
}

func (fixture *autoFixture) options(overrides EnableOptions) EnableOptions {
	overrides.RepositoryRoot = fixture.repository
	overrides.EvidenceRoot = fixture.evidence
	overrides.ExecutablePath = fixture.executable
	return overrides
}

func (fixture *autoFixture) enable(t *testing.T, overrides EnableOptions) Status {
	t.Helper()
	status, err := fixture.service.enable(context.Background(), fixture.options(overrides))
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func (fixture *autoFixture) advance(duration time.Duration) {
	fixture.now = fixture.now.Add(duration)
}

func runGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func canonicalForTest(t *testing.T, path string) string {
	t.Helper()
	value, err := canonicalDirectory(path, "test directory")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func canonicalFileForTest(t *testing.T, path string) string {
	t.Helper()
	value, err := canonicalFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
