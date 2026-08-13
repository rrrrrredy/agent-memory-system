package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func runAgent(args []string) error {
	switch args[0] {
	case "run":
		return runAgentExecution(args[1:])
	case "verify":
		return runAgentVerify(args[1:])
	default:
		return agentUsageError()
	}
}

func runAgentExecution(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return agentRunUsageError()
	}
	if args[0] != "codex" {
		return agentRunUsageError()
	}
	flags := flag.NewFlagSet("agent run codex", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	file := flags.String("file", "", "native Agent run request JSON (required)")
	codex := flags.String("codex", "", "Codex executable (required)")
	repository := flags.String("repo", "", "portable memory repository required by a loadout-backed request")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *file == "" || *codex == "" {
		return errors.New("agent run codex requires --root, --file, and --codex")
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	request, decodeErr := agentbridge.DecodeRequest(input)
	closeErr := input.Close()
	if decodeErr != nil {
		return decodeErr
	}
	if closeErr != nil {
		return closeErr
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, runErr := agentbridge.Run(context.Background(), store, request, agentbridge.Options{
		CodexPath: *codex, PortableRoot: *repository,
	})
	if runErr != nil && result.Receipt.ReceiptID == "" {
		return runErr
	}
	if err := encodeIndented(result); err != nil {
		return err
	}
	return runErr
}

func runAgentVerify(args []string) error {
	flags := flag.NewFlagSet("agent verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("agent verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := agentbridge.Verify(store)
	if err := encodeIndented(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("native Agent execution verification failed")
	}
	return nil
}
