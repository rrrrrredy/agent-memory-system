package deepseekharness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const duplicateEvent = `{"type":"user/message","seq":4,"time":1787011200000,"data":{"role":"user","id":"user-1","content":[{"type":"text","text":"private harness prompt"}],"source":{"kind":"user"}},"surfaceOp":"append"}`

func TestImportHarnessCapturePreservesRawEvidenceProjectsEventsAndDeduplicatesBackfill(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "deepseek-harness-events.jsonl")
	artifact := "{\"type\":\"session\",\"version\":0,\"id\":\"session-one\",\"createdAt\":1787011199000}\n" +
		duplicateEvent + "\n" +
		`{"type":"assistant/message","seq":5,"time":1787011201000,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"reasoning","text":"locally exposed reasoning"},{"type":"text","text":"answer"}]}},"surfaceOp":"append"}` + "\n"
	content := strings.Join([]string{
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:00Z","kind":"session_event","session":{"id":"session-one","header":{"version":0,"id":"session-one"}},"event":` + duplicateEvent + `}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:01Z","kind":"session_event","session":{"id":"session-one"},"event":{"type":"assistant/chunk","seq":6,"time":1787011202000,"data":{"turn":1,"step":1,"chunk":{"type":"reasoning-delta","index":0,"text":"raw delta"}}}}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:02Z","kind":"session_event","session":{"id":"session-one"},"event":{"type":"tool/call","seq":7,"time":1787011203000,"data":{"turn":1,"step":1,"callId":"call-one","name":"read","arguments":"{}"}}}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:03Z","kind":"session_event","session":{"id":"session-one"},"event":{"type":"future/event","seq":8,"time":1787011204000,"data":{"future":"preserved"}}}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:04Z","kind":"raw_artifact","session":{"id":"session-one"},"revision":"rev-one","artifact":{"meta":{"version":0,"id":"session-one"},"filename":"session.jsonl","content":` + quoted(artifact) + `}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	result, err := ImportEventPath(store, source, EventOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 5 || result.EventsSkipped != 1 ||
		result.GapsAppended != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	if result.Kinds[string(ledger.KindUserMessage)] != 1 ||
		result.Kinds[string(ledger.KindAgentMessage)] != 1 ||
		result.Kinds[string(ledger.KindReasoning)] != 1 ||
		result.Kinds[string(ledger.KindToolCall)] != 1 ||
		result.Kinds[string(ledger.KindGap)] != 1 {
		t.Fatalf("unexpected projected kinds: %+v", result.Kinds)
	}

	var sourceBlob *ledger.BlobRef
	rawReasoning := 0
	unknownGap := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindSourceSnapshot {
			sourceBlob = record.Event.Payload.Blob
		}
		if record.Event.Reasoning != nil &&
			record.Event.Reasoning.Visibility == ledger.ReasoningRawExposed {
			rawReasoning++
		}
		if record.Event.Kind == ledger.KindGap &&
			strings.Contains(record.Event.Completeness.Reason, "unknown_harness_event_type_future/event") {
			unknownGap = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if sourceBlob == nil {
		t.Fatal("exact capture source was not preserved")
	}
	preserved, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(sourceBlob.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != content {
		t.Fatal("preserved Harness capture differs from exact input")
	}
	if rawReasoning != 2 || !unknownGap {
		t.Fatalf("reasoning or unknown-event boundary was lost: raw=%d gap=%v", rawReasoning, unknownGap)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
	second, err := ImportEventPath(store, source, EventOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.EventsAppended != 0 {
		t.Fatalf("unchanged capture was not idempotent: %+v", second)
	}
}

func TestImportHarnessCaptureReportsMalformedArtifactAndCaptureGap(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "events.jsonl")
	content := strings.Join([]string{
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:00Z","kind":"raw_artifact","session":{"id":"session-gap"},"revision":"rev-gap","artifact":{"meta":{"id":"session-gap"},"filename":"session.jsonl","content":"{broken\\n"}}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:01Z","kind":"gap","session":{"id":"session-gap"},"reason_code":"raw_artifacts_unsupported","reason":"backend has no per-session raw artifact"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportEventPath(store, source, EventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.GapsAppended != 2 || result.Kinds[string(ledger.KindGap)] != 2 {
		t.Fatalf("capture gaps were not explicit: %+v", result)
	}
}

func TestPackedPersistenceRowsExpandAndCanonicalIdentityDeduplicatesLiveEvents(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "packed.jsonl")
	live := `{"data":{"chunk":{"text":"reason-a","index":0,"type":"reasoning-delta"},"step":1,"turn":1},"time":2000,"seq":10,"type":"assistant/chunk"}`
	artifact := strings.Join([]string{
		`{"type":"session","version":0,"id":"packed-session"}`,
		`{"type":"reasoning-chunks","seq0":10,"time0":2000,"data":{"turn":1,"step":1,"index":0,"dt":[5],"texts":["reason-a","reason-b"]}}`,
		`{"type":"text-chunks","seq0":12,"time0":2010,"data":{"turn":1,"step":1,"index":1,"dt":[],"texts":["answer"]}}`,
		`{"type":"tool-call-chunks","seq0":13,"time0":2015,"data":{"turn":1,"step":1,"index":2,"id":"call-one","name":"read","dt":[],"args":["{}"]}}`,
	}, "\n") + "\n"
	content := strings.Join([]string{
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:00Z","kind":"session_event","session":{"id":"packed-session"},"event":` + live + `}`,
		`{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:01Z","kind":"raw_artifact","session":{"id":"packed-session"},"revision":"packed-revision","artifact":{"meta":{"id":"packed-session"},"filename":"session.jsonl","content":` + quoted(artifact) + `}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportEventPath(store, source, EventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsAppended != 4 || result.EventsSkipped != 1 || result.GapsAppended != 0 {
		t.Fatalf("packed rows did not expand or deduplicate correctly: %+v", result)
	}
	if result.Kinds[string(ledger.KindReasoning)] != 2 ||
		result.Kinds[string(ledger.KindSystemEvent)] != 2 {
		t.Fatalf("unexpected packed projections: %+v", result.Kinds)
	}
}

func TestMalformedPackedPersistenceRowBecomesOneExplicitGap(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "malformed-packed.jsonl")
	artifact := `{"type":"text-chunks","seq0":0,"time0":1,"data":{"turn":1,"step":1,"index":0,"dt":[],"texts":["a"]},"extra":true}` + "\n"
	content := `{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:00Z","kind":"raw_artifact","session":{"id":"packed-gap"},"revision":"rev","artifact":{"meta":{"id":"packed-gap"},"filename":"session.jsonl","content":` + quoted(artifact) + `}}` + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportEventPath(store, source, EventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.GapsAppended != 1 || result.EventsAppended != 1 {
		t.Fatalf("malformed packed row was not one explicit gap: %+v", result)
	}
}

func TestSessionTitleIsAKnownSystemEvent(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "session-title.jsonl")
	content := `{"schema_version":"deepseek-harness-capture/v1alpha1","captured_at":"2026-08-18T00:00:00Z","kind":"session_event","session":{"id":"title-session"},"event":{"type":"session/title","seq":10,"time":1787011200000,"data":{"title":"deterministic title","messageSeqs":[7],"source":{"kind":"fallback"}}}}` + "\n"
	if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ImportEventPath(store, source, EventOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.EventsAppended != 1 || result.GapsAppended != 0 ||
		result.Kinds[string(ledger.KindSystemEvent)] != 1 {
		t.Fatalf("session/title was not projected as a known system event: %+v", result)
	}
}

func quoted(value string) string {
	result := strings.Builder{}
	result.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\':
			result.WriteString(`\\`)
		case '"':
			result.WriteString(`\"`)
		case '\n':
			result.WriteString(`\n`)
		case '\r':
			result.WriteString(`\r`)
		case '\t':
			result.WriteString(`\t`)
		default:
			result.WriteRune(character)
		}
	}
	result.WriteByte('"')
	return result.String()
}
