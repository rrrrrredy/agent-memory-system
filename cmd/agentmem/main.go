package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

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
		if len(args) < 2 || args[1] != "codex" {
			return errors.New("usage: agentmem import codex --root <local-evidence-directory> --path <rollout-file-or-directory>")
		}
		flags := flag.NewFlagSet("import codex", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		path := flags.String("path", "", "Codex rollout file or directory (required)")
		full := flags.Bool("full-reconcile", false, "re-read all source bytes and append only changed events")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if *root == "" || *path == "" {
			return errors.New("import codex requires --root and --path")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		result, err := codex.ImportPath(store, *path, codex.Options{FullReconcile: *full})
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: agentmem <init|doctor|import codex> [options]")
}
