package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const rememberedText = "Please remember that generated reports must stay local"

func TestPromotedMemoryTravelsFromRawAgentEvidenceToThreeAgentRetrieval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Memory Acceptance")
	t.Setenv("GIT_AUTHOR_EMAIL", "memory-acceptance@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Memory Acceptance")
	t.Setenv("GIT_COMMITTER_EMAIL", "memory-acceptance@example.invalid")

	base := shortTempDir(t)
	sourceStore, err := ledger.Init(filepath.Join(base, "device-a-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	importThreeAgentSources(t, sourceStore, filepath.Join(base, "agent-sources"))
	if report := sourceStore.Verify(); len(report.Issues) != 0 {
		t.Fatalf("raw evidence did not verify: %+v", report)
	}

	episodeResult, err := episodes.Build(sourceStore, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if episodeResult.Episodes != 3 {
		t.Fatalf("three Agent sources did not produce three episodes: %+v", episodeResult)
	}
	candidateResult, err := candidates.Build(sourceStore, candidates.BuildOptions{
		EpisodeGenerationPath: episodeResult.GenerationPath,
		ShardCount:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := findCandidate(t, candidateResult.GenerationPath, rememberedText)
	if candidate.Validation.Status != candidates.StatusReviewReady ||
		!containsSupport(candidate.SupportTypes, candidates.SupportExplicitRemember) {
		t.Fatalf("explicit memory was not review-ready: %+v", candidate)
	}

	generation := filepath.Base(candidateResult.GenerationPath)
	reviewed, err := review.Apply(sourceStore, generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
			Scope:  &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis:  []review.Basis{review.BasisExplicitRemember},
			Reason: "The instruction is explicit and safe for cross-Agent use.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	textDigest := sha256.Sum256([]byte(rememberedText))
	promoted, err := promotion.Apply(sourceStore, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation, CandidateID: candidate.CandidateID,
			CandidateContentSHA256:  candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewed.RecordSHA256,
		},
		ExpectedTextSHA256: hex.EncodeToString(textDigest[:]),
		Reason:             "The reviewed memory is eligible for portable retrieval.",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := initBareRemote(t, filepath.Join(base, "remote-memory.git"))
	deviceARepository := filepath.Join(base, "device-a-memory")
	bootstrap, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: deviceARepository, RemoteURL: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Head == "" || !bootstrap.RemoteConfigured {
		t.Fatalf("portable repository did not bootstrap: %+v", bootstrap)
	}
	if initial, err := gitsync.Run(context.Background(), gitsync.SyncOptions{
		RepositoryRoot: deviceARepository,
	}); err != nil || initial.Outcome != "remote_initialized" {
		t.Fatalf("portable root was not published: %+v, %v", initial, err)
	}
	exported, err := portable.Export(sourceStore, deviceARepository, portable.ExportOptions{
		MemoryIDs: []string{promoted.Revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exported.RevisionsWritten != 1 {
		t.Fatalf("promoted memory was not projected: %+v", exported)
	}
	published, err := gitsync.Run(context.Background(), gitsync.SyncOptions{
		RepositoryRoot: deviceARepository,
	})
	if err != nil || published.Outcome != "pushed" || !published.Pushed {
		t.Fatalf("portable memory was not synchronized: %+v, %v", published, err)
	}

	deviceBRepository := filepath.Join(base, "device-b-memory")
	cloneRepository(t, remote, deviceBRepository)
	if report := gitsync.Verify(context.Background(), deviceBRepository); len(report.Issues) != 0 ||
		report.Portable.ActiveMemories != 1 {
		t.Fatalf("second device did not verify the synchronized memory: %+v", report)
	}
	assertNoRawEvidenceInPortableRepository(t, deviceBRepository)

	deviceBStore, err := ledger.Init(filepath.Join(base, "device-b-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	channels := map[ledger.Agent]retrieval.DeliveryChannel{
		ledger.AgentCodex:      retrieval.ChannelCodexHook,
		ledger.AgentClaudeCode: retrieval.ChannelClaudeHook,
		ledger.AgentOpenCode:   retrieval.ChannelOpenCodePlugin,
	}
	for _, agent := range []ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode} {
		contextValue := retrieval.Context{
			Agent: agent, ThreadID: "acceptance-" + string(agent), Channel: channels[agent],
		}
		delivered, err := retrieval.BuildContext(deviceBStore, deviceBRepository, retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local",
			Context: contextValue, Limit: 3, TokenBudget: 512, ByteBudget: 2048,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(delivered.Memories) != 1 || delivered.InjectionID == "" ||
			!strings.Contains(delivered.Content, rememberedText) {
			t.Fatalf("%s did not receive the synchronized memory: %+v", agent, delivered)
		}
		outcomeEventID := "acceptance-outcome-" + string(agent)
		payload := ledger.InlinePayload("utf-8", "text/plain", "completed without exporting raw evidence")
		if _, err := deviceBStore.Append(ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: outcomeEventID, Kind: ledger.KindToolResult,
			ObservedAt: time.Now().UTC(), RecordedAt: time.Now().UTC(),
			Source: ledger.Source{
				Agent: agent, Adapter: "acceptance-harness", AdapterVersion: "acceptance-harness/v1",
				DeviceID: deviceBStore.DeviceID(), ThreadID: contextValue.ThreadID,
			},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Causality: &ledger.Causality{ParentEventIDs: []string{delivered.InjectionID}},
			Privacy:   ledger.Privacy{Classification: "local_only"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := retrieval.RecordAdoption(deviceBStore, contextValue, retrieval.AdoptionRequest{
			SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
			Reporter:           retrieval.Reporter{Kind: "harness", ID: "cross-agent-acceptance"},
			RetrievalReceiptID: delivered.Retrieval.ReceiptID,
			InjectionID:        delivered.InjectionID,
			Items: []retrieval.AdoptionItem{{
				MemoryReference: delivered.Memories[0], Adoption: retrieval.AdoptionAdopted,
				Outcome: retrieval.OutcomeHelpful, Reason: "The delivered constraint was followed.",
			}},
			OutcomeEvidenceEventIDs: []string{outcomeEventID},
		}); err != nil {
			t.Fatal(err)
		}
	}

	receipts := retrieval.Verify(deviceBStore)
	if len(receipts.Issues) != 0 || len(receipts.OpenRetrievalIDs) != 0 ||
		receipts.RetrievalsChecked != 3 || receipts.InjectionsChecked != 3 || receipts.AdoptionsChecked != 3 {
		t.Fatalf("cross-Agent delivery receipts did not close: %+v", receipts)
	}
	if report := deviceBStore.Verify(); len(report.Issues) != 0 {
		t.Fatalf("second-device evidence did not verify: %+v", report)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("", "agent-memory-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove temporary acceptance directory: %v", err)
		}
	})
	return path
}

func importThreeAgentSources(t *testing.T, store *ledger.Store, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	codexSource := filepath.Join(root, "rollout-2026-08-05T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	codexData := `{"timestamp":"2026-08-05T00:00:00Z","type":"session_meta","payload":{"id":"11111111-1111-1111-1111-111111111111"}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"` + rememberedText + `"}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex-private-model-response"}]}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-private","output":"codex-private-tool-output"}}` + "\n"
	writeFile(t, codexSource, []byte(codexData))
	if result, err := codex.ImportPath(store, codexSource, codex.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("Codex source did not import completely: %+v, %v", result, err)
	}

	claudeSource := filepath.Join(root, "22222222-2222-2222-2222-222222222222.jsonl")
	claudeData := `{"type":"user","uuid":"claude-user","sessionId":"22222222-2222-2222-2222-222222222222","timestamp":"2026-08-05T00:01:00Z","message":{"role":"user","content":"claude-private-user-instruction"}}` + "\n" +
		`{"type":"assistant","uuid":"claude-assistant","sessionId":"22222222-2222-2222-2222-222222222222","timestamp":"2026-08-05T00:01:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"claude-private-reasoning"},{"type":"text","text":"claude-private-model-response"}]}}` + "\n"
	writeFile(t, claudeSource, []byte(claudeData))
	if result, err := claudecode.ImportPath(store, claudeSource, claudecode.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("Claude Code source did not import completely: %+v, %v", result, err)
	}

	openCodeSource := filepath.Join(root, "ses_acceptance.json")
	openCodeData := `{
  "info": {"id":"ses_acceptance","projectID":"project_acceptance","directory":"/private/source/path","title":"Acceptance","version":"1","time":{"created":1785888120000,"updated":1785888122000}},
  "messages": [
    {"info":{"id":"oc-user","sessionID":"ses_acceptance","role":"user","time":{"created":1785888120000}},"parts":[{"id":"oc-user-text","sessionID":"ses_acceptance","messageID":"oc-user","type":"text","text":"opencode-private-user-instruction"}]},
    {"info":{"id":"oc-assistant","sessionID":"ses_acceptance","role":"assistant","time":{"created":1785888121000,"completed":1785888122000}},"parts":[{"id":"oc-reasoning","sessionID":"ses_acceptance","messageID":"oc-assistant","type":"reasoning","text":"opencode-private-reasoning"},{"id":"oc-text","sessionID":"ses_acceptance","messageID":"oc-assistant","type":"text","text":"opencode-private-model-response"}]}
  ]
}` + "\n"
	writeFile(t, openCodeSource, []byte(openCodeData))
	if result, err := opencode.ImportPath(store, openCodeSource, opencode.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("OpenCode source did not import completely: %+v, %v", result, err)
	}
}

func findCandidate(t *testing.T, generationPath, text string) candidates.Candidate {
	t.Helper()
	file, err := os.Open(filepath.Join(generationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var candidate candidates.Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if candidate.Text == text {
			return candidate
		}
	}
	t.Fatalf("candidate %q was not derived", text)
	return candidates.Candidate{}
}

func containsSupport(values []candidates.SupportType, wanted candidates.SupportType) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func initBareRemote(t *testing.T, path string) string {
	t.Helper()
	command := exec.Command("git", "init", "--bare", "--initial-branch", gitsync.DefaultBranch, path)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize bare remote: %v: %s", err, output)
	}
	return path
}

func cloneRepository(t *testing.T, remote, target string) {
	t.Helper()
	command := exec.Command("git", "clone", "--branch", gitsync.DefaultBranch, "--", remote, target)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone portable repository: %v: %s", err, output)
	}
}

func assertNoRawEvidenceInPortableRepository(t *testing.T, root string) {
	t.Helper()
	forbidden := []string{
		"codex-private-model-response", "codex-private-tool-output",
		"claude-private-user-instruction", "claude-private-model-response",
		"claude-private-reasoning", "opencode-private-user-instruction",
		"opencode-private-model-response", "opencode-private-reasoning",
		"/private/source/path",
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".agentmem") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if strings.Contains(string(data), value) {
				t.Fatalf("portable repository contains raw-only marker %q", value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
