package opencode

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type fakeCommandRunner struct {
	calls [][]string
}

func (runner *fakeCommandRunner) Run(
	_ context.Context, stdout, stderr io.Writer, binary string, args ...string,
) error {
	call := append([]string{binary}, args...)
	runner.calls = append(runner.calls, call)
	joined := strings.Join(args, " ")
	switch joined {
	case "session list --format json":
		_, _ = io.WriteString(stdout, `[{"id":"ses_good"},{"id":"ses_bad"}]`+"\n")
		return nil
	case "export ses_good":
		_, _ = io.WriteString(stderr, "Exporting session: ses_good\n")
		_, _ = io.WriteString(stdout,
			`{"info":{"id":"ses_good","time":{"created":1785805200000,"updated":1785805260000}},"messages":[{"info":{"id":"msg_good","sessionID":"ses_good","role":"user","time":{"created":1785805201000}},"parts":[{"id":"prt_good","sessionID":"ses_good","messageID":"msg_good","type":"text","text":"synthetic prompt"}]}]}`+"\n")
		return nil
	case "export ses_bad":
		_, _ = io.WriteString(stderr, "synthetic export failure\n")
		_, _ = io.WriteString(stdout, `{"info":{"id":"ses_bad"}`)
		return errors.New("synthetic command failure")
	default:
		return errors.New("unexpected command: " + joined)
	}
}

func TestCaptureAllPreservesCommandArtifactsAndReportsPerSessionGaps(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{}
	now := time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC)
	result, captureErr := CaptureAll(context.Background(), store, CaptureOptions{
		Binary: "opencode-synthetic", StagingRoot: filepath.Join(root, "staging"),
		Now: func() time.Time { return now }, Runner: runner,
	})
	if captureErr == nil {
		t.Fatal("partial OpenCode capture returned success")
	}
	if result.SessionsListed != 2 || result.ExportsWritten != 2 ||
		result.ExportsSucceeded != 1 || result.ExportsFailed != 1 ||
		result.GapsAppended != 1 || result.Import.GapsAppended != 1 {
		t.Fatalf("unexpected capture result: %+v", result)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("unexpected commands: %+v", runner.calls)
	}
	for _, call := range runner.calls {
		for _, argument := range call {
			if argument == "--sanitize" {
				t.Fatalf("capture requested sanitized export: %+v", call)
			}
		}
	}

	foundFailureStderr := false
	foundCommandGap := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Source.Adapter == CaptureAdapterName &&
			record.Event.Kind == ledger.KindGap && record.Event.Source.ThreadID == "ses_bad" {
			foundCommandGap = true
		}
		if record.Event.Source.Adapter == CaptureAdapterName && record.Event.Payload != nil &&
			record.Event.Payload.Blob != nil && strings.Contains(record.Event.Source.SourceCursor, ".stderr.log") {
			data, err := os.ReadFile(filepath.Join(store.Root(),
				filepath.FromSlash(record.Event.Payload.Blob.RelativePath)))
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "synthetic export failure") {
				foundFailureStderr = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundFailureStderr || !foundCommandGap {
		t.Fatalf("failure evidence missing: stderr=%t gap=%t", foundFailureStderr, foundCommandGap)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}

func TestPreparedCaptureBindsStoreSessionListAndStaging(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := PrepareCapture(context.Background(), store, CaptureOptions{
		Binary: "opencode-synthetic", StagingRoot: filepath.Join(root, "staging"),
		Now:    func() time.Time { return time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC) },
		Runner: &fakeCommandRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prepared.sessionListPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CapturePrepared(context.Background(), store, prepared); err == nil {
		t.Fatal("prepared capture accepted a modified persisted session list")
	}
	otherStore, err := ledger.Init(filepath.Join(root, "other-store"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CapturePrepared(context.Background(), otherStore, prepared); err == nil {
		t.Fatal("prepared capture was reusable against a different evidence store")
	}
}

func TestCaptureRejectsStagingInsideGitWorktree(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = CaptureAll(context.Background(), store, CaptureOptions{
		StagingRoot: filepath.Join(root, "raw"), Runner: &fakeCommandRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "outside a Git worktree") {
		t.Fatalf("Git staging was not rejected: %v", err)
	}
}

func TestCaptureRejectsOverlappingStagingAndEvidenceRoots(t *testing.T) {
	for _, test := range []struct {
		name        string
		storePath   func(string) string
		stagingPath func(string) string
	}{
		{
			name:      "staging inside evidence",
			storePath: func(root string) string { return filepath.Join(root, "store") },
			stagingPath: func(root string) string {
				return filepath.Join(root, "store", "raw")
			},
		},
		{
			name: "evidence inside staging",
			storePath: func(root string) string {
				return filepath.Join(root, "staging", "store")
			},
			stagingPath: func(root string) string { return filepath.Join(root, "staging") },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := ledger.Init(test.storePath(root))
			if err != nil {
				t.Fatal(err)
			}
			_, err = CaptureAll(context.Background(), store, CaptureOptions{
				StagingRoot: test.stagingPath(root), Runner: &fakeCommandRunner{},
			})
			if err == nil || !strings.Contains(err.Error(), "must be separate directories") {
				t.Fatalf("overlapping paths were not rejected: %v", err)
			}
		})
	}
}

func TestDecodeSessionListRejectsMissingIDs(t *testing.T) {
	if _, err := decodeSessionList([]byte(`[{"title":"missing id"}]`)); err == nil {
		t.Fatal("session-list entry without id was silently dropped")
	}
}

func TestValidateExportDetectsNativeSanitization(t *testing.T) {
	data := []byte(`{"info":{"id":"ses_redacted","title":"[redacted:session-title:ses_redacted]","directory":"[redacted:session-directory:ses_redacted]"},"messages":[]}`)
	if reason := validateExportForSession(data, "ses_redacted"); reason != "export_sanitized" {
		t.Fatalf("sanitized export was not detected: %q", reason)
	}
}

type cancelingCommandRunner struct{}

func (cancelingCommandRunner) Run(
	_ context.Context, stdout, _ io.Writer, _ string, args ...string,
) error {
	if strings.Join(args, " ") == "session list --format json" {
		_, _ = io.WriteString(stdout, `[{"id":"ses_first"},{"id":"ses_unattempted"}]`)
		return nil
	}
	return context.Canceled
}

func TestCaptureCancellationRecordsUnattemptedSessions(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 6, 0, 0, 0, time.UTC)
	result, err := CaptureAll(context.Background(), store, CaptureOptions{
		StagingRoot: filepath.Join(root, "staging"), Runner: cancelingCommandRunner{},
		Now: func() time.Time { return now },
	})
	if err == nil || result.ExportsFailed != 2 || result.GapsAppended != 2 {
		t.Fatalf("cancellation gaps missing: result=%+v err=%v", result, err)
	}
	found := map[string]bool{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Source.Adapter == CaptureAdapterName && record.Event.Kind == ledger.KindGap {
			found[record.Event.Source.ThreadID] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found["ses_first"] || !found["ses_unattempted"] {
		t.Fatalf("missing cancellation gap threads: %+v", found)
	}
}
