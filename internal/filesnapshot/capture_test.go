package filesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestCaptureRejectsBytesThatDoNotMatchSupervisedInventory(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "artifact.txt")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := adapterjsonl.HashSourcePath(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = CapturePath(store, source, Options{
		Now:     func() time.Time { return time.Unix(1800000000, 0).UTC() },
		Context: context.Background(),
		ExpectedSources: map[string]adapterjsonl.ExpectedSource{
			identity: {ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Bytes: 7},
		},
	}, Spec{
		Agent: ledger.AgentClaudeCode, AdapterName: "test", AdapterVersion: "test/v1",
		IDNamespace: "test", Include: func(string, os.DirEntry) bool { return true },
		ThreadID: func(string, string) string { return "test-thread" },
		Kind:     func(string) ledger.EventKind { return ledger.KindSourceSnapshot },
	})
	if err == nil {
		t.Fatal("file snapshot accepted bytes that differed from the supervised pre-capture inventory")
	}
}
