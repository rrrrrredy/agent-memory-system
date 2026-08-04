package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestImportHomeCapturesDocumentedProcessDataAndExcludesConfiguration(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, ".claude")
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	project := filepath.Join(home, "projects", "-synthetic-project")
	mustWrite(t, filepath.Join(project, sessionID+".jsonl"),
		`{"type":"user","uuid":"u-1","sessionId":"`+sessionID+`","timestamp":"2026-08-04T01:00:00Z","message":{"role":"user","content":"synthetic prompt"}}`+"\n")
	toolResult := "synthetic large tool result"
	mustWrite(t, filepath.Join(project, sessionID, "tool-results", "tool-1.jsonl"), toolResult)
	mustWrite(t, filepath.Join(home, "file-history", sessionID, "snapshot.txt"), "synthetic pre-edit snapshot")
	mustWrite(t, filepath.Join(home, "plans", "synthetic-plan.md"), "# Synthetic plan")
	mustWrite(t, filepath.Join(home, "image-cache", "synthetic-image.bin"), "synthetic image bytes")
	mustWrite(t, filepath.Join(home, "stats-cache.json"), `{"tokens":1}`)
	mustWrite(t, filepath.Join(home, "history.jsonl"),
		`{"display":"synthetic typed prompt","timestamp":1785805200000,"project":"/synthetic"}`+"\n")
	mustWrite(t, filepath.Join(home, "settings.json"), `{"excluded":"configuration"}`)
	mustWrite(t, filepath.Join(home, "plugins", "synthetic-secret.txt"), "excluded plugin state")
	mustWrite(t, filepath.Join(home, "backups", "claude.json"), "excluded config backup")

	now := time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)
	result, err := ImportHome(store, home, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Transcripts.SourceSegments != 1 || result.Transcripts.EventsAppended != 1 {
		t.Fatalf("unexpected transcript result: %+v", result.Transcripts)
	}
	if result.PromptHistory.SourceSegments != 1 || result.PromptHistory.EventsAppended != 1 {
		t.Fatalf("unexpected prompt-history result: %+v", result.PromptHistory)
	}
	if result.Companion.SnapshotsAppended != 5 || result.Companion.GapsAppended != 0 {
		t.Fatalf("unexpected companion result: %+v", result.Companion)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}

	kinds := map[ledger.EventKind]int{}
	var toolBlob *ledger.BlobRef
	if err := store.VisitRecords(func(record ledger.Record) error {
		kinds[record.Event.Kind]++
		cursor := record.Event.Source.SourceCursor
		if strings.Contains(cursor, "settings.json") || strings.Contains(cursor, "plugins/") ||
			strings.Contains(cursor, "backups/") {
			t.Fatalf("configuration escaped process-data allowlist: %s", cursor)
		}
		if record.Event.Source.Adapter == CompanionAdapterName &&
			record.Event.Kind == ledger.KindToolResult {
			toolBlob = record.Event.Payload.Blob
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if kinds[ledger.KindUserMessage] != 2 || kinds[ledger.KindToolResult] != 1 ||
		kinds[ledger.KindFileChange] != 1 || kinds[ledger.KindAttachment] != 1 {
		t.Fatalf("unexpected event kinds: %+v", kinds)
	}
	if toolBlob == nil {
		t.Fatal("tool-result companion blob was not recorded")
	}
	preserved, err := os.ReadFile(filepath.Join(store.Root(), filepath.FromSlash(toolBlob.RelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != toolResult {
		t.Fatalf("tool-result bytes changed: %q", preserved)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}

	second, err := ImportHome(store, home, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if second.Transcripts.EventsAppended != 0 || second.PromptHistory.EventsAppended != 0 ||
		second.Companion.SnapshotsAppended != 0 || second.Companion.GapsAppended != 0 {
		t.Fatalf("idempotent home import changed ledger: %+v", second)
	}

	mustWrite(t, filepath.Join(project, sessionID, "tool-results", "tool-1.jsonl"),
		"synthetic changed tool result")
	changed, err := ImportHome(store, home, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Companion.SnapshotsAppended != 1 {
		t.Fatalf("changed companion was not versioned: %+v", changed.Companion)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
