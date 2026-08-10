package ledger

import (
	"errors"
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
	if store.DeviceID() == "" {
		t.Fatal("store has no stable device id")
	}
	visited := 0
	if err := store.VisitRecords(func(record Record) error {
		visited++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if visited != 2 {
		t.Fatalf("visited %d records, want 2", visited)
	}
}

func TestLedgerAcceptsLegacyPrefixAndCurrentTailButRejectsNewKindsInV1Alpha1(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0).UTC()
	makeEvent := func(version string, kind EventKind, id string, offset time.Duration) Event {
		payload := InlinePayload("utf-8", "application/json", `{"evidence":"bound"}`)
		return Event{SchemaVersion: version, EventID: id, Kind: kind,
			ObservedAt: now.Add(offset), RecordedAt: now.Add(offset),
			Source: Source{Agent: AgentCodex, Adapter: "mixed-version-test", AdapterVersion: "v1",
				DeviceID: store.DeviceID(), ThreadID: "mixed-version-thread"},
			Payload: &payload, Completeness: Completeness{Status: CompletenessComplete},
			Privacy: Privacy{Classification: "local_only"}}
	}
	if _, err := store.Append(makeEvent(SchemaVersionV1Alpha1, KindUserMessage,
		"legacy-prefix-event", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(makeEvent(SchemaVersionV1Alpha2, KindTaskAttemptContract,
		"current-tail-event", time.Second)); err != nil {
		t.Fatal(err)
	}
	if report := store.Verify(); len(report.Issues) != 0 || report.RecordsChecked != 2 {
		t.Fatalf("mixed-version ledger did not verify: %+v", report)
	}
	if _, err := store.Append(makeEvent(SchemaVersionV1Alpha1, KindTaskAttemptContract,
		"invalid-legacy-new-kind", 2*time.Second)); err == nil {
		t.Fatal("v1alpha1 event accepted a v1alpha2-only kind")
	}
	if _, err := store.Append(makeEvent(SchemaVersionV1Alpha2, EventKind("invented_kind"),
		"invalid-current-kind", 3*time.Second)); err == nil {
		t.Fatal("v1alpha2 event accepted an unknown kind")
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

func TestAppendBatchBuildsOneVerifiedChain(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	events := make([]Event, 0, 100)
	for index := 0; index < 100; index++ {
		eventID, err := NewEventID(now.Add(time.Duration(index) * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		payload := InlinePayload("utf-8", "text/plain", "synthetic batch record")
		events = append(events, Event{
			SchemaVersion: SchemaVersion,
			EventID:       eventID,
			Kind:          KindSystemEvent,
			ObservedAt:    now,
			RecordedAt:    now,
			Source: Source{
				Agent: AgentCodex, Adapter: "test", AdapterVersion: "test",
				DeviceID: "device-a", ThreadID: "thread-a",
			},
			Payload:      &payload,
			Completeness: Completeness{Status: CompletenessComplete},
			Privacy:      Privacy{Classification: "local_only"},
		})
	}
	records, err := store.AppendBatch(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(events) {
		t.Fatalf("appended %d records, want %d", len(records), len(events))
	}
	report := store.Verify()
	if len(report.Issues) != 0 || report.RecordsChecked != len(events) {
		t.Fatalf("unexpected verification report: %+v", report)
	}

	visited := 0
	appender, err := store.NewAppenderAfterVisit(func(record Record) error {
		visited++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != len(events) {
		t.Fatalf("visited %d records, want %d", visited, len(events))
	}
	extra := events[0]
	extra.EventID, err = NewEventID(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appender.AppendBatch([]Event{extra}); err != nil {
		_ = appender.Close()
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	report = store.Verify()
	if len(report.Issues) != 0 || report.RecordsChecked != len(events)+1 {
		t.Fatalf("unexpected post-append report: %+v", report)
	}
}

func TestWriterLockBlocksConcurrentAppendAndSupportsExplicitRecovery(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appender, err := store.NewAppender()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAppender(); err == nil || !errors.Is(err, ErrWriterLocked) {
		_ = appender.Close()
		t.Fatalf("concurrent writer was not rejected: %v", err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), filepath.FromSlash(writerLockPath))
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("writer lock remained after close: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewAppender(); err == nil {
		t.Fatal("stale writer lock was silently removed")
	}
	cleared, err := store.ClearStaleWriterLock()
	if err != nil || !cleared {
		t.Fatalf("explicit stale-lock recovery failed: cleared=%v err=%v", cleared, err)
	}
	appender, err = store.NewAppender()
	if err != nil {
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFailedAppendReleasesWriterLock(t *testing.T) {
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(Event{}); err == nil {
		t.Fatal("invalid event was accepted")
	}
	appender, err := store.NewAppender()
	if err != nil {
		t.Fatalf("failed append left writer lock behind: %v", err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceRootInsideGitWorktreeIsRejected(t *testing.T) {
	worktree := t.TempDir()
	if err := os.Mkdir(filepath.Join(worktree, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(worktree, "private-evidence")
	if _, err := Init(root); !errors.Is(err, ErrEvidenceRootInGitWorktree) {
		t.Fatalf("Git-contained evidence root was not rejected: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected evidence root was created: %v", err)
	}
}

func TestExistingEvidenceStoreStopsWritingAfterGitInitialization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-evidence")
	store, err := Init(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); !errors.Is(err, ErrEvidenceRootInGitWorktree) {
		t.Fatalf("existing Git-contained store was reopened: %v", err)
	}
	if _, err := store.NewAppender(); !errors.Is(err, ErrEvidenceRootInGitWorktree) {
		t.Fatalf("existing store opened a writer after Git initialization: %v", err)
	}
	if _, err := store.PutBlob(strings.NewReader("raw evidence")); !errors.Is(err, ErrEvidenceRootInGitWorktree) {
		t.Fatalf("existing store wrote a blob after Git initialization: %v", err)
	}
}
