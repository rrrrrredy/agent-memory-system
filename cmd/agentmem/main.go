package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
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
	default:
		return usageError()
	}
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
	return errors.New("usage: agentmem <init|doctor|import|capture|derive|review|promote|rule-approval> [options]")
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

func deriveUsageError() error {
	return errors.New("usage: agentmem derive <episodes|candidates> --root <local-evidence-directory> [options]")
}

func captureUsageError() error {
	return errors.New("usage: agentmem capture opencode --root <local-evidence-directory> --staging <non-Git-local-directory> [--binary opencode]")
}

func importUsageError() error {
	return errors.New("usage: agentmem import <codex|claude|claude-home|opencode-export|opencode-events> --root <local-evidence-directory> --path <source-file-or-directory>")
}
