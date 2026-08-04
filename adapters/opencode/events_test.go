package opencode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const syntheticEvents = `{"captured_at":"2026-08-04T04:00:00Z","event":{"type":"message.updated","properties":{"info":{"id":"msg_user","sessionID":"ses_one","role":"user"}}}}
{"captured_at":"2026-08-04T04:00:01Z","event":{"type":"message.part.updated","properties":{"part":{"id":"prt_text","sessionID":"ses_one","messageID":"msg_user","type":"text","text":"synthetic prompt"}}}}
{"captured_at":"2026-08-04T04:00:02Z","event":{"type":"message.part.updated","properties":{"part":{"id":"prt_reasoning","sessionID":"ses_one","messageID":"msg_assistant","type":"reasoning","text":"synthetic exposed reasoning","time":{"start":1785816002000}}}}}
{"captured_at":"2026-08-04T04:00:03Z","event":{"type":"message.part.updated","properties":{"part":{"id":"prt_tool","sessionID":"ses_one","messageID":"msg_assistant","type":"tool","callID":"call_one","tool":"read","state":{"status":"completed","input":{"path":"/synthetic"},"output":"synthetic result"}}}}}
{"captured_at":"2026-08-04T04:00:04Z","event":{"type":"permission.asked","properties":{"id":"per_one","sessionID":"ses_one","callID":"call_one","title":"Synthetic approval"}}}
{"captured_at":"2026-08-04T04:00:05Z","event":{"type":"session.compacted","properties":{"sessionID":"ses_one"}}}
{"captured_at":"2026-08-04T04:00:06Z","event":{"type":"file.edited","properties":{"sessionID":"ses_one","file":"synthetic.txt"}}}
{"captured_at":"2026-08-04T04:00:07Z","event":{"type":"future.event","properties":{"sessionID":"ses_two","value":"preserved"}}}
`

func TestImportEventSpoolPreservesLinesAndSessionOverrides(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "opencode-events.jsonl")
	if err := os.WriteFile(source, []byte(syntheticEvents), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 5, 0, 0, 0, time.UTC)
	result, err := ImportEventPath(store, source, EventOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 9 || result.GapsAppended != 0 {
		t.Fatalf("unexpected event import result: %+v", result)
	}
	wantKinds := map[ledger.EventKind]int{
		ledger.KindSystemEvent: 2, ledger.KindReasoning: 1, ledger.KindToolCall: 1,
		ledger.KindToolResult: 1, ledger.KindApproval: 1, ledger.KindCompaction: 1,
		ledger.KindFileChange: 1, ledger.KindUnknown: 1,
	}
	for kind, want := range wantKinds {
		if got := result.Kinds[string(kind)]; got != want {
			t.Fatalf("kind %s: got %d want %d; all=%+v", kind, got, want, result.Kinds)
		}
	}

	var sourceBlob *ledger.BlobRef
	seenThreads := map[string]int{}
	visibility := map[ledger.ReasoningVisibility]int{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		seenThreads[record.Event.Source.ThreadID]++
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
		t.Fatal("event spool source blob was not recorded")
	}
	preserved, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(sourceBlob.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != syntheticEvents {
		t.Fatal("preserved OpenCode event spool differs from input")
	}
	if seenThreads["ses_one"] != 8 || seenThreads["ses_two"] != 1 {
		t.Fatalf("per-event session overrides were lost: %+v", seenThreads)
	}
	if visibility[ledger.ReasoningRawExposed] != 1 {
		t.Fatalf("unexpected reasoning visibility: %+v", visibility)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}

func TestImportEventSpoolHandlesSegmentsAndRecoveredPartialTail(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(root, "spool")
	if err := os.Mkdir(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	first := `{"captured_at":"2026-08-04T04:00:00Z","event":{"type":"message.updated","properties":{"info":{"id":"msg_one","sessionID":"ses_one"}}}}` + "\n"
	second := `{"captured_at":"2026-08-04T04:00:01Z","event":{"type":"session.updated","properties":{"info":{"id":"ses_two"}}}}` + "\n"
	partial := `{"captured_at":"2026-08-04T04:00:02Z","event":{"type":"session.updated","properties":{"id":"ses_partial"}}}`
	for name, content := range map[string]string{
		"events.jsonl":                             first,
		"events.segment-one.jsonl":                 second,
		"events.recovered-synthetic.partial.jsonl": partial,
	} {
		if err := os.WriteFile(filepath.Join(spool, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 8, 4, 5, 0, 0, 0, time.UTC)
	result, err := ImportEventPath(store, spool, EventOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 3 || result.EventsAppended != 3 || result.GapsAppended != 1 ||
		result.Kinds[string(ledger.KindSystemEvent)] != 2 {
		t.Fatalf("unexpected segmented import: %+v", result)
	}

	third := `{"captured_at":"2026-08-04T04:00:03Z","event":{"type":"session.idle","properties":{"sessionID":"ses_one"}}}` + "\n"
	active, err := os.OpenFile(filepath.Join(spool, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := active.WriteString(third); err != nil {
		_ = active.Close()
		t.Fatal(err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = ImportEventPath(store, spool, EventOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 1 || result.GapsAppended != 0 {
		t.Fatalf("incremental segmented import failed: %+v", result)
	}

	foundPartial := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindGap &&
			strings.Contains(record.Event.Completeness.Reason, "trailing_partial_json") {
			foundPartial = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundPartial {
		t.Fatal("recovered partial tail did not produce an explicit gap")
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}
