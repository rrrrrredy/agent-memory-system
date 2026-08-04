package hookcapture

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestCaptureStreamsAndReconcilesInputLargerThanLegacyLimit(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"session_id":"session-large","transcript_path":"D:\\sessions\\rollout-large.jsonl","hook_event_name":"UserPromptSubmit","turn_id":"turn-large","prompt":"` +
		strings.Repeat("x", 8*1024*1024+257) + `"}`)
	result, err := Capture(store, ledger.AgentCodex, bytes.NewReader(raw), CaptureOptions{
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RawBytes != int64(len(raw)) || result.Status != "complete" {
		t.Fatalf("unexpected capture result: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(result.Relative)))
	if err != nil {
		t.Fatal(err)
	}
	_, decoded, err := DecodeEnvelope(bytes.TrimSpace(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatal("captured hook bytes differ from stdin")
	}
	spoolReport := VerifySpools(store)
	if len(spoolReport.Issues) != 0 || spoolReport.FilesChecked != 1 ||
		spoolReport.CompleteCaptures != 1 || spoolReport.RawBytes != int64(len(raw)) {
		t.Fatalf("unexpected spool verification: %+v", spoolReport)
	}
	imported, err := ImportPath(store, ledger.AgentCodex, SpoolRoot(store, ledger.AgentCodex), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if imported.FilesExamined != 1 || imported.SourceSegments != 1 ||
		imported.Kinds[string(ledger.KindUserMessage)] != 1 || imported.GapsAppended != 0 {
		t.Fatalf("unexpected hook import result: %+v", imported)
	}
	if verification := store.Verify(); len(verification.Issues) != 0 {
		t.Fatalf("captured evidence is invalid: %+v", verification)
	}
}

func TestPartialHookReadIsPreservedAndProjectedAsGap(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, captureErr := Capture(store, ledger.AgentClaudeCode,
		&failingReader{data: []byte(`{"session_id":"partial`)}, CaptureOptions{})
	if captureErr == nil || result.Status != "partial" || result.RawBytes == 0 {
		t.Fatalf("partial read was not reported: result=%+v err=%v", result, captureErr)
	}
	if report := VerifySpools(store); len(report.Issues) != 0 || report.PartialCaptures != 1 {
		t.Fatalf("partial capture envelope did not verify: %+v", report)
	}
	imported, err := ImportPath(store, ledger.AgentClaudeCode,
		SpoolRoot(store, ledger.AgentClaudeCode), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if imported.GapsAppended != 1 || imported.Kinds[string(ledger.KindGap)] != 1 {
		t.Fatalf("partial hook input did not become a gap: %+v", imported)
	}
}

func TestSpoolVerificationDetectsEnvelopeTampering(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := Capture(store, ledger.AgentCodex,
		strings.NewReader(`{"session_id":"tamper","transcript_path":null,"hook_event_name":"SessionStart"}`),
		CaptureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root(), filepath.FromSlash(result.Relative))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(result.RawSHA256), []byte(strings.Repeat("0", 64)), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report := VerifySpools(store)
	if len(report.Issues) != 1 || report.FilesChecked != 1 {
		t.Fatalf("tampered hook envelope was not rejected: %+v", report)
	}
}

func TestMissingTranscriptHintBecomesExplicitGap(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"session-no-path","transcript_path":null,"hook_event_name":"SessionStart","source":"startup"}`
	if _, err := Capture(store, ledger.AgentCodex, strings.NewReader(raw), CaptureOptions{}); err != nil {
		t.Fatal(err)
	}
	imported, err := ImportPath(store, ledger.AgentCodex,
		SpoolRoot(store, ledger.AgentCodex), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Kinds[string(ledger.KindSystemEvent)] != 1 || imported.GapsAppended != 1 {
		t.Fatalf("missing transcript path was not explicit: %+v", imported)
	}
}

func TestHookEventsProjectProcessKindsAndStableIdentifiers(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"session-kinds","transcript_path":"D:\\sessions\\rollout.jsonl",` +
		`"hook_event_name":"PostCompact","turn_id":"turn-1","compaction_id":"compact-1"}`
	if _, err := Capture(store, ledger.AgentClaudeCode, strings.NewReader(raw), CaptureOptions{}); err != nil {
		t.Fatal(err)
	}
	imported, err := ImportPath(store, ledger.AgentClaudeCode,
		SpoolRoot(store, ledger.AgentClaudeCode), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Kinds[string(ledger.KindCompaction)] != 1 {
		t.Fatalf("compaction hook was not classified: %+v", imported)
	}
	found := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindCompaction {
			found = record.Event.Causality != nil &&
				record.Event.Causality.CompactionID == "compact-1" &&
				record.Event.Source.SourceEventID == "turn-1"
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("compaction hook identifiers were not retained")
	}
}

func TestHookSemanticOmissionsBecomeExplicitGaps(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"session-missing-summary","transcript_path":"D:\\sessions\\rollout.jsonl",` +
		`"hook_event_name":"PostCompact"}`
	if _, err := Capture(store, ledger.AgentCodex, strings.NewReader(raw), CaptureOptions{}); err != nil {
		t.Fatal(err)
	}
	imported, err := ImportPath(store, ledger.AgentCodex,
		SpoolRoot(store, ledger.AgentCodex), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Kinds[string(ledger.KindCompaction)] != 1 || imported.GapsAppended != 1 {
		t.Fatalf("missing compact summary was not explicit: %+v", imported)
	}
}

func TestConcurrentHookCapturesCommitDistinctImmutableFiles(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const captures = 16
	var group sync.WaitGroup
	errorsSeen := make(chan error, captures)
	for index := 0; index < captures; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := Capture(store, ledger.AgentCodex,
				strings.NewReader(`{"session_id":"concurrent","transcript_path":"D:\\sessions\\rollout.jsonl","hook_event_name":"Stop"}`),
				CaptureOptions{})
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(SpoolRoot(store, ledger.AgentCodex))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != captures {
		t.Fatalf("captured %d files, want %d", len(entries), captures)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("unexpected spool entry %q", entry.Name())
		}
	}
}

func TestReconcileImportsHookAndCodexTranscript(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	rollout := filepath.Join(source, "rollout-2027-01-15T08-00-00-00000000-0000-0000-0000-000000000001.jsonl")
	transcript := "" +
		`{"timestamp":"2027-01-15T08:00:00Z","type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000001"}}` + "\n" +
		`{"timestamp":"2027-01-15T08:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"keep all evidence"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := `{"session_id":"00000000-0000-0000-0000-000000000001","transcript_path":` + quoted(rollout) + `,"hook_event_name":"SessionEnd"}`
	if _, err := Capture(store, ledger.AgentCodex, strings.NewReader(hook), CaptureOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(store, ledger.AgentCodex, ReconcileOptions{SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.HookSpool == nil || result.Codex == nil || result.GapsAppended != 0 || len(result.Issues) != 0 {
		t.Fatalf("unexpected reconciliation result: %+v", result)
	}
	if result.HookSpool.Kinds[string(ledger.KindSystemEvent)] != 1 ||
		result.Codex.Kinds[string(ledger.KindUserMessage)] != 1 {
		t.Fatalf("reconciliation missed hook or transcript evidence: %+v", result)
	}
}

func TestReconcileFailureAppendsLocalGap(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(t.TempDir(), "missing")
	result, reconcileErr := Reconcile(store, ledger.AgentCodex,
		ReconcileOptions{SourcePath: missingPath})
	if reconcileErr == nil || result.GapsAppended != 1 || len(result.Issues) != 1 {
		t.Fatalf("missing source did not produce an explicit gap: result=%+v err=%v", result, reconcileErr)
	}
	gapFound := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindGap &&
			strings.HasPrefix(record.Event.Source.SourceCursor, "reconcile:") {
			gapFound = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !gapFound {
		t.Fatal("reconciliation gap was not retained")
	}
	second, secondErr := Reconcile(store, ledger.AgentCodex,
		ReconcileOptions{SourcePath: missingPath})
	if secondErr == nil || second.GapsAppended != 0 || len(second.Issues) != 1 {
		t.Fatalf("repeated missing source duplicated its gap: result=%+v err=%v", second, secondErr)
	}
}

func TestCaptureRejectsLinkedSpoolPath(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spoolParent := filepath.Join(store.Root(), "evidence", "hook-spool")
	if err := os.MkdirAll(spoolParent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	linked := filepath.Join(spoolParent, string(ledger.AgentCodex))
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := Capture(store, ledger.AgentCodex, strings.NewReader(`{"session_id":"linked"}`),
		CaptureOptions{}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("linked spool path was not rejected: %v", err)
	}
}

func TestReconcileRejectsTranscriptHintOutsideConfiguredSource(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	inside := filepath.Join(source, "rollout-2027-01-15T08-00-00-00000000-0000-0000-0000-000000000002.jsonl")
	transcript := `{"timestamp":"2027-01-15T08:00:00Z","type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000002"}}` + "\n"
	if err := os.WriteFile(inside, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "rollout-outside.jsonl")
	if err := os.WriteFile(outside, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	hook := `{"session_id":"00000000-0000-0000-0000-000000000002","transcript_path":` + quoted(outside) + `,"hook_event_name":"SessionEnd"}`
	if _, err := Capture(store, ledger.AgentCodex, strings.NewReader(hook), CaptureOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(store, ledger.AgentCodex, ReconcileOptions{SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.GapsAppended != 1 || len(result.Issues) != 1 ||
		!strings.Contains(result.Issues[0], "source_hint_outside_configured_root") {
		t.Fatalf("outside transcript hint was not quarantined: %+v", result)
	}
}

type failingReader struct {
	data []byte
	done bool
}

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if len(reader.data) > 0 {
		count := copy(buffer, reader.data)
		reader.data = reader.data[count:]
		return count, nil
	}
	if !reader.done {
		reader.done = true
		return 0, io.ErrUnexpectedEOF
	}
	return 0, io.EOF
}

func quoted(value string) string {
	var buffer bytes.Buffer
	buffer.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\', '"':
			buffer.WriteByte('\\')
			buffer.WriteRune(character)
		default:
			buffer.WriteRune(character)
		}
	}
	buffer.WriteByte('"')
	return buffer.String()
}
