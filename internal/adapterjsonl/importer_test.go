package adapterjsonl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestSupervisedImportBindsParsedBytesToPreCaptureInventory(t *testing.T) {
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"type\":\"synthetic\"}\n")
	source := filepath.Join(root, "events.jsonl")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := HashSourcePath(source)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	spec := Spec{
		Agent: ledger.AgentOpenCode, AdapterName: "synthetic-supervised-jsonl",
		AdapterVersion: "synthetic-supervised-jsonl/v1", IDNamespace: "synthetic-supervised-jsonl",
		MatchFile:        func(_ string, entry fs.DirEntry) bool { return !entry.IsDir() },
		DiscoverThreadID: func(_, _ string) (string, error) { return "synthetic", nil },
		Project: func(_ json.RawMessage) []Projection {
			return []Projection{{Kind: ledger.KindSystemEvent, ObservedAt: now}}
		},
	}
	wrong := sha256.Sum256([]byte("different"))
	_, err = ImportPath(store, source, Options{
		Now: func() time.Time { return now }, Context: context.Background(),
		ExpectedSources: map[string]ExpectedSource{
			identity: {ContentSHA256: hex.EncodeToString(wrong[:]), Bytes: int64(len(content))},
		},
	}, spec)
	if err == nil {
		t.Fatal("supervised import accepted bytes that differed from the pre-capture inventory")
	}
	digest := sha256.Sum256(content)
	result, err := ImportPath(store, source, Options{
		Now: func() time.Time { return now }, Context: context.Background(),
		ExpectedSources: map[string]ExpectedSource{
			identity: {ContentSHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content))},
		},
	}, spec)
	if err != nil || result.EventsAppended != 1 {
		t.Fatalf("matching supervised bytes were not imported: result=%+v err=%v", result, err)
	}
}
