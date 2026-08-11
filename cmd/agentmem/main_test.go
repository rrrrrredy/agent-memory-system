package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
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
		{name: "missing eval subcommand", args: []string{"eval"}, message: "eval <corpus ...|oracle init|sut bind|trial select|trial preregister"},
		{name: "unknown eval subcommand", args: []string{"eval", "unknown"}, message: "eval <corpus ...|oracle init|sut bind|trial select|trial preregister"},
		{name: "missing eval corpus subcommand", args: []string{"eval", "corpus"}, message: "eval <corpus ...|oracle init|sut bind|trial select|trial preregister"},
		{name: "unknown eval corpus subcommand", args: []string{"eval", "corpus", "unknown"}, message: "eval <corpus ...|oracle init|sut bind|trial select|trial preregister"},
		{name: "eval corpus baseline flags", args: []string{"eval", "corpus", "baseline"}, message: "requires --root, --corpus, --run, and --system-version"},
		{name: "eval corpus freeze flags", args: []string{"eval", "corpus", "freeze"}, message: "requires --root and --legacy-root"},
		{name: "eval corpus verify flags", args: []string{"eval", "corpus", "verify"}, message: "requires --root and --corpus"},
		{name: "eval corpus review pack flags", args: []string{"eval", "corpus", "review-pack"}, message: "requires --root, --corpus, and --candidates"},
		{name: "eval corpus review queue flags", args: []string{"eval", "corpus", "review-queue"}, message: "requires --root and --pack"},
		{name: "eval corpus Agent assessment flags", args: []string{"eval", "corpus", "agent-assessment", "prepare"}, message: "requires --root and --queue"},
		{name: "eval corpus Agent assessment import flags", args: []string{"eval", "corpus", "agent-assessment", "import-external"}, message: "requires projection, submission, assessor, claimed model, harness, prompt, and time metadata"},
		{name: "eval corpus controlled Agent assessment flags", args: []string{"eval", "corpus", "agent-assessment", "run-openai"}, message: "requires --root, --projection, --model, and --confirm-remote-disclosure"},
		{name: "eval attest flags", args: []string{"eval", "attest"}, message: "requires --root and --file"},
		{name: "eval sut bind flags", args: []string{"eval", "sut", "bind"}, message: "requires root, agent, provider, model, and four artifact files"},
		{name: "eval oracle init flags", args: []string{"eval", "oracle", "init"}, message: "requires --file"},
		{name: "eval trial preregister flags", args: []string{"eval", "trial", "preregister"}, message: "requires root, corpus, suite, selected corpus artifact, agent, semantic key, both arm contexts, four artifact files, and registry"},
		{name: "eval attempt preregister flags", args: []string{"eval", "attempt", "preregister"}, message: "requires root, draft, four artifact files, registry, thread, and session"},
		{name: "eval attempt execute flags", args: []string{"eval", "attempt", "execute"}, message: "requires --root, --file, and --repo"},
		{name: "eval attempt observe flags", args: []string{"eval", "attempt", "observe"}, message: "requires --root, --file, and --result"},
		{name: "eval attempt finalize flags", args: []string{"eval", "attempt", "finalize"}, message: "requires --root, --file, and --oracle-registry"},
		{name: "eval attempt record flags", args: []string{"eval", "attempt", "record"}, message: "requires --root and --file"},
		{name: "eval attempt verify flags", args: []string{"eval", "attempt", "verify"}, message: "requires --root and --receipt"},
		{name: "eval compaction seal flags", args: []string{"eval", "compaction", "seal"}, message: "requires --root and --file"},
		{name: "eval prepare flags", args: []string{"eval", "prepare"}, message: "requires --root, --repo, --oracle-registry, --corpus, --suite, --run, and --system-version"},
		{name: "eval run flags", args: []string{"eval", "run"}, message: "requires --root and --file"},
		{name: "eval verify flags", args: []string{"eval", "verify"}, message: "requires --root, --suite, and --run"},
		{name: "missing inject adapter", args: []string{"inject"}, message: "inject <codex|claude-code|opencode>"},
		{name: "unknown inject adapter", args: []string{"inject", "unknown"}, message: "inject <codex|claude-code|opencode>"},
		{name: "codex inject flags", args: []string{"inject", "codex"}, message: "requires --root and --repo"},
		{name: "claude inject flags", args: []string{"inject", "claude-code"}, message: "requires --root and --repo"},
		{name: "opencode inject flags", args: []string{"inject", "opencode"}, message: "requires --root and --repo"},
		{name: "missing backup subcommand", args: []string{"backup"}, message: "backup <keygen|create|verify|restore>"},
		{name: "unknown backup subcommand", args: []string{"backup", "unknown"}, message: "backup <keygen|create|verify|restore>"},
		{name: "backup keygen flags", args: []string{"backup", "keygen"}, message: "requires --identity"},
		{name: "backup create flags", args: []string{"backup", "create"}, message: "requires --root, --output, and at least one --recipient"},
		{name: "backup verify flags", args: []string{"backup", "verify"}, message: "requires --archive and at least one --identity"},
		{name: "backup restore flags", args: []string{"backup", "restore"}, message: "requires --archive, --target, and at least one --identity"},
		{name: "missing capture subcommand", args: []string{"capture"}, message: "capture <opencode"},
		{name: "unknown capture subcommand", args: []string{"capture", "unknown"}, message: "capture <opencode"},
		{name: "capture recover flags", args: []string{"capture", "recover"}, message: "requires --root, --source-root, and --manifest"},
		{name: "capture recovery plan kind", args: []string{"capture", "plan-recovery"}, message: "plan-recovery legacy-codex"},
		{name: "capture recovery plan flags", args: []string{"capture", "plan-recovery", "legacy-codex"}, message: "requires --root, --corpus, --source-root, and --output"},
		{name: "missing capture supervisor subcommand", args: []string{"capture", "supervisor"}, message: "supervisor <configure|run|watch|status|recover|clear-stale-lock>"},
		{name: "unknown capture supervisor subcommand", args: []string{"capture", "supervisor", "unknown"}, message: "supervisor <configure|run|watch|status|recover|clear-stale-lock>"},
		{name: "capture supervisor configure flags", args: []string{"capture", "supervisor", "configure"}, message: "requires --root and --file"},
		{name: "capture supervisor run flags", args: []string{"capture", "supervisor", "run"}, message: "requires --root"},
		{name: "capture supervisor watch flags", args: []string{"capture", "supervisor", "watch"}, message: "requires --root"},
		{name: "capture supervisor status flags", args: []string{"capture", "supervisor", "status"}, message: "requires --root"},
		{name: "capture supervisor recover flags", args: []string{"capture", "supervisor", "recover"}, message: "requires --root"},
		{name: "missing review subcommand", args: []string{"review"}, message: "review <list|decide|apply|status|verify>"},
		{name: "unknown review subcommand", args: []string{"review", "unknown"}, message: "review <list|decide|apply|status|verify>"},
		{name: "review list flags", args: []string{"review", "list"}, message: "requires --root and --candidates"},
		{name: "review decide flags", args: []string{"review", "decide"}, message: "requires --root, --candidates, --candidate, --action, --reviewer, and --reason"},
		{name: "review apply flags", args: []string{"review", "apply"}, message: "requires --root, --candidates, and --file"},
		{name: "review status flags", args: []string{"review", "status"}, message: "requires --root, --candidates, and --candidate"},
		{name: "review verify flags", args: []string{"review", "verify"}, message: "requires --root"},
		{name: "missing promote subcommand", args: []string{"promote"}, message: "promote <candidate|scan|apply|status|verify>"},
		{name: "unknown promote subcommand", args: []string{"promote", "unknown"}, message: "promote <candidate|scan|apply|status|verify>"},
		{name: "promote candidate flags", args: []string{"promote", "candidate"}, message: "requires --root, --candidates, --candidate, --approver, --confirm-text-sha256, and --reason"},
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
		{name: "missing recall subcommand", args: []string{"recall"}, message: "recall <search|get|context|adoption|verify>"},
		{name: "unknown recall subcommand", args: []string{"recall", "unknown"}, message: "recall <search|get|context|adoption|verify>"},
		{name: "recall search flags", args: []string{"recall", "search"}, message: "requires --query"},
		{name: "recall get flags", args: []string{"recall", "get"}, message: "requires --memory"},
		{name: "recall context flags", args: []string{"recall", "context"}, message: "requires --root and --repo"},
		{name: "recall adoption flags", args: []string{"recall", "adoption"}, message: "requires --root and --file"},
		{name: "recall verify flags", args: []string{"recall", "verify"}, message: "requires --root"},
		{name: "missing serve subcommand", args: []string{"serve"}, message: "serve mcp"},
		{name: "unknown serve subcommand", args: []string{"serve", "unknown"}, message: "serve mcp"},
		{name: "serve mcp flags", args: []string{"serve", "mcp"}, message: "requires --root, --repo, and --agent"},
		{name: "missing sync subcommand", args: []string{"sync"}, message: "sync <bootstrap|verify|run|install-hooks|auto>"},
		{name: "unknown sync subcommand", args: []string{"sync", "unknown"}, message: "sync <bootstrap|verify|run|install-hooks|auto>"},
		{name: "sync bootstrap flags", args: []string{"sync", "bootstrap"}, message: "requires --repo"},
		{name: "sync verify flags", args: []string{"sync", "verify"}, message: "requires --repo"},
		{name: "sync run flags", args: []string{"sync", "run"}, message: "requires --repo"},
		{name: "sync install hooks flags", args: []string{"sync", "install-hooks"}, message: "requires --repo"},
		{name: "missing sync auto subcommand", args: []string{"sync", "auto"}, message: "sync auto <enable|disable|status|run|recover>"},
		{name: "unknown sync auto subcommand", args: []string{"sync", "auto", "unknown"}, message: "sync auto <enable|disable|status|run|recover>"},
		{name: "sync auto enable flags", args: []string{"sync", "auto", "enable"}, message: "requires --repo and --root"},
		{name: "sync auto disable flags", args: []string{"sync", "auto", "disable"}, message: "requires --repo"},
		{name: "sync auto status flags", args: []string{"sync", "auto", "status"}, message: "requires --repo"},
		{name: "sync auto run flags", args: []string{"sync", "auto", "run"}, message: "requires --repo"},
		{name: "sync auto recover flags", args: []string{"sync", "auto", "recover"}, message: "requires --repo"},
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

