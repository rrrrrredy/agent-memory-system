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
	flags.Var(&values, "agent", "Agent runtime to probe: codex, claude-code, or opencode; repeatable")
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
		agent, err := captureAgent(value)
		if err != nil {
			return err
		}
		agents = append(agents, agent)
	}
	report := compatibility.Probe(context.Background(), compatibility.Options{Agents: agents, Timeout: *timeout})
	if err := encodeIndented(report); err != nil {
		return err
	}
	if *requireAll && !report.Ready {
		return errors.New("one or more requested Agent runtimes are unavailable")
	}
	return nil
}
