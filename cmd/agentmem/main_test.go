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
