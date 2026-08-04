package adapterjsonl

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestValidUnterminatedLineRemainsPartialUntilTerminated(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "events.jsonl")
	if err := os.WriteFile(source, []byte(`{"type":"synthetic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	spec := Spec{
		Agent: ledger.AgentOpenCode, AdapterName: "synthetic-jsonl",
		AdapterVersion: "synthetic-jsonl/v1", IDNamespace: "synthetic-jsonl",
		MatchFile: func(_ string, entry fs.DirEntry) bool {
			return !entry.IsDir()
		},
		DiscoverThreadID: func(_, _ string) (string, error) { return "synthetic", nil },
		Project: func(_ json.RawMessage) []Projection {
			return []Projection{{Kind: ledger.KindSystemEvent, ObservedAt: now}}
		},
	}

	result, err := ImportPath(store, source, Options{Now: func() time.Time { return now }}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 1 || result.GapsAppended != 1 ||
		result.Kinds[string(ledger.KindGap)] != 1 {
		t.Fatalf("unterminated JSON was accepted as complete: %+v", result)
	}

	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	result, err = ImportPath(store, source, Options{Now: func() time.Time { return now }}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSegments != 1 || result.EventsAppended != 1 || result.GapsAppended != 0 ||
		result.Kinds[string(ledger.KindSystemEvent)] != 1 {
		t.Fatalf("terminated JSON was not recovered: %+v", result)
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		t.Fatalf("verification issues: %v", report.Issues)
	}
}
