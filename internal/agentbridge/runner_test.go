package agentbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
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