func TestEvaluationRunRejectsLegacyUnboundContinuousInput(t *testing.T) {
	root := t.TempDir()
	if _, err := ledger.Init(root); err != nil {
		t.Fatal(err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	stdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = stdout }()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "evals", "fixtures", "quality-pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"quality_profile": "component"`,
		`"quality_profile": "continuous_learning"`, 1))
	fixture = filepath.Join(t.TempDir(), "legacy-unbound-continuous.json")
	if err := os.WriteFile(fixture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	err = runEvaluationRun([]string{"--root", root, "--file", fixture, "--enforce"})
	if err == nil || !strings.Contains(err.Error(), "fixed policy") {
		t.Fatalf("legacy unbound continuous input was accepted: %v", err)
	}
}

func TestTopLevelHelpSucceeds(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	stdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = stdout }()
	if err := run([]string{"--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandGroupHelpSucceeds(t *testing.T) {
	for _, group := range []string{
		"import", "inject", "capture", "backup", "derive", "eval", "review", "promote",
		"rule-approval", "portable", "recall", "serve", "sync",
	} {
		if err := run([]string{group, "--help"}); err != nil {
			t.Fatalf("%s --help: %v", group, err)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
}

func TestSyncBootstrapLeavesRepositoryHooksDisabledByDefault(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "portable")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "init", "--initial-branch", "main")
	runTestGit(t, repository, "config", "user.name", "Test User")
	runTestGit(t, repository, "config", "user.email", "test@example.invalid")
	if err := run([]string{"sync", "bootstrap", "--repo", repository}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "pre-push"} {
		path := filepath.Join(repository, ".git", "hooks", name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("bootstrap installed %s without explicit opt-in: %v", name, err)
		}
	}
}

func runTestGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
