package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/autosync"
	"github.com/rrrrrredy/agent-memory-system/internal/backup"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/hookcapture"
	"github.com/rrrrrredy/agent-memory-system/internal/hookinject"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/mcpserver"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/ruleapproval"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
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
	case "version":
		return encodeIndented(map[string]any{
			"schema_version": "agent-memory-version/v1alpha1",
			"version":        version,
			"commit":         commit,
			"build_date":     buildDate,
			"go_version":     runtime.Version(),
			"platform":       runtime.GOOS + "/" + runtime.GOARCH,
		})
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
		repository := flags.String("repo", "", "optional portable memory Git repository")
		requireRepository := flags.Bool("require-repo", false,
			"require and verify a portable memory Git repository")
		clearWriterLock := flags.Bool("clear-stale-writer-lock", false,
			"remove the writer lock after independently verifying no ledger writer is active")
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
		if *clearWriterLock {
			cleared, err := store.ClearStaleWriterLock()
			if err != nil {
				return err
			}
			report := store.Verify()
			if err := encodeIndented(map[string]any{
				"schema_version":      "evidence-writer-recovery-result/v1alpha1",
				"writer_lock_cleared": cleared,
				"verification":        report,
				"privacy":             "local_only",
			}); err != nil {
				return err
			}
			if len(report.Issues) > 0 {
				return errors.New("evidence verification failed")
			}
			return nil
		}
		report := diagnostics.Run(context.Background(), store, diagnostics.Options{
			Repository: *repository, RequireRepository: *requireRepository,
		})
		if err := encodeIndented(report); err != nil {
			return err
		}
		if !report.Ready {
			return errors.New("agent memory diagnostic checks failed")
		}
		return nil
	case "import":
		if len(args) < 2 {
			return importUsageError()
		}
		return runImport(args[1:])
	case "inject":
		if len(args) < 2 {
			return injectUsageError()
		}
		return runInject(args[1:])
	case "capture":
		if len(args) < 2 {
			return captureUsageError()
		}
		return runCapture(args[1:])
	case "backup":
		if len(args) < 2 {
			return backupUsageError()
		}
		return runBackup(args[1:])
	case "derive":
		if len(args) < 2 {
			return deriveUsageError()
		}
		return runDerive(args[1:])
	case "eval":
		if len(args) < 2 {
			return evalUsageError()
		}
		return runEvaluation(args[1:])
	case "review":
		if len(args) < 2 {
			return reviewUsageError()
		}
		return runReview(args[1:])
	case "promote":
		if len(args) < 2 {
			return promoteUsageError()
		}
		return runPromote(args[1:])
	case "rule-approval":
		if len(args) < 2 {
			return ruleApprovalUsageError()
		}
		return runRuleApproval(args[1:])
	case "portable":
		if len(args) < 2 {
			return portableUsageError()
		}
		return runPortable(args[1:])
	case "recall":
		if len(args) < 2 {
			return recallUsageError()
		}
		return runRecall(args[1:])
	case "serve":
		if len(args) < 2 {
			return serveUsageError()
		}
		return runServe(args[1:])
	case "sync":
		if len(args) < 2 {
			return syncUsageError()
		}
		return runSync(args[1:])
	default:
		return usageError()
	}
}

type repeatedStrings []string

