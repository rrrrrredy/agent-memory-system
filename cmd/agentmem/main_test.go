package main

import (
	"strings"
	"testing"
)

func TestDeriveCommandDispatchAndRequiredFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "missing subcommand", args: []string{"derive"}, message: "derive <episodes|candidates>"},
		{name: "unknown subcommand", args: []string{"derive", "unknown"}, message: "derive <episodes|candidates>"},
		{name: "candidate flags", args: []string{"derive", "candidates"}, message: "requires --root and --episodes"},
		{name: "episode flags", args: []string{"derive", "episodes"}, message: "requires --root"},
		{name: "missing review subcommand", args: []string{"review"}, message: "review <apply|status|verify>"},
		{name: "unknown review subcommand", args: []string{"review", "unknown"}, message: "review <apply|status|verify>"},
		{name: "review apply flags", args: []string{"review", "apply"}, message: "requires --root, --candidates, and --file"},
		{name: "review status flags", args: []string{"review", "status"}, message: "requires --root, --candidates, and --candidate"},
		{name: "review verify flags", args: []string{"review", "verify"}, message: "requires --root"},
		{name: "missing promote subcommand", args: []string{"promote"}, message: "promote <scan|apply|status|verify>"},
		{name: "unknown promote subcommand", args: []string{"promote", "unknown"}, message: "promote <scan|apply|status|verify>"},
		{name: "promote scan flags", args: []string{"promote", "scan"}, message: "requires --root, --candidates, and --candidate"},
		{name: "promote apply flags", args: []string{"promote", "apply"}, message: "requires --root and --file"},
		{name: "promote status flags", args: []string{"promote", "status"}, message: "requires --root and --memory"},
		{name: "promote verify flags", args: []string{"promote", "verify"}, message: "requires --root"},
		{name: "missing rule approval subcommand", args: []string{"rule-approval"}, message: "rule-approval <apply|status|verify>"},
		{name: "unknown rule approval subcommand", args: []string{"rule-approval", "unknown"}, message: "rule-approval <apply|status|verify>"},
		{name: "rule approval apply flags", args: []string{"rule-approval", "apply"}, message: "requires --root and --file"},
		{name: "rule approval status flags", args: []string{"rule-approval", "status"}, message: "requires --root, --memory, --revision, --surface, and --target"},
		{name: "rule approval verify flags", args: []string{"rule-approval", "verify"}, message: "requires --root"},
		{name: "missing portable subcommand", args: []string{"portable"}, message: "portable <init|export|verify>"},
		{name: "unknown portable subcommand", args: []string{"portable", "unknown"}, message: "portable <init|export|verify>"},
		{name: "portable init flags", args: []string{"portable", "init"}, message: "requires --repo"},
		{name: "portable export flags", args: []string{"portable", "export"}, message: "requires --root and --repo"},
		{name: "portable verify flags", args: []string{"portable", "verify"}, message: "requires --repo"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("run(%v) error = %v, want message containing %q", test.args, err, test.message)
			}
		})
	}
}
