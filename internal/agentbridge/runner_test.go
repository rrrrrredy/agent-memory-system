package agentbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENTBRIDGE_TEST_HELPER") == "1" {
		runAgentBridgeTestHelper()
		return
	}
	os.Exit(m.Run())
}

func runAgentBridgeTestHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-test 1.0")
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "exec" || os.Args[len(os.Args)-1] != "-" ||
		!containsArgument(os.Args, "--json") || !containsArgument(os.Args, "--ephemeral") ||
		!containsArgument(os.Args, "--ignore-user-config") || !containsArgument(os.Args, "--ignore-rules") {
		fmt.Fprintln(os.Stderr, "unexpected Codex invocation")
		os.Exit(4)
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil || len(strings.TrimSpace(string(prompt))) == 0 {
		fmt.Fprintln(os.Stderr, "missing stdin prompt")
		os.Exit(5)
	}
	fmt.Println(`{"type":"thread.started","thread_id":"native-thread"}`)
	if ready := os.Getenv("AGENTBRIDGE_BLOCK_READY"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			os.Exit(6)
		}
		release := os.Getenv("AGENTBRIDGE_BLOCK_RELEASE")
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(7)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if strings.Contains(string(prompt), "force process failure") {
		fmt.Println(`{"type":`)
		fmt.Fprintln(os.Stderr, "forced failure")
		os.Exit(2)
	}
	fmt.Println(`{"type":"turn.started","future_field":"accepted"}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning","text":"brief exposed summary","future_item_field":true}}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"verified answer"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":2,"cache_write_input_tokens":0,"output_tokens":4,"reasoning_output_tokens":1}}`)
}

func containsArgument(arguments []string, target string) bool {
	for _, argument := range arguments {
		if argument == target {
			return true
		}
	}
	return false
}

