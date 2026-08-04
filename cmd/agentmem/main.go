package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

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
	default:
		return usageError()
	}
}

func usageError() error {
	return errors.New("usage: agentmem <init|doctor> --root <local-evidence-directory>")
}
