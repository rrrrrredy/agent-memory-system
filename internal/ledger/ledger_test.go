package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndVerifyInlineAndBlobEvents(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	firstID, err := NewEventID(now)
	if err != nil {
		t.Fatal(err)
	}
	inline := InlinePayload("utf-8", "text/plain", "preserve this exact instruction")
	first := Event{
		SchemaVersion: SchemaVersion,
		EventID:       firstID,
		Kind:          KindUserMessage,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: Source{
			Agent: AgentCodex, Adapter: "codex-jsonl", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Payload:      &inline,
		Completeness: Completeness{Status: CompletenessComplete},
		Privacy:      Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(first); err != nil {
		t.Fatal(err)
	}

	blob, err := store.PutBlob(strings.NewReader("opaque encrypted reasoning bytes"))
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := NewEventID(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second := Event{
		SchemaVersion: SchemaVersion,
		EventID:       secondID,
		Kind:          KindReasoning,
		ObservedAt:    now.Add(time.Second),
		RecordedAt:    now.Add(time.Second),
		Source: Source{
			Agent: AgentCodex, Adapter: "codex-jsonl", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Payload: &Payload{
			Encoding: "encrypted", MediaType: "application/octet-stream",
			Blob: &blob, SHA256: blob.SHA256, Bytes: blob.Bytes,
		},
		Reasoning:    &ReasoningCapture{Visibility: ReasoningEncryptedOpaque},
		Completeness: Completeness{Status: CompletenessComplete},
		Privacy:      Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(second); err != nil {
		t.Fatal(err)
	}

	report := store.Verify()
	if len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
	if report.RecordsChecked != 2 || report.BlobsChecked != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestMissingReasoningIsExplicit(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	eventID, err := NewEventID(now)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		Kind:          KindReasoning,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: Source{
			Agent: AgentClaudeCode, Adapter: "test", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Reasoning: &ReasoningCapture{Visibility: ReasoningNotExposed},
		Completeness: Completeness{
			Status: CompletenessMissing,
			Reason: "provider did not expose private reasoning",
		},
		Privacy: Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(event); err != nil {
		t.Fatal(err)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}

func TestTamperingBreaksHashChain(t *testing.T) {
	root := t.TempDir()
	store, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	eventID, err := NewEventID(now)
	if err != nil {
		t.Fatal(err)
	}
	payload := InlinePayload("utf-8", "text/plain", "original")
	event := Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID, Kind: KindAgentMessage,
		ObservedAt: now, RecordedAt: now,
		Source: Source{
			Agent: AgentOpenCode, Adapter: "test", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Payload:      &payload,
		Completeness: Completeness{Status: CompletenessComplete},
		Privacy:      Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(event); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "evidence", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "original", "tampered", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if report := store.Verify(); len(report.Issues) == 0 {
		t.Fatal("tampering was not detected")
	}
	if _, err := store.Append(event); err == nil {
		t.Fatal("append continued after existing ledger tampering")
	}
}

func TestBlobPathTraversalIsRejected(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	eventID, err := NewEventID(now)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := store.PutBlob(strings.NewReader("safe bytes"))
	if err != nil {
		t.Fatal(err)
	}
	blob.RelativePath = "../../outside"
	event := Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		Kind:          KindToolResult,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: Source{
			Agent: AgentCodex, Adapter: "test", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Payload: &Payload{
			Encoding: "binary", Blob: &blob, SHA256: blob.SHA256, Bytes: blob.Bytes,
		},
		Completeness: Completeness{Status: CompletenessComplete},
		Privacy:      Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(event); err == nil {
		t.Fatal("unsafe blob path was accepted")
	}
}

func TestMissingBlobIsRejectedBeforeAppend(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	eventID, err := NewEventID(now)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	blob := BlobRef{
		SHA256: digest,
		Bytes:  12,
		RelativePath: filepath.ToSlash(filepath.Join(
			"evidence", "blobs", "sha256", digest[:2], digest[2:],
		)),
	}
	event := Event{
		SchemaVersion: SchemaVersion,
		EventID:       eventID,
		Kind:          KindToolResult,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: Source{
			Agent: AgentOpenCode, Adapter: "test", AdapterVersion: "test",
			DeviceID: "device-a", ThreadID: "thread-a",
		},
		Payload: &Payload{
			Encoding: "binary", Blob: &blob, SHA256: blob.SHA256, Bytes: blob.Bytes,
		},
		Completeness: Completeness{Status: CompletenessComplete},
		Privacy:      Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(event); err == nil {
		t.Fatal("missing blob was accepted")
	}
	if report := store.Verify(); report.RecordsChecked != 0 {
		t.Fatalf("failed append changed the ledger: %+v", report)
	}
}
