package diagnostics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/backup"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestDoctorAcceptsHealthyLocalStoreAndCanRequireRepository(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	report := Run(context.Background(), store, Options{Now: func() time.Time { return now }})
	if !report.Ready || len(report.Issues) != 0 || report.RepositoryChecked ||
		report.CheckedAt != now || report.Platform == "" {
		t.Fatalf("unexpected healthy report: %+v", report)
	}
	required := Run(context.Background(), store, Options{
		RequireRepository: true, Now: func() time.Time { return now },
	})
	if required.Ready || !hasIssue(required, "git_sync", "repository_required") {
		t.Fatalf("missing required repository was not reported: %+v", required)
	}
}

func TestDoctorIsNotReadyWhileEvidenceWriterLockExists(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appender, err := store.NewAppender()
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, Options{})
	if report.Ready || !hasIssue(report, "evidence", "writer_lock_present") {
		_ = appender.Close()
		t.Fatalf("doctor ignored the evidence writer lock: %+v", report)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	report = Run(context.Background(), store, Options{})
	if !report.Ready || hasIssue(report, "evidence", "writer_lock_present") {
		t.Fatalf("doctor remained blocked after writer lock cleanup: %+v", report)
	}
}

func TestDoctorHoldsEvidenceWriterLockThroughVerification(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	locked := make(chan struct{})
	continueDoctor := make(chan struct{})
	done := make(chan Report, 1)
	go func() {
		done <- runWithHooks(context.Background(), store, Options{}, runHooks{
			afterEvidenceLock: func() {
				close(locked)
				<-continueDoctor
			},
		})
	}()
	<-locked
	if _, err := store.NewAppender(); !errors.Is(err, ledger.ErrWriterLocked) {
		close(continueDoctor)
		t.Fatalf("writer entered while doctor was verifying the evidence prefix: %v", err)
	}
	close(continueDoctor)
	if report := <-done; !report.Ready {
		t.Fatalf("doctor failed after holding a stable evidence prefix: %+v", report)
	}
	appender, err := store.NewAppender()
	if err != nil {
		t.Fatalf("doctor did not release the evidence writer lock: %v", err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorAcceptsVerifiedPrivateMemoryRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(base, "portable")
	if _, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: repository,
	}); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, Options{
		Repository: repository, RequireRepository: true,
	})
	if !report.Ready || !report.RepositoryChecked || report.GitSync == nil ||
		len(report.GitSync.Issues) != 0 {
		t.Fatalf("verified recovery repositories were not ready: %+v", report)
	}
}

func TestNewDeviceRecoveryAcceptance(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Test User")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	base := t.TempDir()
	source, err := ledger.Init(filepath.Join(base, "source"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(400, 0).UTC()
	payload := ledger.InlinePayload("utf-8", "text/plain", "recovery evidence")
	if _, err := source.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "recovery-acceptance-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "recovery-test", AdapterVersion: "recovery-test/v1",
			DeviceID: source.DeviceID(), ThreadID: "recovery-acceptance-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	key, err := backup.GenerateIdentity(filepath.Join(base, "keys", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "backups", "evidence.age")
	if _, err := backup.Create(source, backup.CreateOptions{
		Output: archive, Recipients: []string{key.Recipient},
	}); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(base, "restored")
	if _, err := backup.Restore(backup.RestoreOptions{
		Archive: archive, IdentityPaths: []string{key.IdentityPath}, Target: restoredPath,
	}); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(base, "portable")
	if _, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: repository,
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := ledger.Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), restored, Options{
		Repository: repository, RequireRepository: true,
	})
	if !report.Ready || report.Evidence.RecordsChecked != 2 {
		t.Fatalf("new-device recovery did not pass acceptance: %+v", report)
	}
}

func TestDoctorFindsEvidenceTamperingAndStorageOverlap(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	payload := ledger.InlinePayload("utf-8", "text/plain", "original")
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "doctor-test-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "doctor-test", AdapterVersion: "doctor-test/v1",
			DeviceID: store.DeviceID(), ThreadID: "doctor-test-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "evidence", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "original", "tampered", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), store, Options{})
	if report.Ready || !hasIssue(report, "evidence", "integrity_failed") {
		t.Fatalf("evidence tampering was not reported: %+v", report)
	}

	clean, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	overlap := Run(context.Background(), clean, Options{Repository: clean.Root()})
	if overlap.Ready || !hasIssue(overlap, "storage", "storage_zones_overlap") {
		t.Fatalf("overlapping storage zones were not reported: %+v", overlap)
	}
}

func hasIssue(report Report, component, code string) bool {
	for _, issue := range report.Issues {
		if issue.Component == component && issue.Code == code {
			return true
		}
	}
	return false
}
