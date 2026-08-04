package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const syntheticRollout = `{"timestamp":"2026-08-04T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555","cwd":"/synthetic/workspace"}}
{"timestamp":"2026-08-04T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"synthetic user instruction"}}
{"timestamp":"2026-08-04T00:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic reply"}]}}
{"timestamp":"2026-08-04T00:00:03Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"synthetic summary"}],"encrypted_content":"opaque-synthetic"}}
{"timestamp":"2026-08-04T00:00:04Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"synthetic exposed reasoning"}}
{"timestamp":"2026-08-04T00:00:05Z","type":"response_item","payload":{"type":"function_call","id":"call-item-1","call_id":"call-1","name":"read_file","arguments":"{}"}}
{"timestamp":"2026-08-04T00:00:06Z","type":"response_item","payload":{"type":"function_call_output","id":"output-1","call_id":"call-1","output":"synthetic result"}}
{"timestamp":"2026-08-04T00:00:07Z","type":"compacted","payload":{"window_id":"window-2","previous_window_id":"window-1","message":"synthetic compaction","replacement_history":[]}}
{"timestamp":"2026-08-04T00:00:08Z","type":"future_event","payload":{"type":"future_payload","value":"preserve unknown"}}
`

func TestImportPreservesExactSourceAndClassifiesEvents(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "rollout-2026-08-04T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(source, []byte(syntheticRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 9 || result.GapsAppended != 0 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if result.Kinds[string(ledger.KindReasoning)] != 2 ||
		result.Kinds[string(ledger.KindToolCall)] != 1 ||
		result.Kinds[string(ledger.KindToolResult)] != 1 ||
		result.Kinds[string(ledger.KindCompaction)] != 1 ||
		result.Kinds[string(ledger.KindUnknown)] != 1 {
		t.Fatalf("unexpected kind counts: %+v", result.Kinds)
	}

	var sourceBlob *ledger.BlobRef
	visibility := map[ledger.ReasoningVisibility]int{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindSourceSnapshot {
			sourceBlob = record.Event.Payload.Blob
		}
		if record.Event.Reasoning != nil {
			visibility[record.Event.Reasoning.Visibility]++
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
	if string(preserved) != syntheticRollout {
		t.Fatal("preserved source bytes differ from input")
	}
	if visibility[ledger.ReasoningSummaryOnly] != 1 ||
		visibility[ledger.ReasoningRawExposed] != 1 {
		t.Fatalf("unexpected reasoning visibility: %+v", visibility)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}

	second, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceSegments != 0 || second.EventsAppended != 0 || second.BytesCaptured != 0 {
		t.Fatalf("idempotent import changed the ledger: %+v", second)
	}

	appendLine := `{"timestamp":"2026-08-04T00:00:09Z","type":"event_msg","payload":{"type":"agent_message","message":"synthetic append"}}` + "\n"
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

func TestTrailingPartialRecordIsPreservedAndRetried(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "rollout-partial.jsonl")
	partial := strings.TrimSuffix(syntheticRollout, "\n") + `
{"timestamp":"2026-08-04T00:00:09Z","type":"event_msg","payload":`
	if err := os.WriteFile(source, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	first, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if first.GapsAppended != 1 {
		t.Fatalf("partial record was not reported: %+v", first)
	}
	completion := `{"type":"agent_message","message":"completed"}}` + "\n"
	file, err := os.OpenFile(source, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(completion); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsAppended != 1 || second.GapsAppended != 0 {
		t.Fatalf("completed partial record was not reconciled: %+v", second)
	}
}

func TestTruncationAppendsGapInsteadOfSilentlyRewinding(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "rollout-truncated.jsonl")
	if err := os.WriteFile(source, []byte(syntheticRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	if _, err := ImportPath(store, source, Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	replacement := `{"timestamp":"2026-08-04T02:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555"}}` + "\n"
	if err := os.WriteFile(source, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.GapsAppended != 1 || result.SourceSegments != 1 || result.EventsAppended != 2 {
		t.Fatalf("truncation was not made explicit: %+v", result)
	}
}
