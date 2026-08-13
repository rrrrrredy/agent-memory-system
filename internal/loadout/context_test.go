package loadout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestBuildAndReplayExactLoadoutContext(t *testing.T) {
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	first := writePortableRevision(t, repository, "Run the focused test before reporting completion.")
	second := writePortableRevision(t, repository, "Preserve unrelated working-tree changes.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name:        "Completion safeguards",
		Description: "An explicit ordered set for repository changes.",
		Agents:      []ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode},
		Scope:       review.Scope{Kind: review.ScopeProject, Value: "example-project"},
		MemoryIDs:   []string{first.MemoryID, second.MemoryID},
		TokenBudget: 800,
		ByteBudget:  4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	context := retrieval.Context{
		Agent: ledger.AgentCodex, ThreadID: "loadout-thread", SessionID: "loadout-session",
		Project: "example-project", Channel: retrieval.ChannelHarness,
	}
	result, err := BuildContext(store, repository, created.Loadout.LoadoutID, context)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.ReceiptID == "" || len(result.Receipt.Memories) != 2 ||
		len(result.Receipt.RetrievalReceiptIDs) != 2 ||
		!strings.Contains(result.Receipt.Content, first.MemoryID) ||
		!strings.Contains(result.Receipt.Content, "Current user instructions") {
		t.Fatalf("unexpected loadout context: %+v", result)
	}
	if report := retrieval.Verify(store); len(report.Issues) != 0 || report.RetrievalsChecked != 2 {
		t.Fatalf("underlying retrieval receipts failed verification: %+v", report)
	}
	report := Verify(store)
	if len(report.Issues) != 0 || report.ReceiptsChecked != 1 {
		t.Fatalf("loadout context failed replay: %+v", report)
	}
	resolved, err := ResolveVerifiedContext(store, result.Receipt.ReceiptID)
	if err != nil || resolved.Content != result.Receipt.Content {
		t.Fatalf("exact verified context was unavailable: receipt=%+v err=%v", resolved, err)
	}
}

func TestBuildLoadoutContextRejectsWrongAgentAndScopeBeforeRetrieval(t *testing.T) {
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	revision := writePortableRevision(t, repository, "Use the project formatter.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name:      "Project formatter",
		Agents:    []ledger.Agent{ledger.AgentCodex},
		Scope:     review.Scope{Kind: review.ScopeProject, Value: "alpha"},
		MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildContext(store, repository, created.Loadout.LoadoutID, retrieval.Context{
		Agent: ledger.AgentClaudeCode, Project: "alpha", Channel: retrieval.ChannelCLI,
	}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("wrong agent was accepted: %v", err)
	}
	if _, err := BuildContext(store, repository, created.Loadout.LoadoutID, retrieval.Context{
		Agent: ledger.AgentCodex, Project: "beta", Channel: retrieval.ChannelCLI,
	}); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("wrong project scope was accepted: %v", err)
	}
	if report := retrieval.Verify(store); report.RetrievalsChecked != 0 {
		t.Fatalf("rejected loadout emitted retrieval evidence: %+v", report)
	}
}

func TestLoadoutReplayRejectsTamperedCompositeReceipt(t *testing.T) {
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	revision := writePortableRevision(t, repository, "Keep raw evidence local.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name: "Privacy", Agents: []ledger.Agent{ledger.AgentCodex},
		Scope: review.Scope{Kind: review.ScopeGlobal, Value: "*"}, MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := BuildContext(store, repository, created.Loadout.LoadoutID, retrieval.Context{
		Agent: ledger.AgentCodex, Channel: retrieval.ChannelCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := result.Receipt
	tampered.ReceiptID = "loadout-context-tampered"
	tampered.RecordedAt = time.Now().UTC()
	tampered.Content += "unbound text"
	data, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	payload := ledger.InlinePayload("json", "application/json", string(data))
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: tampered.ReceiptID,
		Kind: ledger.KindSystemEvent, ObservedAt: tampered.RecordedAt, RecordedAt: tampered.RecordedAt,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "agentmem-loadout", AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), ThreadID: "standalone-loadout"},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: tampered.RetrievalReceiptIDs},
		Privacy:   ledger.Privacy{Classification: PrivacyLocalOnly},
	}); err != nil {
		t.Fatal(err)
	}
	if report := Verify(store); len(report.Issues) == 0 || report.ReceiptsChecked != 1 {
		t.Fatalf("tampered composite receipt was accepted: %+v", report)
	}
}

func writePortableRevision(t *testing.T, root, text string) portable.Revision {
	t.Helper()
	textDigest := sha256.Sum256([]byte(text))
	textSHA := hex.EncodeToString(textDigest[:])
	memoryEnvelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA, review.Scope{Kind: review.ScopeGlobal, Value: "*"}}
	memoryData, _ := json.Marshal(memoryEnvelope)
	memoryDigest := sha256.Sum256(memoryData)
	revision := portable.Revision{
		SchemaVersion: portable.RevisionSchemaVersion,
		MemoryID:      "memory-" + hex.EncodeToString(memoryDigest[:]),
		Action:        portable.ActionPromote, Status: portable.StatusActive,
		Kind: candidates.KindDirective, ScopeKind: review.ScopeGlobal, ScopeValue: "*",
		EvidenceBasis: []review.Basis{review.BasisExplicitRemember}, Text: text, TextSHA256: textSHA,
		RuleChangeAuthorization: "not_granted", Privacy: portable.PortablePrivacy,
	}
	identity := revision
	identity.Text = ""
	identity.RevisionID = ""
	revisionData, _ := json.Marshal(identity)
	revisionDigest := sha256.Sum256(revisionData)
	revision.RevisionID = "portable-revision-" + hex.EncodeToString(revisionDigest[:])
	data, err := portable.RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash := strings.TrimPrefix(revision.MemoryID, "memory-")
	revisionHash := strings.TrimPrefix(revision.RevisionID, "portable-revision-")
	path := filepath.Join(root, "memories", memoryHash[:2], memoryHash, revisionHash+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return revision
}
