package claudecode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const syntheticTranscript = `{"type":"user","uuid":"u-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:00Z","message":{"role":"user","content":"synthetic instruction"}}
{"type":"assistant","uuid":"a-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:01Z","message":{"id":"msg-1","role":"assistant","content":[{"type":"text","text":"synthetic reply"},{"type":"thinking","thinking":"locally exposed synthetic thought","signature":"synthetic"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"/synthetic"}}]}}
{"type":"user","uuid":"u-2","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"synthetic result"}]}}
{"type":"assistant","uuid":"a-2","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:03Z","message":{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque-synthetic"}]}}
{"type":"system","subtype":"compact_boundary","uuid":"c-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:04Z","compactMetadata":{"trigger":"auto","preTokens":1000}}
{"type":"summary","summary":"synthetic compacted summary","leafUuid":"leaf-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:05Z"}
{"type":"assistant","uuid":"side-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:06Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"synthetic subagent reply"}]}}
{"type":"future-record","uuid":"future-1","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:07Z","payload":{"preserve":true}}
`

func TestImportPreservesClaudeTranscriptAndProjectsBlocks(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(source, []byte(syntheticTranscript), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)
	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 10 || result.GapsAppended != 0 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	wantKinds := map[ledger.EventKind]int{
		ledger.KindUserMessage: 1, ledger.KindAgentMessage: 1, ledger.KindReasoning: 2,
		ledger.KindToolCall: 1, ledger.KindToolResult: 1, ledger.KindCompaction: 2,
		ledger.KindSubagentEvent: 1, ledger.KindUnknown: 1,
	}
	for kind, want := range wantKinds {
		if got := result.Kinds[string(kind)]; got != want {
			t.Fatalf("kind %s: got %d want %d; all=%+v", kind, got, want, result.Kinds)
		}
	}

	var sourceBlob *ledger.BlobRef
	visibility := map[ledger.ReasoningVisibility]int{}
	eventIDs := map[string]struct{}{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, duplicate := eventIDs[record.Event.EventID]; duplicate {
			t.Fatalf("duplicate event ID %s", record.Event.EventID)
		}
		eventIDs[record.Event.EventID] = struct{}{}
		if record.Event.Kind == ledger.KindSourceSnapshot {
			sourceBlob = record.Event.Payload.Blob
		}
		if record.Event.Reasoning != nil {
			visibility[record.Event.Reasoning.Visibility]++
		}
		if record.Event.Source.ThreadID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
			t.Fatalf("unexpected thread id: %s", record.Event.Source.ThreadID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sourceBlob == nil {
		t.Fatal("source segment blob was not recorded")
	}
	preserved, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(sourceBlob.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != syntheticTranscript {
		t.Fatal("preserved source bytes differ from Claude transcript")
	}
	if visibility[ledger.ReasoningRawExposed] != 1 ||
		visibility[ledger.ReasoningEncryptedOpaque] != 1 {
		t.Fatalf("unexpected reasoning visibility: %+v", visibility)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}

func TestClaudeTranscriptImportIsIncrementalAndIdempotent(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	firstLine := `{"type":"user","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:00Z","message":{"role":"user","content":"first"}}` + "\n"
	if err := os.WriteFile(source, []byte(firstLine), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)
	if _, err := ImportPath(store, source, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	second, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsAppended != 0 || second.SourceSegments != 0 || second.BytesCaptured != 0 {
		t.Fatalf("idempotent import changed ledger: %+v", second)
	}
	appendLine := `{"type":"assistant","sessionId":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","timestamp":"2026-08-04T01:00:01Z","message":{"role":"assistant","content":"second"}}` + "\n"
	file, err := os.OpenFile(source, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(appendLine); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	incremental, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if incremental.SourceSegments != 1 || incremental.EventsAppended != 1 ||
		incremental.BytesCaptured != int64(len(appendLine)) {
		t.Fatalf("unexpected incremental import: %+v", incremental)
	}
}
