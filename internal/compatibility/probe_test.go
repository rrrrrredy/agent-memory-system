package compatibility

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENTMEM_COMPATIBILITY_HELPER") == "1" {
		if len(os.Args) > 1 && os.Args[1] == "--version" {
			fmt.Println("codex-compatibility-test 1.0")
			return
		}
		_, _ = io.ReadAll(os.Stdin)
		fmt.Println(`{"type":"thread.started","thread_id":"compatibility-thread"}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"done"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
		return
	}
	os.Exit(m.Run())
}

func TestProbeSeparatesShippedIntegrationFromRuntimeAvailability(t *testing.T) {
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	report := Probe(context.Background(), Options{
		Agents: []ledger.Agent{ledger.AgentOpenCode, ledger.AgentCodex, ledger.AgentCodex},
		Now:    func() time.Time { return now },
		LookPath: func(command string) (string, error) {
			if command == "opencode" {
				return "", errors.New("missing")
			}
			return "runtime", nil
		},
		RunVersion: func(_ context.Context, path string) ([]byte, error) {
			return []byte(" codex 1.2.3\n"), nil
		},
		ReadFile: func(path string) ([]byte, error) { return []byte("exact-runtime"), nil },
	})
	if report.Ready || !report.GeneratedAt.Equal(now) || len(report.Agents) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Agents[0].Agent != ledger.AgentCodex || report.Agents[0].RuntimeStatus != RuntimeAvailable ||
		report.Agents[0].Version != "codex 1.2.3" || report.Agents[0].ExecutableSHA256 == "" ||
		report.Agents[0].NativeExecutionVerified || len(report.Agents[0].CaptureModes) == 0 ||
		report.Agents[0].ExecutionEvidence != "not_verified" ||
		report.Agents[0].ProviderIndependentlyAttested {
		t.Fatalf("available runtime was misreported: %+v", report.Agents[0])
	}
	if report.Agents[1].Agent != ledger.AgentOpenCode || report.Agents[1].RuntimeStatus != RuntimeNotFound ||
		report.Agents[1].RuntimeIssue != "command_not_found" || len(report.Agents[1].RetrievalModes) == 0 ||
		report.Agents[1].ExecutionEvidence != "hosted_runtime_smoke_only" {
		t.Fatalf("missing runtime lost its integration boundary: %+v", report.Agents[1])
	}
	if !report.Agents[0].HistoryImportAvailable || !report.Agents[1].HistoryImportAvailable {
		t.Fatalf("runtime availability was incorrectly used to gate history import: %+v", report.Agents)
	}
}

func TestDefaultProbeRunsTheExactBytesItHashes(t *testing.T) {
	t.Setenv("AGENTMEM_COMPATIBILITY_HELPER", "1")
	stageRoot := t.TempDir()
	t.Setenv("TMP", stageRoot)
	t.Setenv("TEMP", stageRoot)
	t.Setenv("TMPDIR", stageRoot)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(data)
	report := Probe(context.Background(), Options{Agents: []ledger.Agent{ledger.AgentCodex},
		LookPath: func(string) (string, error) { return executable, nil }})
	if !report.Ready || len(report.Agents) != 1 || report.Agents[0].RuntimeStatus != RuntimeAvailable ||
		report.Agents[0].Version != "codex-compatibility-test 1.0" ||
		report.Agents[0].ExecutableSHA256 != hex.EncodeToString(expected[:]) {
		t.Fatalf("default probe did not execute the exact hashed bytes: %+v", report)
	}
	entries, err := filepath.Glob(filepath.Join(stageRoot, "agentmem-compatibility-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("compatibility probe left staged executables behind: %v", entries)
	}
}
func TestProbeFailsClosedOnUnexecutableAndEmptyVersionCommands(t *testing.T) {
	calls := 0
	report := Probe(context.Background(), Options{
		Agents:   []ledger.Agent{ledger.AgentClaudeCode, ledger.AgentCodex},
		LookPath: func(command string) (string, error) { return command, nil },
		RunVersion: func(_ context.Context, path string) ([]byte, error) {
			calls++
			if path == "claude" {
				return nil, errors.New("blocked")
			}
			return []byte(" \n"), nil
		},
		ReadFile: func(path string) ([]byte, error) { return nil, errors.New("unreadable") },
	})
	if report.Ready || calls != 2 || len(report.Agents) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	for _, item := range report.Agents {
		if item.RuntimeStatus != RuntimeBlocked || item.RuntimeIssue == "" || item.ExecutableSHA256 != "" {
			t.Fatalf("unusable runtime was accepted: %+v", item)
		}
		if !item.HistoryImportAvailable {
			t.Fatalf("blocked version probe incorrectly disabled history import: %+v", item)
		}
	}
}

func TestProbeMarksCodexVerifiedOnlyAfterReplayableNativeReceipt(t *testing.T) {
	t.Setenv("AGENTMEM_COMPATIBILITY_HELPER", "1")
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := agentbridge.RunRequest{
		SchemaVersion: agentbridge.RunRequestSchema, TaskID: "compatibility-task", Prompt: "Return done.",
		Model: "default", Sandbox: "read-only", WorkingDirectory: workspace, TimeoutSeconds: 60,
		SkipGitRepositoryCheck: true, Privacy: agentbridge.PrivacyLocalOnly,
	}
	if _, err := agentbridge.Run(context.Background(), store, request, agentbridge.Options{CodexPath: executable}); err != nil {
		t.Fatal(err)
	}
	report := Probe(context.Background(), Options{
		Agents: []ledger.Agent{ledger.AgentCodex}, EvidenceStore: store,
		LookPath:   func(string) (string, error) { return executable, nil },
		RunVersion: func(context.Context, string) ([]byte, error) { return []byte("codex-test 1.0"), nil },
		ReadFile:   os.ReadFile,
	})
	if !report.Ready || len(report.Agents) != 1 || !report.Agents[0].NativeExecutionVerified ||
		report.Agents[0].ExecutionEvidence != "local_replayable_receipt" ||
		report.Agents[0].ProviderIndependentlyAttested {
		t.Fatalf("verified native receipt was not reflected honestly: %+v", report)
	}
	changed := Probe(context.Background(), Options{
		Agents: []ledger.Agent{ledger.AgentCodex}, EvidenceStore: store,
		LookPath:   func(string) (string, error) { return executable, nil },
		RunVersion: func(context.Context, string) ([]byte, error) { return []byte("codex-test 2.0"), nil },
		ReadFile:   func(string) ([]byte, error) { return []byte("different-codex-bytes"), nil },
	})
	if !changed.Ready || len(changed.Agents) != 1 || changed.Agents[0].NativeExecutionVerified ||
		changed.Agents[0].ExecutionEvidence != "not_verified" ||
		len(changed.Agents[0].Limitations) < 3 {
		t.Fatalf("historical receipt was misreported for changed executable bytes: %+v", changed)
	}
}
