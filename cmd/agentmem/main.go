package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentmem:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "init":
		flags := flag.NewFlagSet("init", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" {
			return errors.New("init requires --root")
		}
		store, err := ledger.Init(*root)
		if err != nil {
			return err
		}
		fmt.Println(store.Root())
		return nil
	case "doctor":
		flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" {
			return errors.New("doctor requires --root")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		report := store.Verify()
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if len(report.Issues) > 0 {
			return errors.New("evidence verification failed")
		}
		return nil
	case "import":
		if len(args) < 2 {
			return importUsageError()
		}
		return runImport(args[1:])
	default:
		return usageError()
	}
}

func runImport(args []string) error {
	agent := args[0]
	if agent != "codex" && agent != "claude" && agent != "claude-home" {
		return importUsageError()
	}
	flags := flag.NewFlagSet("import "+agent, flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	path := flags.String("path", "", agent+" transcript file or directory (required)")
	full := flags.Bool("full-reconcile", false, "re-read all source bytes and append only changed events")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *path == "" {
		return fmt.Errorf("import %s requires --root and --path", agent)
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	var result any
	switch agent {
	case "codex":
		result, err = codex.ImportPath(store, *path, codex.Options{FullReconcile: *full})
	case "claude":
		result, err = claudecode.ImportPath(store, *path, claudecode.Options{FullReconcile: *full})
	case "claude-home":
		result, err = claudecode.ImportHome(store, *path, claudecode.Options{FullReconcile: *full})
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func usageError() error {
	return errors.New("usage: agentmem <init|doctor|import> [options]")
}

func importUsageError() error {
	return errors.New("usage: agentmem import <codex|claude|claude-home> --root <local-evidence-directory> --path <source-file-or-directory>")
}