func (values *repeatedStrings) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runBackup(args []string) error {
	switch args[0] {
	case "keygen":
		flags := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
		identity := flags.String("identity", "", "new private recovery identity path outside Git (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *identity == "" {
			return errors.New("backup keygen requires --identity")
		}
		result, err := backup.GenerateIdentity(*identity)
		if err != nil {
			return err
		}
		return encodeIndented(result)
	case "create":
		flags := flag.NewFlagSet("backup create", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		output := flags.String("output", "", "new encrypted archive path outside Git (required)")
		var recipients repeatedStrings
		flags.Var(&recipients, "recipient", "native age recipient; repeat for independent recovery keys")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *output == "" || len(recipients) == 0 {
			return errors.New("backup create requires --root, --output, and at least one --recipient")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		result, err := backup.Create(store, backup.CreateOptions{
			Output: *output, Recipients: recipients,
		})
		if err != nil {
			return err
		}
		return encodeIndented(result)
	case "verify":
		flags := flag.NewFlagSet("backup verify", flag.ContinueOnError)
		archive := flags.String("archive", "", "encrypted evidence archive (required)")
		maxBytes := flags.Int64("max-bytes", backup.DefaultMaxPlaintextBytes,
			"maximum authenticated plaintext bytes")
		maxFiles := flags.Int("max-files", backup.DefaultMaxFiles,
			"maximum authenticated archive files")
		var identities repeatedStrings
		flags.Var(&identities, "identity", "native age private identity file; may be repeated")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *archive == "" || len(identities) == 0 {
			return errors.New("backup verify requires --archive and at least one --identity")
		}
		result, verifyErr := backup.Verify(backup.VerifyOptions{
			Archive: *archive, IdentityPaths: identities,
			MaxPlaintextBytes: *maxBytes, MaxFiles: *maxFiles,
		})
		if err := encodeIndented(result); err != nil {
			return err
		}
		return verifyErr
	case "restore":
		flags := flag.NewFlagSet("backup restore", flag.ContinueOnError)
		archive := flags.String("archive", "", "encrypted evidence archive (required)")
		target := flags.String("target", "", "new local evidence directory outside Git (required)")
		maxBytes := flags.Int64("max-bytes", backup.DefaultMaxPlaintextBytes,
			"maximum restored plaintext bytes")
		maxFiles := flags.Int("max-files", backup.DefaultMaxFiles,
			"maximum restored files")
		var identities repeatedStrings
		flags.Var(&identities, "identity", "native age private identity file; may be repeated")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *archive == "" || *target == "" || len(identities) == 0 {
			return errors.New("backup restore requires --archive, --target, and at least one --identity")
		}
		result, restoreErr := backup.Restore(backup.RestoreOptions{
			Archive: *archive, IdentityPaths: identities, Target: *target,
			MaxPlaintextBytes: *maxBytes, MaxFiles: *maxFiles,
		})
		if err := encodeIndented(result); err != nil {
			return err
		}
		return restoreErr
	default:
		return backupUsageError()
	}
}

func runInject(args []string) error {
	var agent ledger.Agent
	switch args[0] {
	case "codex":
		agent = ledger.AgentCodex
	case "claude-code":
		agent = ledger.AgentClaudeCode
	case "opencode":
		agent = ledger.AgentOpenCode
	default:
		return injectUsageError()
	}
	flags := flag.NewFlagSet("inject "+args[0], flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	scopeRepository := flags.String("scope-repository", "", "trusted logical repository scope")
	scopeProject := flags.String("scope-project", "", "trusted logical project scope")
	scopeTask := flags.String("scope-task", "", "trusted logical task scope")
	limit := flags.Int("limit", retrieval.DefaultLimit, "maximum matching memories")
	tokenBudget := flags.Int("token-budget", 600, "maximum estimated tokens in additional context")
	byteBudget := flags.Int("byte-budget", 3072, "maximum UTF-8 bytes in additional context")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *repository == "" {
		return errors.New("inject requires --root and --repo")
	}
	output, _ := hookinject.Process(os.Stdin, hookinject.Config{
		EvidenceRoot: *root,
		PortableRoot: *repository,
		Agent:        agent,
		Repository:   *scopeRepository,
		Project:      *scopeProject,
		Task:         *scopeTask,
		Limit:        *limit,
		TokenBudget:  *tokenBudget,
		ByteBudget:   *byteBudget,
	})
	return json.NewEncoder(os.Stdout).Encode(output)
}

func runServe(args []string) error {
	if args[0] != "mcp" {
		return serveUsageError()
	}
	flags := flag.NewFlagSet("serve mcp", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	agent := flags.String("agent", "", "codex, claude_code, opencode, or unknown (required)")
	thread := flags.String("thread", "", "optional default source thread id")
	session := flags.String("session", "", "optional default source session id")
	scopeRepository := flags.String("scope-repository", "", "trusted logical repository scope")
	scopeProject := flags.String("scope-project", "", "trusted logical project scope")
	scopeTask := flags.String("scope-task", "", "trusted logical task scope")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *repository == "" || *agent == "" {
		return errors.New("serve mcp requires --root, --repo, and --agent")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return mcpserver.Run(ctx, mcpserver.Config{
		EvidenceRoot: *root,
		PortableRoot: *repository,
		Agent:        ledger.Agent(*agent),
		ThreadID:     *thread,
		SessionID:    *session,
		Repository:   *scopeRepository,
		Project:      *scopeProject,
		Task:         *scopeTask,
	})
}

func runRecall(args []string) error {
	switch args[0] {
	case "search":
		return runRecallQuery(args[1:], false)
	case "get":
		return runRecallQuery(args[1:], true)
	case "context":
		return runRecallContext(args[1:])
	case "adoption":
		return runRecallAdoption(args[1:])
	case "verify":
		return runRecallVerify(args[1:])
	default:
		return recallUsageError()
	}
}

type recallFlags struct {
	root            *string
	repository      *string
	agent           *string
	thread          *string
	session         *string
	scopeRepository *string
	scopeProject    *string
	scopeTask       *string
	channel         *string
	limit           *int
	tokenBudget     *int
	byteBudget      *int
}

func addRecallFlags(flags *flag.FlagSet) recallFlags {
	return recallFlags{
		root:            flags.String("root", "", "local evidence root (required)"),
		repository:      flags.String("repo", "", "portable memory repository root (required)"),
		agent:           flags.String("agent", string(ledger.AgentUnknown), "codex, claude_code, opencode, or unknown"),
		thread:          flags.String("thread", "", "optional source thread id"),
		session:         flags.String("session", "", "optional source session id"),
		scopeRepository: flags.String("scope-repository", "", "trusted logical repository scope"),
		scopeProject:    flags.String("scope-project", "", "trusted logical project scope"),
		scopeTask:       flags.String("scope-task", "", "trusted logical task scope"),
		channel:         flags.String("channel", string(retrieval.ChannelCLI), "delivery channel"),
		limit:           flags.Int("limit", retrieval.DefaultLimit, "maximum matching memories"),
		tokenBudget:     flags.Int("token-budget", retrieval.DefaultTokenBudget, "estimated token budget"),
		byteBudget:      flags.Int("byte-budget", retrieval.DefaultByteBudget, "UTF-8 byte budget"),
	}
}

func (values recallFlags) request(query, memoryID string) (retrieval.Request, error) {
	if *values.root == "" || *values.repository == "" {
		return retrieval.Request{}, errors.New("recall requires --root and --repo")
	}
	agent := ledger.Agent(*values.agent)
	context := retrieval.Context{
		Agent:      agent,
		ThreadID:   *values.thread,
		SessionID:  *values.session,
		Repository: *values.scopeRepository,
		Project:    *values.scopeProject,
		Task:       *values.scopeTask,
		Channel:    retrieval.DeliveryChannel(*values.channel),
	}
	return retrieval.Request{
		SchemaVersion: retrieval.RequestSchemaVersion,
		Query:         query,
		MemoryID:      memoryID,
		Context:       context,
		Limit:         *values.limit,
		TokenBudget:   *values.tokenBudget,
		ByteBudget:    *values.byteBudget,
	}, nil
}

func runRecallQuery(args []string, exact bool) error {
	name := "recall search"
	if exact {
		name = "recall get"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	values := addRecallFlags(flags)
	query := flags.String("query", "", "search query")
	memoryID := flags.String("memory", "", "exact memory id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if exact && *memoryID == "" {
		return errors.New("recall get requires --memory")
	}
	if !exact && *query == "" {
		return errors.New("recall search requires --query")
	}
	if exact {
		*query = ""
	} else {
		*memoryID = ""
	}
	request, err := values.request(*query, *memoryID)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*values.root)
	if err != nil {
		return err
	}
	result, searchErr := retrieval.Search(store, *values.repository, request)
	if err := encodeIndented(result); err != nil {
		return err
	}
	return searchErr
}

func runRecallContext(args []string) error {
	flags := flag.NewFlagSet("recall context", flag.ContinueOnError)
	values := addRecallFlags(flags)
	query := flags.String("query", "", "search query")
	memoryID := flags.String("memory", "", "exact memory id")
	output := flags.String("output", "json", "json or text")
	if err := flags.Parse(args); err != nil {
		return err
	}
	request, err := values.request(*query, *memoryID)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*values.root)
	if err != nil {
		return err
	}
	result, contextErr := retrieval.BuildContext(store, *values.repository, request)
	switch *output {
	case "json":
		if err := encodeIndented(result); err != nil {
			return err
		}
	case "text":
		if result.Content != "" {
			fmt.Print(result.Content)
		}
	default:
		return errors.New("recall context --output must be json or text")
	}
	return contextErr
}

func runRecallAdoption(args []string) error {
	flags := flag.NewFlagSet("recall adoption", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	requestPath := flags.String("file", "", "adoption request JSON file, or - for stdin (required)")
	agent := flags.String("agent", string(ledger.AgentUnknown), "codex, claude_code, opencode, or unknown")
	thread := flags.String("thread", "", "optional source thread id")
	session := flags.String("session", "", "optional source session id")
	channel := flags.String("channel", string(retrieval.ChannelCLI), "delivery channel")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *requestPath == "" {
		return errors.New("recall adoption requires --root and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open adoption request: %w", err)
		}
		defer file.Close()
		reader = file
	}
	request, err := retrieval.DecodeAdoptionRequest(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	receipt, err := retrieval.RecordAdoption(store, retrieval.Context{
		Agent: ledger.Agent(*agent), ThreadID: *thread, SessionID: *session,
		Channel: retrieval.DeliveryChannel(*channel),
	}, request)
	if err != nil {
		return err
	}
	return encodeIndented(receipt)
}

func runRecallVerify(args []string) error {
	flags := flag.NewFlagSet("recall verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("recall verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := retrieval.Verify(store)
	if err := encodeIndented(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("memory receipt verification failed")
	}
	return nil
}

func runEvaluation(args []string) error {
	switch args[0] {
	case "corpus":
		if len(args) < 2 {
			return evalUsageError()
		}
		return runEvaluationCorpus(args[1:])
	case "attest":
		return runEvaluationAttest(args[1:])
	case "run":
		return runEvaluationRun(args[1:])
	case "verify":
		return runEvaluationVerify(args[1:])
	default:
		return evalUsageError()
	}
}

func runEvaluationAttest(args []string) error {
	flags := flag.NewFlagSet("eval attest", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	requestPath := flags.String("file", "", "evaluation attestation JSON file, or - for stdin (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *requestPath == "" {
		return errors.New("eval attest requires --root and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open evaluation attestation: %w", err)
		}
		defer file.Close()
		reader = file
	}
	attestation, err := evaluation.DecodeAttestation(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := evaluation.RecordAttestation(store, attestation, nil)
	if err != nil {
		return err
	}
	return encodeIndented(result)
}

func runEvaluationCorpus(args []string) error {
	switch args[0] {
	case "baseline":
		flags := flag.NewFlagSet("eval corpus baseline", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		corpusID := flags.String("corpus", "", "frozen corpus id (required)")
		suiteID := flags.String("suite", "legacy-capture", "evaluation suite id")
		runID := flags.String("run", "", "evaluation run id (required)")
		systemVersion := flags.String("system-version", "", "system version under evaluation (required)")
		minimumCoverage := flags.Float64("minimum-coverage", 1, "minimum complete rollout coverage")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *corpusID == "" || *runID == "" || *systemVersion == "" {
			return errors.New("eval corpus baseline requires --root, --corpus, --run, and --system-version")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		input, err := evaluation.BuildLegacyCaptureInput(store, *corpusID,
			evaluation.LegacyCaptureInputOptions{
				SuiteID: *suiteID, RunID: *runID, SystemVersion: *systemVersion,
				MinimumCoverage: *minimumCoverage,
			})
		if err != nil {
			return err
		}
		return encodeIndented(input)
	case "freeze":
		flags := flag.NewFlagSet("eval corpus freeze", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		legacyRoot := flags.String("legacy-root", "", "legacy context-journal root (required)")
		name := flags.String("name", "legacy-context-journal", "safe corpus name")
		allowIncomplete := flags.Bool("allow-incomplete", false,
			"return success while preserving explicit legacy coverage issues")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *legacyRoot == "" {
			return errors.New("eval corpus freeze requires --root and --legacy-root")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		result, freezeErr := evaluation.FreezeLegacyCorpus(store, *legacyRoot,
			evaluation.FreezeOptions{Name: *name})
		if err := encodeIndented(result); err != nil {
			return err
		}
		if freezeErr != nil {
			return freezeErr
		}
		if len(result.Issues) != 0 && !*allowIncomplete {
			return errors.New("legacy corpus was frozen with explicit coverage issues")
		}
		return nil
	case "verify":
		flags := flag.NewFlagSet("eval corpus verify", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		corpusID := flags.String("corpus", "", "frozen corpus id (required)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *corpusID == "" {
			return errors.New("eval corpus verify requires --root and --corpus")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		report := evaluation.VerifyCorpus(store, *corpusID)
		if err := encodeIndented(report); err != nil {
			return err
		}
		if len(report.Issues) != 0 {
			return errors.New("legacy corpus verification failed")
		}
		return nil
	default:
		return evalUsageError()
	}
}

func runEvaluationRun(args []string) error {
	flags := flag.NewFlagSet("eval run", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	requestPath := flags.String("file", "", "evaluation input JSON file, or - for stdin (required)")
	portableRoot := flags.String("repo", "", "portable memory repository when revision evidence is used")
	enforce := flags.Bool("enforce", false, "return a failure when release gates do not pass")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *requestPath == "" {
		return errors.New("eval run requires --root and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open evaluation input: %w", err)
		}
		defer file.Close()
		reader = file
	}
	input, err := evaluation.DecodeInput(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := evaluation.Run(store, input, evaluation.RunOptions{PortableRoot: *portableRoot})
	if err != nil {
		return err
	}
	if err := encodeIndented(result); err != nil {
		return err
	}
	if *enforce && !result.Report.ReleaseReady {
		return errors.New("continuous-learning evaluation gates did not pass")
	}
	return nil
}

func runEvaluationVerify(args []string) error {
	flags := flag.NewFlagSet("eval verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	suiteID := flags.String("suite", "", "evaluation suite id (required)")
	runID := flags.String("run", "", "evaluation run id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *suiteID == "" || *runID == "" {
		return errors.New("eval verify requires --root, --suite, and --run")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := evaluation.VerifyRun(store, *suiteID, *runID)
	if err := encodeIndented(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("continuous-learning evaluation verification failed")
	}
	return nil
}

func runSync(args []string) error {
	switch args[0] {
	case "bootstrap":
		return runSyncBootstrap(args[1:])
	case "verify":
		return runSyncVerify(args[1:])
	case "run":
		return runSyncRun(args[1:])
	case "install-hooks":
		return runSyncInstallHooks(args[1:])
	case "auto":
		if len(args) < 2 {
			return syncAutoUsageError()
		}
		return runSyncAuto(args[1:])
	default:
		return syncUsageError()
	}
}

func runSyncAuto(args []string) error {
	switch args[0] {
	case "enable":
		return runSyncAutoEnable(args[1:])
	case "disable":
		return runSyncAutoDisable(args[1:])
	case "status":
		return runSyncAutoStatus(args[1:])
	case "run":
		return runSyncAutoRun(args[1:])
	case "recover":
		return runSyncAutoRecover(args[1:])
	default:
		return syncAutoUsageError()
	}
}

func runSyncAutoEnable(args []string) error {
	flags := flag.NewFlagSet("sync auto enable", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	root := flags.String("root", "", "local evidence root (required)")
	remote := flags.String("remote", gitsync.DefaultRemote, "Git remote name")
	intervalValue := flags.String("interval", autosync.DefaultInterval.String(), "schedule interval")
	maximumRetryValue := flags.String("maximum-retry", autosync.DefaultMaximumRetry.String(), "maximum retry delay")
	maximumErrors := flags.Int("maximum-errors", autosync.DefaultMaximumConsecutiveErrors, "errors before suspension")
	executableDefault, err := os.Executable()
	if err != nil {
		return errors.New("agentmem executable path is unavailable")
	}
	executable := flags.String("executable", executableDefault, "installed agentmem executable path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *root == "" {
		return errors.New("sync auto enable requires --repo and --root")
	}
	interval, err := time.ParseDuration(*intervalValue)
	if err != nil {
		return errors.New("sync auto enable has an invalid --interval")
	}
	maximumRetry, err := time.ParseDuration(*maximumRetryValue)
	if err != nil {
		return errors.New("sync auto enable has an invalid --maximum-retry")
	}
	status, err := autosync.Enable(context.Background(), autosync.EnableOptions{
		RepositoryRoot: *repository, EvidenceRoot: *root, ExecutablePath: *executable,
		Remote: *remote, Interval: interval, MaximumRetry: maximumRetry,
		MaximumConsecutiveErrors: *maximumErrors,
	})
	if err != nil {
		return err
	}
	return encodeIndented(status)
}

func runSyncAutoDisable(args []string) error {
	flags := flag.NewFlagSet("sync auto disable", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync auto disable requires --repo")
	}
	status, err := autosync.Disable(context.Background(), *repository)
	if err != nil {
		return err
	}
	return encodeIndented(status)
}

func runSyncAutoStatus(args []string) error {
	flags := flag.NewFlagSet("sync auto status", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync auto status requires --repo")
	}
	status, err := autosync.GetStatus(context.Background(), *repository)
	if err != nil {
		return err
	}
	if err := encodeIndented(status); err != nil {
		return err
	}
	if len(status.Issues) != 0 {
		return errors.New("automatic synchronization status has unresolved issues")
	}
	return nil
}

func runSyncAutoRun(args []string) error {
	flags := flag.NewFlagSet("sync auto run", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	scheduled := flags.Bool("scheduled", false, "suppress routine scheduler output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync auto run requires --repo")
	}
	result, runErr := autosync.Run(context.Background(), *repository)
	if !*scheduled {
		if err := encodeIndented(result); err != nil {
			return err
		}
	}
	return runErr
}

func runSyncAutoRecover(args []string) error {
	flags := flag.NewFlagSet("sync auto recover", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	clearLock := flags.Bool("clear-stale-lock", false, "remove a lock after verifying no automatic sync process is active")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync auto recover requires --repo")
	}
	if *clearLock {
		if _, err := autosync.ClearStaleLock(*repository); err != nil {
			return err
		}
	}
	status, err := autosync.Recover(context.Background(), *repository)
	if err != nil {
		return err
	}
	return encodeIndented(status)
}

func encodeIndented(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func runSyncBootstrap(args []string) error {
	flags := flag.NewFlagSet("sync bootstrap", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	remote := flags.String("remote", gitsync.DefaultRemote, "Git remote name")
	remoteURL := flags.String("remote-url", "", "optional private Git remote URL")
	hooks := flags.Bool("hooks", false, "install repository-local pre-commit and pre-push guards")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync bootstrap requires --repo")
	}
	result, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: *repository, RemoteName: *remote, RemoteURL: *remoteURL,
		InstallHooks: *hooks,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runSyncVerify(args []string) error {
	flags := flag.NewFlagSet("sync verify", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync verify requires --repo")
	}
	report := gitsync.Verify(context.Background(), *repository)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("Git synchronization verification failed")
	}
	return nil
}

func runSyncRun(args []string) error {
	flags := flag.NewFlagSet("sync run", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	remote := flags.String("remote", gitsync.DefaultRemote, "Git remote name")
	nonInteractive := flags.Bool("non-interactive", false, "disable Git credential prompts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync run requires --repo")
	}
	result, syncErr := gitsync.Run(context.Background(), gitsync.SyncOptions{
		RepositoryRoot: *repository, RemoteName: *remote,
		NonInteractive: *nonInteractive,
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return syncErr
}

func runSyncInstallHooks(args []string) error {
	flags := flag.NewFlagSet("sync install-hooks", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("sync install-hooks requires --repo")
	}
	if err := gitsync.InstallHooks(context.Background(), *repository); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema_version": "git-sync-hook-install-result/v1alpha1",
		"installed":      true,
		"privacy":        portable.PortablePrivacy,
	})
}

func runPortable(args []string) error {
	switch args[0] {
	case "init":
		return runPortableInit(args[1:])
	case "export":
		return runPortableExport(args[1:])
	case "verify":
		return runPortableVerify(args[1:])
	default:
		return portableUsageError()
	}
}

func runPortableInit(args []string) error {
	flags := flag.NewFlagSet("portable init", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("portable init requires --repo")
	}
	if err := portable.InitRepository(*repository); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema_version": "portable-memory-init-result/v1alpha1",
		"initialized":    true,
		"privacy":        portable.PortablePrivacy,
	})
}

func runPortableExport(args []string) error {
	flags := flag.NewFlagSet("portable export", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "portable memory repository root (required)")
	memoryID := flags.String("memory", "", "optional promoted memory id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *repository == "" {
		return errors.New("portable export requires --root and --repo")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	options := portable.ExportOptions{}
	if *memoryID != "" {
		options.MemoryIDs = []string{*memoryID}
	}
	result, err := portable.Export(store, *repository, options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runPortableVerify(args []string) error {
	flags := flag.NewFlagSet("portable verify", flag.ContinueOnError)
	repository := flags.String("repo", "", "portable memory repository root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *repository == "" {
		return errors.New("portable verify requires --repo")
	}
	report := portable.VerifyRepository(*repository)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if len(report.Issues) != 0 {
		return errors.New("portable memory repository verification failed")
	}
	return nil
}

func runPromote(args []string) error {
	switch args[0] {
	case "scan":
		return runPromoteScan(args[1:])
	case "apply":
		return runPromoteApply(args[1:])
	case "status":
		return runPromoteStatus(args[1:])
	case "verify":
		return runPromoteVerify(args[1:])
	default:
		return promoteUsageError()
	}
}

func runPromoteScan(args []string) error {
	flags := flag.NewFlagSet("promote scan", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	candidateGeneration := flags.String("candidates", "", "candidate generation path or directory name (required)")
	candidateID := flags.String("candidate", "", "candidate id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *candidateGeneration == "" || *candidateID == "" {
		return errors.New("promote scan requires --root, --candidates, and --candidate")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := promotion.ScanCandidate(store, *candidateGeneration, *candidateID)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runPromoteApply(args []string) error {
	flags := flag.NewFlagSet("promote apply", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	requestPath := flags.String("file", "", "promotion request JSON file, or - for stdin (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *requestPath == "" {
		return errors.New("promote apply requires --root and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open promotion request: %w", err)
		}
		defer file.Close()
		reader = file
	}
	request, err := promotion.DecodeRequest(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := promotion.Apply(store, request)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runPromoteStatus(args []string) error {
	flags := flag.NewFlagSet("promote status", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	memoryID := flags.String("memory", "", "promoted memory id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *memoryID == "" {
		return errors.New("promote status requires --root and --memory")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := promotion.GetStatus(store, *memoryID)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runPromoteVerify(args []string) error {
	flags := flag.NewFlagSet("promote verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("promote verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := promotion.Verify(store)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if len(report.Issues) > 0 {
		return errors.New("promoted memory verification failed")
	}
	return nil
}

func runRuleApproval(args []string) error {
	switch args[0] {
	case "apply":
		return runRuleApprovalApply(args[1:])
	case "status":
		return runRuleApprovalStatus(args[1:])
	case "verify":
		return runRuleApprovalVerify(args[1:])
	default:
		return ruleApprovalUsageError()
	}
}

func runRuleApprovalApply(args []string) error {
	flags := flag.NewFlagSet("rule-approval apply", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	requestPath := flags.String("file", "", "rule approval request JSON file, or - for stdin (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *requestPath == "" {
		return errors.New("rule-approval apply requires --root and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open rule approval request: %w", err)
		}
		defer file.Close()
		reader = file
	}
	request, err := ruleapproval.DecodeRequest(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := ruleapproval.Apply(store, request)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runRuleApprovalStatus(args []string) error {
	flags := flag.NewFlagSet("rule-approval status", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	memoryID := flags.String("memory", "", "promoted memory id (required)")
	revisionID := flags.String("revision", "", "promoted memory revision id (required)")
	surface := flags.String("surface", "", "rule surface (required)")
	target := flags.String("target", "", "exact rule target identifier (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *memoryID == "" || *revisionID == "" || *surface == "" || *target == "" {
		return errors.New("rule-approval status requires --root, --memory, --revision, --surface, and --target")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := ruleapproval.GetStatus(
		store, *memoryID, *revisionID, ruleapproval.Surface(*surface), *target,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runRuleApprovalVerify(args []string) error {
	flags := flag.NewFlagSet("rule-approval verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("rule-approval verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := ruleapproval.Verify(store)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if len(report.Issues) > 0 {
		return errors.New("rule approval verification failed")
	}
	return nil
}

func runReview(args []string) error {
	switch args[0] {
	case "apply":
		return runReviewApply(args[1:])
	case "status":
		return runReviewStatus(args[1:])
	case "verify":
		return runReviewVerify(args[1:])
	default:
		return reviewUsageError()
	}
}

func runReviewApply(args []string) error {
	flags := flag.NewFlagSet("review apply", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	candidateGeneration := flags.String("candidates", "", "candidate generation path or directory name (required)")
	requestPath := flags.String("file", "", "review request JSON file, or - for stdin (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *candidateGeneration == "" || *requestPath == "" {
		return errors.New("review apply requires --root, --candidates, and --file")
	}
	reader := os.Stdin
	var file *os.File
	var err error
	if *requestPath != "-" {
		file, err = os.Open(*requestPath)
		if err != nil {
			return fmt.Errorf("open review request: %w", err)
		}
		defer file.Close()
		reader = file
	}
	request, err := review.DecodeRequest(reader)
	if err != nil {
		return err
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := review.Apply(store, *candidateGeneration, request)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runReviewStatus(args []string) error {
	flags := flag.NewFlagSet("review status", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	candidateGeneration := flags.String("candidates", "", "candidate generation path or directory name (required)")
	candidateID := flags.String("candidate", "", "candidate id (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *candidateGeneration == "" || *candidateID == "" {
		return errors.New("review status requires --root, --candidates, and --candidate")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := review.GetStatus(store, *candidateGeneration, *candidateID)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runReviewVerify(args []string) error {
	flags := flag.NewFlagSet("review verify", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("review verify requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	report := review.Verify(store)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if len(report.Issues) > 0 {
		return errors.New("candidate review verification failed")
	}
	return nil
}

func runDerive(args []string) error {
	switch args[0] {
	case "episodes":
		return runDeriveEpisodes(args[1:])
	case "candidates":
		return runDeriveCandidates(args[1:])
	default:
		return deriveUsageError()
	}
}

func runDeriveEpisodes(args []string) error {
	flags := flag.NewFlagSet("derive episodes", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	shards := flags.Int("shards", 64, "power-of-two work shard count (1-256)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("derive episodes requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := episodes.Build(store, episodes.BuildOptions{ShardCount: *shards})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runDeriveCandidates(args []string) error {
	flags := flag.NewFlagSet("derive candidates", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	episodeGeneration := flags.String("episodes", "", "episode generation path or directory name (required)")
	shards := flags.Int("shards", 64, "power-of-two work shard count (1-256)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *episodeGeneration == "" {
		return errors.New("derive candidates requires --root and --episodes")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: *episodeGeneration, ShardCount: *shards,
	})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func runCapture(args []string) error {
	switch args[0] {
	case "opencode":
		flags := flag.NewFlagSet("capture opencode", flag.ContinueOnError)
		root := flags.String("root", "", "local evidence root (required)")
		staging := flags.String("staging", "", "raw staging directory outside Git (required)")
		binary := flags.String("binary", "opencode", "OpenCode executable")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *staging == "" {
			return errors.New("capture opencode requires --root and --staging")
		}
		store, err := ledger.Open(*root)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		result, captureErr := opencode.CaptureAll(ctx, store, opencode.CaptureOptions{
			Binary: *binary, StagingRoot: *staging,
		})
		if err := encodeIndented(result); err != nil {
			return err
		}
		return captureErr
	case "hook":
		return runCaptureHook(args[1:])
	case "reconcile":
		return runCaptureReconcile(args[1:])
	default:
		return captureUsageError()
	}
}

func runCaptureHook(args []string) error {
	if len(args) == 0 {
		return captureUsageError()
	}
	agent, err := captureAgent(args[0])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("capture hook "+args[0], flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("capture hook requires --root")
	}
	output := struct {
		Continue bool `json:"continue"`
	}{Continue: true}
	store, openErr := ledger.Open(*root)
	var captureErr error
	if openErr == nil {
		_, captureErr = hookcapture.Capture(store, agent, os.Stdin, hookcapture.CaptureOptions{})
	} else {
		captureErr = openErr
	}
	if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
		return err
	}
	if captureErr != nil {
		fmt.Fprintln(os.Stderr, "agentmem capture warning:", captureErr)
	}
	return nil
}

func runCaptureReconcile(args []string) error {
	if len(args) == 0 {
		return captureUsageError()
	}
	agent, err := captureAgent(args[0])
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("capture reconcile "+args[0], flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	path := flags.String("path", "", "Codex sessions path or Claude Code home (required)")
	full := flags.Bool("full-reconcile", false, "re-read all source bytes and append only changed events")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *path == "" {
		return errors.New("capture reconcile requires --root and --path")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	result, reconcileErr := hookcapture.Reconcile(store, agent, hookcapture.ReconcileOptions{
		SourcePath: *path, FullReconcile: *full,
	})
	if err := encodeIndented(result); err != nil {
		return err
	}
	return reconcileErr
}

func captureAgent(value string) (ledger.Agent, error) {
	switch value {
	case "codex":
		return ledger.AgentCodex, nil
	case "claude-code":
		return ledger.AgentClaudeCode, nil
	default:
		return "", captureUsageError()
	}
}

func runImport(args []string) error {
	agent := args[0]
	if agent != "codex" && agent != "claude" && agent != "claude-home" &&
		agent != "opencode-export" && agent != "opencode-events" {
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
	case "opencode-export":
		result, err = opencode.ImportPath(store, *path, opencode.Options{})
	case "opencode-events":
		result, err = opencode.ImportEventPath(store, *path,
			opencode.EventOptions{FullReconcile: *full})
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func usageError() error {
	return errors.New("usage: agentmem <version|init|doctor|import|inject|capture|backup|derive|eval|review|promote|rule-approval|portable|recall|serve|sync> [options]")
}

func backupUsageError() error {
	return errors.New("usage: agentmem backup <keygen|create|verify|restore> [options]")
}

func reviewUsageError() error {
	return errors.New("usage: agentmem review <apply|status|verify> [options]")
}

func promoteUsageError() error {
	return errors.New("usage: agentmem promote <scan|apply|status|verify> [options]")
}

func ruleApprovalUsageError() error {
	return errors.New("usage: agentmem rule-approval <apply|status|verify> [options]")
}

func portableUsageError() error {
	return errors.New("usage: agentmem portable <init|export|verify> [options]")
}

func recallUsageError() error {
	return errors.New("usage: agentmem recall <search|get|context|adoption|verify> [options]")
}

func serveUsageError() error {
	return errors.New("usage: agentmem serve mcp --root <local-evidence-directory> --repo <portable-memory-directory> --agent <agent>")
}

func syncUsageError() error {
	return errors.New("usage: agentmem sync <bootstrap|verify|run|install-hooks|auto> [options]")
}

func syncAutoUsageError() error {
	return errors.New("usage: agentmem sync auto <enable|disable|status|run|recover> [options]")
}

func deriveUsageError() error {
	return errors.New("usage: agentmem derive <episodes|candidates> --root <local-evidence-directory> [options]")
}

func evalUsageError() error {
	return errors.New("usage: agentmem eval <corpus baseline|corpus freeze|corpus verify|attest|run|verify> [options]")
}

func captureUsageError() error {
	return errors.New("usage: agentmem capture <opencode --root <local-evidence-directory> --staging <non-Git-local-directory> [--binary opencode]|hook <codex|claude-code> --root <local-evidence-directory>|reconcile <codex|claude-code> --root <local-evidence-directory> --path <agent-source-path> [--full-reconcile]>")
}

func importUsageError() error {
	return errors.New("usage: agentmem import <codex|claude|claude-home|opencode-export|opencode-events> --root <local-evidence-directory> --path <source-file-or-directory>")
}

func injectUsageError() error {
	return errors.New("usage: agentmem inject <codex|claude-code|opencode> --root <local-evidence-directory> --repo <portable-memory-directory>")
}
