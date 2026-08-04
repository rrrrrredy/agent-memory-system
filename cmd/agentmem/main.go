package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/autosync"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/ruleapproval"
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
	case "capture":
		if len(args) < 2 {
			return captureUsageError()
		}
		return runCapture(args[1:])
	case "derive":
		if len(args) < 2 {
			return deriveUsageError()
		}
		return runDerive(args[1:])
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
	case "sync":
		if len(args) < 2 {
			return syncUsageError()
		}
		return runSync(args[1:])
	default:
		return usageError()
	}
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
	if args[0] != "opencode" {
		return captureUsageError()
	}
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
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return captureErr
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
	return errors.New("usage: agentmem <init|doctor|import|capture|derive|review|promote|rule-approval|portable|sync> [options]")
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

func syncUsageError() error {
	return errors.New("usage: agentmem sync <bootstrap|verify|run|install-hooks|auto> [options]")
}

func syncAutoUsageError() error {
	return errors.New("usage: agentmem sync auto <enable|disable|status|run|recover> [options]")
}

func deriveUsageError() error {
	return errors.New("usage: agentmem derive <episodes|candidates> --root <local-evidence-directory> [options]")
}

func captureUsageError() error {
	return errors.New("usage: agentmem capture opencode --root <local-evidence-directory> --staging <non-Git-local-directory> [--binary opencode]")
}

func importUsageError() error {
	return errors.New("usage: agentmem import <codex|claude|claude-home|opencode-export|opencode-events> --root <local-evidence-directory> --path <source-file-or-directory>")
}
