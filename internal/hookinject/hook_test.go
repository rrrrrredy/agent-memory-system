package hookinject

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestUserPromptHookPreservesInputAndReturnsBoundedContext(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := hookPortableRepository(t, "Before editing, verify repository instructions and preserve unrelated changes.")
	raw := `{"session_id":"session-hook","transcript_path":"C:\\private\\transcript.jsonl","cwd":"D:\\work","hook_event_name":"UserPromptSubmit","turn_id":"turn-hook","prompt":"edit repository instructions safely","model":"current-model"}`
	output, err := Process(strings.NewReader(raw), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentCodex,
		TokenBudget:  500,
		ByteBudget:   2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Continue || output.HookSpecificOutput == nil ||
		output.HookSpecificOutput.HookEventName != "UserPromptSubmit" ||
		!strings.Contains(output.HookSpecificOutput.AdditionalContext, "verify repository instructions") {
		t.Fatalf("unexpected hook output: %+v", output)
	}
	inputPreserved := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindSystemEvent && record.Event.Payload != nil &&
			record.Event.Payload.Content != nil && *record.Event.Payload.Content == raw {
			inputPreserved = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !inputPreserved {
		t.Fatal("exact hook input was not preserved")
	}
	report := retrieval.Verify(store)
	if len(report.Issues) != 0 || report.RetrievalsChecked != 1 || report.InjectionsChecked != 1 {
		t.Fatalf("unexpected hook receipt graph: %+v", report)
	}
}

func TestUnsupportedHookEventIsCapturedWithoutInjection(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := t.TempDir()
	if err := portable.InitRepository(portableRoot); err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"session-hook","cwd":"D:\\work","hook_event_name":"SessionStart","source":"compact"}`
	output, err := Process(strings.NewReader(raw), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentClaudeCode,
	})
	if err != nil || !output.Continue || output.HookSpecificOutput != nil {
		t.Fatalf("non-prompt hook did not fail open: output=%+v err=%v", output, err)
	}
	verification := store.Verify()
	if len(verification.Issues) != 0 || verification.RecordsChecked != 1 {
		t.Fatalf("non-prompt hook was not captured exactly once: %+v", verification)
	}
}

func TestOpenCodeCompactionCanReuseTheLatestPrompt(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := hookPortableRepository(t, "Preserve verified constraints when a session is compacted.")
	raw := `{"session_id":"session-opencode","cwd":"D:\\work","hook_event_name":"SessionCompacting","turn_id":"message-opencode","prompt":"preserve constraints during compaction","source":"experimental.session.compacting"}`
	output, err := Process(strings.NewReader(raw), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentOpenCode,
		TokenBudget:  500,
		ByteBudget:   2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Continue || output.HookSpecificOutput == nil ||
		output.HookSpecificOutput.HookEventName != "SessionCompacting" ||
		!strings.Contains(output.HookSpecificOutput.AdditionalContext, "verified constraints") {
		t.Fatalf("unexpected OpenCode compaction output: %+v", output)
	}
	report := retrieval.Verify(store)
	if len(report.Issues) != 0 || report.RetrievalsChecked != 1 || report.InjectionsChecked != 1 {
		t.Fatalf("unexpected OpenCode receipt graph: %+v", report)
	}
}

func TestDeepSeekHarnessPromptUsesHarnessChannelAndVerifiedMemory(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := hookPortableRepository(t,
		"Use only verified promoted memory in the DeepSeek Harness pre-step.")
	raw := `{"session_id":"session-dsh","cwd":"D:\\work","hook_event_name":"UserPromptSubmit","turn_id":"turn-2-step-1","prompt":"retrieve verified harness memory","source":"dsh-agent-pre-step"}`
	output, err := Process(strings.NewReader(raw), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentDeepSeekHarness,
		TokenBudget:  500,
		ByteBudget:   2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Continue || output.HookSpecificOutput == nil ||
		!strings.Contains(output.HookSpecificOutput.AdditionalContext, "verified promoted memory") {
		t.Fatalf("unexpected DeepSeek Harness injection output: %+v", output)
	}
	seenHarnessChannel := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindRetrieval && record.Event.Payload != nil &&
			record.Event.Payload.Content != nil &&
			strings.Contains(*record.Event.Payload.Content, `"channel":"harness"`) {
			seenHarnessChannel = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seenHarnessChannel {
		t.Fatal("DeepSeek Harness retrieval was not recorded on the harness channel")
	}
	if report := retrieval.Verify(store); len(report.Issues) != 0 ||
		report.RetrievalsChecked != 1 || report.InjectionsChecked != 1 {
		t.Fatalf("unexpected DeepSeek Harness receipt graph: %+v", report)
	}
}

func TestOversizeHookInputFailsOpenAndStoresBoundedPrefixAsBlob(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := t.TempDir()
	if err := portable.InitRepository(portableRoot); err != nil {
		t.Fatal(err)
	}
	output, err := Process(strings.NewReader(strings.Repeat("x", MaximumInputBytes+1)), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentCodex,
	})
	if err == nil || !output.Continue || output.HookSpecificOutput != nil {
		t.Fatalf("oversize input did not fail open: output=%+v err=%v", output, err)
	}
	blobGap := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Kind == ledger.KindGap && record.Event.Payload != nil &&
			record.Event.Payload.Blob != nil && record.Event.Payload.Bytes > MaximumInputBytes {
			blobGap = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !blobGap {
		t.Fatal("oversize input gap was not preserved as a content-addressed blob")
	}
	if report := store.Verify(); len(report.Issues) != 0 || report.RecordsChecked != 1 || report.BlobsChecked != 1 {
		t.Fatalf("oversize hook evidence failed verification: %+v", report)
	}
}

func TestRejectedPortableRepositoryStillReturnsContinue(t *testing.T) {
	evidenceRoot := t.TempDir()
	store, err := ledger.Init(evidenceRoot)
	if err != nil {
		t.Fatal(err)
	}
	portableRoot := t.TempDir()
	if err := portable.InitRepository(portableRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(portableRoot, "unexpected.txt"), []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"session_id":"session-fail-open","hook_event_name":"UserPromptSubmit","prompt":"retrieve safely"}`
	output, err := Process(strings.NewReader(raw), Config{
		EvidenceRoot: evidenceRoot,
		PortableRoot: portableRoot,
		Agent:        ledger.AgentCodex,
	})
	if !errors.Is(err, retrieval.ErrPortableRepositoryRejected) || !output.Continue ||
		output.HookSpecificOutput != nil {
		t.Fatalf("rejected repository did not fail open: output=%+v err=%v", output, err)
	}
	if report := retrieval.Verify(store); len(report.Issues) != 0 || report.RetrievalsChecked != 1 {
		t.Fatalf("rejected hook receipt graph is invalid: %+v", report)
	}
}

func hookPortableRepository(t *testing.T, text string) string {
	t.Helper()
	root := t.TempDir()
	if err := portable.InitRepository(root); err != nil {
		t.Fatal(err)
	}
	textDigest := sha256.Sum256([]byte(text))
	textSHA256 := hex.EncodeToString(textDigest[:])
	memoryEnvelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA256, review.Scope{Kind: review.ScopeGlobal, Value: "*"}}
	memoryData, _ := json.Marshal(memoryEnvelope)
	memoryDigest := sha256.Sum256(memoryData)
	revision := portable.Revision{
		SchemaVersion:           portable.RevisionSchemaVersion,
		MemoryID:                "memory-" + hex.EncodeToString(memoryDigest[:]),
		Action:                  portable.ActionPromote,
		Status:                  portable.StatusActive,
		Kind:                    candidates.KindDirective,
		ScopeKind:               review.ScopeGlobal,
		ScopeValue:              "*",
		EvidenceBasis:           []review.Basis{review.BasisExplicitRemember},
		Text:                    text,
		TextSHA256:              textSHA256,
		RuleChangeAuthorization: "not_granted",
		Privacy:                 portable.PortablePrivacy,
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
	if report := portable.VerifyRepository(root); len(report.Issues) != 0 {
		t.Fatalf("portable fixture failed verification: %+v", report)
	}
	return root
}