func TestRunPersistsAndReplaysExactNativeCodexReceipt(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	result, err := Run(context.Background(), store, testRunRequest(workspace, "Return a verified answer."),
		Options{CodexPath: executable, Now: fixedAgentBridgeClock()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Outcome != OutcomeCompleted || result.Receipt.ProcessExitCode != 0 ||
		result.Receipt.ThreadID != "native-thread" || result.Receipt.Events != 5 ||
		result.Receipt.ToolCalls != 0 || result.Receipt.AgentMessageBlob == nil ||
		result.Receipt.ReasoningVisibility != ledger.ReasoningSummaryOnly ||
		result.Receipt.ProviderAuthority != ProviderAuthority {
		t.Fatalf("native receipt is incomplete: %+v", result.Receipt)
	}
	report := Verify(store)
	if len(report.Issues) != 0 || report.ReceiptsChecked != 1 || report.BlobsChecked < 5 {
		t.Fatalf("native receipt replay failed: %+v", report)
	}
	resolved, err := ResolveVerifiedReceipt(store, result.Receipt.ReceiptID)
	if err != nil || resolved.ReceiptID != result.Receipt.ReceiptID || resolved.TaskID != "native-smoke" {
		t.Fatalf("resolve verified receipt failed: receipt=%+v err=%v", resolved, err)
	}

	rawPath := filepath.Join(store.Root(), filepath.FromSlash(result.Receipt.RawEventsBlob.RelativePath))
	if err := os.WriteFile(rawPath, []byte("tampered raw transcript\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tampered := Verify(store); len(tampered.Issues) == 0 {
		t.Fatal("native receipt replay accepted a tampered raw Codex transcript")
	}
}

func TestRunPersistsFailedTerminalReceipt(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	result, err := Run(context.Background(), store, testRunRequest(workspace, "force process failure"),
		Options{CodexPath: executable, Now: fixedAgentBridgeClock()})
	var executionError *ExecutionError
	if !errors.As(err, &executionError) || executionError.ReceiptID == "" ||
		result.Receipt.Outcome != OutcomeFailed || result.Receipt.FailureKind != "process_exit" ||
		result.Receipt.ProcessExitCode != 2 || result.Receipt.AgentMessageBlob != nil {
		t.Fatalf("failed execution did not preserve a terminal receipt: result=%+v err=%v", result, err)
	}
	if report := Verify(store); len(report.Issues) != 0 || report.ReceiptsChecked != 1 {
		t.Fatalf("failed terminal receipt does not replay: %+v", report)
	}
}

func TestWorkspaceChangeAfterStartedLeavesFailedTerminalWithoutLaunchingCodex(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	archive, err := store.PutBlob(strings.NewReader("sealed workspace archive"))
	if err != nil {
		t.Fatal(err)
	}
	binding := WorkspaceBinding{Archive: archive, TreeSHA256: strings.Repeat("a", 64),
		Format: "tar/v1", Policy: "test/v1", Files: 1, UncompressedBytes: 24}
	checks := 0
	result, err := Run(context.Background(), store, testRunRequest(workspace, "Return a verified answer."), Options{
		CodexPath: executable, WorkspaceBinding: &binding, Now: fixedAgentBridgeClock(),
		WorkspacePreflight: func() error {
			checks++
			if checks == 2 {
				return errors.New("workspace changed after the native start was recorded")
			}
			return nil
		},
	})
	var executionError *ExecutionError
	if !errors.As(err, &executionError) || checks != 2 || result.Receipt.Outcome != OutcomeFailed ||
		result.Receipt.FailureKind != "workspace_changed" || result.Receipt.ProcessExitCode == 0 {
		t.Fatalf("workspace preflight failure was not terminal: result=%+v checks=%d err=%v", result, checks, err)
	}
	if report := Verify(store); len(report.Issues) != 0 || report.ReceiptsChecked != 1 {
		t.Fatalf("workspace preflight failure does not replay: %+v", report)
	}
}

func TestExecutableChangeAfterStartedLeavesVerifiedFailedTerminal(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	checks := 0
	result, err := Run(context.Background(), store, testRunRequest(workspace, "Return a verified answer."), Options{
		CodexPath: executable, Now: fixedAgentBridgeClock(),
		beforeExecutableCheck: func(staged string) error {
			checks++
			if writeErr := os.WriteFile(staged, []byte("changed after start"), 0o700); writeErr != nil {
				return writeErr
			}
			return nil
		},
	})
	var executionError *ExecutionError
	if !errors.As(err, &executionError) || checks != 1 || result.Receipt.Outcome != OutcomeFailed ||
		result.Receipt.FailureKind != "executable_changed" || result.Receipt.RawEventsBlob.Bytes != 0 ||
		result.Receipt.AgentMessageBlob != nil {
		t.Fatalf("executable change did not produce a non-launched terminal: result=%+v checks=%d err=%v", result, checks, err)
	}
	if report := Verify(store); len(report.Issues) != 0 || report.ReceiptsChecked != 1 {
		t.Fatalf("executable-change terminal does not replay: %+v", report)
	}
}

func TestWorkspaceArchiveVerificationUsesBoundedStreamingReads(t *testing.T) {
	const size = int64(8 * 1024 * 1024)
	hasher := sha256.New()
	if _, err := io.Copy(hasher, &boundedZeroReader{remaining: size, maximumRead: 32 * 1024}); err != nil {
		t.Fatal(err)
	}
	reference := ledger.BlobRef{SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: size,
		RelativePath: "blobs/sha256/streaming-test"}
	reader := &boundedZeroReader{remaining: size, maximumRead: 32 * 1024}
	if err := verifyBlobContent(reader, reference); err != nil {
		t.Fatal(err)
	}
	if reader.largestRead > reader.maximumRead {
		t.Fatalf("workspace archive verification requested an unbounded buffer: %d", reader.largestRead)
	}
}

type boundedZeroReader struct {
	remaining   int64
	maximumRead int
	largestRead int
}

func (reader *boundedZeroReader) Read(buffer []byte) (int, error) {
	if len(buffer) > reader.largestRead {
		reader.largestRead = len(buffer)
	}
	if len(buffer) > reader.maximumRead {
		return 0, errors.New("reader was asked for an unbounded buffer")
	}
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	count := len(buffer)
	if int64(count) > reader.remaining {
		count = int(reader.remaining)
	}
	clear(buffer[:count])
	reader.remaining -= int64(count)
	return count, nil
}
func TestRunRejectsUnknownLoadoutReceiptBeforeExecution(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	request := testRunRequest(workspace, "Do not execute this task.")
	request.LoadoutContextReceiptID = "loadout-context-unknown"
	_, err := Run(context.Background(), store, request, Options{
		CodexPath: executable, PortableRoot: filepath.Join(t.TempDir(), "portable"),
		Now: fixedAgentBridgeClock(),
	})
	if err == nil {
		t.Fatal("native execution accepted an unknown loadout context receipt")
	}
	if report := store.Verify(); report.RecordsChecked != 0 || len(report.Issues) != 0 {
		t.Fatalf("rejected loadout execution changed the evidence ledger: %+v", report)
	}
}

func TestLoadoutUseLeaseBlocksPortableMutationThroughNativeExecution(t *testing.T) {
	t.Setenv("AGENTBRIDGE_TEST_HELPER", "1")
	store, workspace, executable := agentBridgeFixture(t)
	repository := filepath.Join(t.TempDir(), "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	revision := writeAgentBridgeRevision(t, repository, "Keep the verified memory head stable during delivery.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name: "Stable delivery", Agents: []ledger.Agent{ledger.AgentCodex},
		Scope: review.Scope{Kind: review.ScopeGlobal, Value: "*"}, MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	contextResult, err := loadoutcontext.BuildContext(store, repository, created.Loadout.LoadoutID,
		retrieval.Context{Agent: ledger.AgentCodex, ThreadID: "lease-thread", Task: "native-smoke",
			Channel: retrieval.ChannelHarness})
	if err != nil {
		t.Fatal(err)
	}
	request := testRunRequest(workspace, "Return a verified answer.")
	request.LoadoutContextReceiptID = contextResult.Receipt.ReceiptID
	ready := filepath.Join(t.TempDir(), "helper-ready")
	release := filepath.Join(t.TempDir(), "helper-release")
	t.Setenv("AGENTBRIDGE_BLOCK_READY", ready)
	t.Setenv("AGENTBRIDGE_BLOCK_RELEASE", release)
	type executionResult struct {
		result RunResult
		err    error
	}
	completed := make(chan executionResult, 1)
	go func() {
		result, err := Run(context.Background(), store, request, Options{CodexPath: executable,
			PortableRoot: repository, Now: fixedAgentBridgeClock()})
		completed <- executionResult{result: result, err: err}
	}()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native execution did not reach the loadout-backed process boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if mutationLock, err := portable.AcquireRepositoryLock(repository); err == nil {
		_ = mutationLock.Release()
		t.Fatal("portable mutation lock was acquired during loadout-backed execution")
	} else if !strings.Contains(err.Error(), "repository is locked") {
		t.Fatalf("unexpected portable mutation failure: %v", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case finished := <-completed:
		if finished.err != nil || finished.result.Receipt.Outcome != OutcomeCompleted {
			t.Fatalf("loadout-backed native execution failed: result=%+v err=%v", finished.result, finished.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("loadout-backed native execution did not finish")
	}
}

func TestParserAcceptsAdditiveFieldsAndRejectsMalformedEvents(t *testing.T) {
	valid := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread","unknown":1}`,
		`{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"answer","unknown":true}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0},"unknown":[]}`,
	}, "\n")
	parsed, err := parseExecutionJSONL([]byte(valid))
	if err != nil || parsed.ThreadID != "thread" || parsed.AgentMessage != "answer" || parsed.Events != 3 {
		t.Fatalf("additive Codex fields were not accepted: parsed=%+v err=%v", parsed, err)
	}
	if _, err := parseExecutionJSONL([]byte("{not-json}\n")); err == nil {
		t.Fatal("malformed Codex JSONL was accepted")
	}
}

func agentBridgeFixture(t *testing.T) (*ledger.Store, string, string) {
	t.Helper()
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return store, workspace, executable
}

func testRunRequest(workspace, prompt string) RunRequest {
	return RunRequest{SchemaVersion: RunRequestSchema, TaskID: "native-smoke", Prompt: prompt,
		Model: "default", Sandbox: "read-only", WorkingDirectory: workspace, TimeoutSeconds: 60,
		SkipGitRepositoryCheck: true, Privacy: PrivacyLocalOnly}
}

func fixedAgentBridgeClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC) }
}

func writeAgentBridgeRevision(t *testing.T, root, text string) portable.Revision {
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
		SchemaVersion:           portable.RevisionSchemaVersion,
		MemoryID:                "memory-" + hex.EncodeToString(memoryDigest[:]),
		Action:                  portable.ActionPromote,
		Status:                  portable.StatusActive,
		Kind:                    candidates.KindDirective,
		ScopeKind:               review.ScopeGlobal,
		ScopeValue:              "*",
		EvidenceBasis:           []review.Basis{review.BasisExplicitRemember},
		Text:                    text,
		TextSHA256:              textSHA,
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
	return revision
}
