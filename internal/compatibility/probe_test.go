package compatibility

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

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
		report.Agents[0].NativeExecutionVerified || len(report.Agents[0].CaptureModes) == 0 {
		t.Fatalf("available runtime was misreported: %+v", report.Agents[0])
	}
	if report.Agents[1].Agent != ledger.AgentOpenCode || report.Agents[1].RuntimeStatus != RuntimeNotFound ||
		report.Agents[1].RuntimeIssue != "command_not_found" || len(report.Agents[1].RetrievalModes) == 0 {
		t.Fatalf("missing runtime lost its integration boundary: %+v", report.Agents[1])
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
	}
}
