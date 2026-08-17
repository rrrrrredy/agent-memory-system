package main

import (
	"context"
	"errors"
	"flag"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/compatibility"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func runCompatibility(args []string) error {
	flags := flag.NewFlagSet("compatibility", flag.ContinueOnError)
	var values repeatedStrings
	flags.Var(&values, "agent", "Agent runtime to probe: codex, claude-code, opencode, or deepseek-harness; repeatable")
	root := flags.String("root", "", "optional local evidence root used to verify native Codex receipts")
	timeout := flags.Duration("timeout", 5*time.Second, "maximum version-probe duration per Agent")
	requireAll := flags.Bool("require-all", false, "return a non-zero status unless every requested Agent is executable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return errors.New("compatibility timeout must be positive")
	}
	agents := make([]ledger.Agent, 0, len(values))
	for _, value := range values {
		agent, err := compatibilityAgent(value)
		if err != nil {
			return err
		}
		agents = append(agents, agent)
	}
	var store *ledger.Store
	if *root != "" {
		opened, err := ledger.Open(*root)
		store = opened
		if err != nil {
			return err
		}
	}
	report := compatibility.Probe(context.Background(), compatibility.Options{
		Agents: agents, Timeout: *timeout, EvidenceStore: store,
	})
	if err := encodeIndented(report); err != nil {
		return err
	}
	if *requireAll && !report.Ready {
		return errors.New("one or more requested Agent runtimes are unavailable")
	}
	return nil
}

func compatibilityAgent(value string) (ledger.Agent, error) {
	switch value {
	case "codex":
		return ledger.AgentCodex, nil
	case "claude-code":
		return ledger.AgentClaudeCode, nil
	case "opencode":
		return ledger.AgentOpenCode, nil
	case "deepseek-harness":
		return ledger.AgentDeepSeekHarness, nil
	default:
		return "", errors.New("compatibility --agent must be codex, claude-code, opencode, or deepseek-harness")
	}
}
