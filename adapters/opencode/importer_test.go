package opencode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const syntheticExport = `{
  "info": {
    "id": "ses_synthetic",
    "projectID": "project_synthetic",
    "directory": "/synthetic/workspace",
    "title": "Synthetic session",
    "version": "1.0.0",
    "time": {"created": 1785805200000, "updated": 1785805260000}
  },
  "messages": [
    {
      "info": {"id": "msg_user", "sessionID": "ses_synthetic", "role": "user", "time": {"created": 1785805201000}},
      "parts": [
        {"id": "prt_user_text", "sessionID": "ses_synthetic", "messageID": "msg_user", "type": "text", "text": "synthetic instruction", "time": {"start": 1785805201000}},
        {"id": "prt_file", "sessionID": "ses_synthetic", "messageID": "msg_user", "type": "file", "mime": "text/plain", "url": "data:text/plain;base64,c3ludGhldGlj"}
      ]
    },
    {
      "info": {"id": "msg_assistant", "sessionID": "ses_synthetic", "role": "assistant", "time": {"created": 1785805202000, "completed": 1785805250000}},
      "parts": [
        {"id": "prt_reasoning", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "reasoning", "text": "locally exposed synthetic reasoning", "time": {"start": 1785805202000, "end": 1785805203000}},
        {"id": "prt_text", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "text", "text": "synthetic response"},
        {"id": "prt_tool", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "tool", "callID": "call_synthetic", "tool": "read", "state": {"status": "completed", "input": {"path": "/synthetic"}, "output": "synthetic tool result", "title": "Read", "metadata": {}, "time": {"start": 1785805204000, "end": 1785805205000}}},
        {"id": "prt_patch", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "patch", "hash": "synthetic", "files": ["synthetic.txt"]},
        {"id": "prt_compaction", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "compaction", "auto": true},
        {"id": "prt_subtask", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "subtask", "prompt": "synthetic subtask", "description": "synthetic", "agent": "general"},
        {"id": "prt_retry", "sessionID": "ses_synthetic", "messageID": "msg_assistant", "type": "retry", "attempt": 1, "error": {"name": "SyntheticError"}, "time": {"created": 1785805206000}}
      ]
    }
  ]
}
`

func TestImportExportPreservesDocumentAndProjectsAllParts(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "ses_synthetic.json")
	if err := os.WriteFile(source, []byte(syntheticExport), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSnapshots != 1 || result.EventsAppended != 13 ||
		result.GapsAppended != 0 || result.FilesChanged != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	wantKinds := map[ledger.EventKind]int{
		ledger.KindSystemEvent: 4, ledger.KindUserMessage: 1, ledger.KindAttachment: 1,
		ledger.KindReasoning: 1, ledger.KindAgentMessage: 1, ledger.KindToolCall: 1,
		ledger.KindToolResult: 1, ledger.KindFileChange: 1, ledger.KindCompaction: 1,
		ledger.KindSubagentEvent: 1,
	}
	for kind, want := range wantKinds {
		if got := result.Kinds[string(kind)]; got != want {
			t.Fatalf("kind %s: got %d want %d; all=%+v", kind, got, want, result.Kinds)
		}
	}

	var sourceBlob *ledger.BlobRef
	visibility := map[ledger.ReasoningVisibility]int{}
	toolEvents := 0
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindSourceSnapshot && record.Event.Payload.Blob != nil {
			sourceBlob = record.Event.Payload.Blob
		}
		if record.Event.Reasoning != nil {
			visibility[record.Event.Reasoning.Visibility]++
		}
		if record.Event.Kind == ledger.KindToolCall || record.Event.Kind == ledger.KindToolResult {
			toolEvents++
			if record.Event.Causality.CallID != "call_synthetic" {
				t.Fatalf("tool correlation lost: %+v", record.Event.Causality)
			}
		}
		if record.Event.Source.ThreadID != "ses_synthetic" {
			t.Fatalf("unexpected thread id: %s", record.Event.Source.ThreadID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sourceBlob == nil {
		t.Fatal("source export blob was not recorded")
	}
	preserved, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(sourceBlob.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != syntheticExport {
		t.Fatal("preserved OpenCode export bytes differ from input")
	}
	if visibility[ledger.ReasoningRawExposed] != 1 || toolEvents != 2 {
		t.Fatalf("unexpected reasoning/tool projections: visibility=%+v tools=%d", visibility, toolEvents)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}

	second, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceSnapshots != 0 || second.EventsAppended != 0 ||
		second.GapsAppended != 0 || second.FilesChanged != 0 {
		t.Fatalf("idempotent import changed ledger: %+v", second)
	}
}

func TestSanitizedExportIsExplicitlyPartial(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "sanitized.json")
	data := `{"info":{"id":"ses_sanitized","time":{"updated":1785805260000}},"messages":[{"info":{"id":"msg_1","sessionID":"ses_sanitized","role":"assistant","time":{"created":1785805200000}},"parts":[{"id":"prt_1","sessionID":"ses_sanitized","messageID":"msg_1","type":"reasoning","text":"[redacted:reasoning:prt_1]","time":{"start":1785805200000}}]}]}`
	if err := os.WriteFile(source, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.GapsAppended != 1 {
		t.Fatalf("sanitized export did not append a gap: %+v", result)
	}
	foundPartialReasoning := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindReasoning {
			foundPartialReasoning = record.Event.Completeness.Status == ledger.CompletenessPartial &&
				record.Event.Reasoning.Visibility == ledger.ReasoningNotExposed
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundPartialReasoning {
		t.Fatal("sanitized reasoning was presented as complete or exposed")
	}
}
