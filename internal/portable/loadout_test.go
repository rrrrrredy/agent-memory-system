package portable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestLoadoutCreationIsContentAddressedIdempotentAndCurrent(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	revision := testRootRevision(t, "Verify the focused test before reporting completion.")
	writeTestRevision(t, root, revision)
	options := LoadoutCreateOptions{
		Name:        "Repository completion checks",
		Description: "Reviewed completion constraints for coding tasks.",
		Agents:      []ledger.Agent{ledger.AgentOpenCode, ledger.AgentCodex},
		Scope:       review.Scope{Kind: review.ScopeProject, Value: "example-project"},
		MemoryIDs:   []string{revision.MemoryID},
		TokenBudget: 600,
		ByteBudget:  4096,
	}
	created, err := CreateLoadout(root, options)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Written || created.Loadout.LoadoutID == "" ||
		created.Loadout.Memories[0].RevisionID != revision.RevisionID ||
		len(created.Loadout.Agents) != 2 || created.Loadout.Agents[0] != ledger.AgentCodex {
		t.Fatalf("unexpected loadout creation result: %+v", created)
	}
	if !IsDataPath(created.RelativePath) {
		t.Fatalf("loadout is absent from the Git data allowlist: %s", created.RelativePath)
	}
	second, err := CreateLoadout(root, options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Written || second.Loadout.LoadoutID != created.Loadout.LoadoutID {
		t.Fatalf("idempotent creation changed the loadout: %+v", second)
	}
	loaded, report, err := LoadCurrentLoadout(root, created.Loadout.LoadoutID)
	if err != nil || report.LoadoutsChecked != 1 || len(report.Issues) != 0 ||
		loaded.LoadoutID != created.Loadout.LoadoutID {
		t.Fatalf("current loadout did not verify: loadout=%+v report=%+v err=%v", loaded, report, err)
	}
	listed, report := ListLoadouts(root)
	if len(report.Issues) != 0 || len(listed) != 1 || listed[0].LoadoutID != created.Loadout.LoadoutID {
		t.Fatalf("loadout listing changed identity: listed=%+v report=%+v", listed, report)
	}
}

func TestHistoricalLoadoutRemainsAuditableButCannotLoadAfterSupersession(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	initial := testRootRevision(t, "Keep task evidence local.")
	writeTestRevision(t, root, initial)
	created, err := CreateLoadout(root, LoadoutCreateOptions{
		Name:      "Privacy baseline",
		Agents:    []ledger.Agent{ledger.AgentClaudeCode},
		Scope:     review.Scope{Kind: review.ScopeGlobal, Value: "*"},
		MemoryIDs: []string{initial.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	superseded := testChildRevision(t, initial, "Keep raw task evidence local unless an encrypted backup is explicitly requested.")
	writeTestRevision(t, root, superseded)
	report := VerifyRepository(root)
	if len(report.Issues) != 0 || report.LoadoutsChecked != 1 || report.ActiveMemories != 1 {
		t.Fatalf("historical loadout made the repository unverifiable: %+v", report)
	}
	if _, _, err := LoadCurrentLoadout(root, created.Loadout.LoadoutID); err == nil ||
		!strings.Contains(err.Error(), "active memory heads") {
		t.Fatalf("stale loadout was accepted for delivery: %v", err)
	}
}

func TestLoadoutVerificationRejectsTamperingAndMissingRevision(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	revision := testRootRevision(t, "Use the project-scoped formatter.")
	writeTestRevision(t, root, revision)
	created, err := CreateLoadout(root, LoadoutCreateOptions{
		Name:      "Formatter",
		Agents:    []ledger.Agent{ledger.AgentCodex},
		Scope:     review.Scope{Kind: review.ScopeProject, Value: "formatter-project"},
		MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(created.RelativePath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := VerifyRepository(root); !hasIssue(report, "invalid_loadout") {
		t.Fatalf("non-canonical loadout tampering was accepted: %+v", report)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, revisionRelativePath(revision))); err != nil {
		t.Fatal(err)
	}
	if report := VerifyRepository(root); !hasIssue(report, "invalid_loadout_reference") {
		t.Fatalf("loadout with missing historical revision was accepted: %+v", report)
	}
}

func TestLoadoutParserRejectsASecondJSONValue(t *testing.T) {
	root := t.TempDir()
	if err := InitRepository(root); err != nil {
		t.Fatal(err)
	}
	revision := testRootRevision(t, "Keep generated reports local.")
	writeTestRevision(t, root, revision)
	created, err := CreateLoadout(root, LoadoutCreateOptions{
		Name:      "Local reports",
		Agents:    []ledger.Agent{ledger.AgentCodex},
		Scope:     review.Scope{Kind: review.ScopeProject, Value: "example-project"},
		MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(created.RelativePath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("{}\n")...)
	if _, err := parseLoadout(data); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("portable loadout accepted a second JSON value: %v", err)
	}
}
