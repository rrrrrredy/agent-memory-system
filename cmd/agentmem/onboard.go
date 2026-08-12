package main

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/workflow"
)

func runOnboard(args []string) error {
	if len(args) == 0 || args[0] != "codex" {
		return onboardUsageError()
	}
	flags := flag.NewFlagSet("onboard codex", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	path := flags.String("path", "", "Codex rollout file or directory (required)")
	repository := flags.String("repo", "", "optional separate portable memory repository")
	full := flags.Bool("full-reconcile", false, "re-read all source bytes and append only changed events")
	shards := flags.Int("shards", 64, "power-of-two derivation shard count (1-256)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || *path == "" {
		return errors.New("onboard codex requires --root and --path")
	}
	store, err := ledger.Init(*root)
	if err != nil {
		return err
	}
	var syncResult *gitsync.BootstrapResult
	if *repository != "" {
		bootstrapped, bootstrapErr := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
			RepositoryRoot: *repository, RemoteName: gitsync.DefaultRemote,
		})
		if bootstrapErr != nil {
			return bootstrapErr
		}
		syncResult = &bootstrapped
	}
	imported, err := codex.ImportPath(store, *path, codex.Options{FullReconcile: *full})
	if err != nil {
		return err
	}
	episodeResult, err := episodes.Build(store, episodes.BuildOptions{ShardCount: *shards})
	if err != nil {
		return err
	}
	candidateResult, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeResult.GenerationPath, ShardCount: *shards,
	})
	if err != nil {
		return err
	}
	reviewSummary, err := review.Summarize(store, candidateResult.GenerationPath)
	if err != nil {
		return err
	}
	doctor := diagnostics.Run(context.Background(), store, diagnostics.Options{
		Repository: *repository, RequireRepository: *repository != "",
	})
	result := workflow.OnboardResult{SchemaVersion: workflow.OnboardResultSchema, Agent: "codex",
		EvidenceRoot: store.Root(), Repository: cleanAbsolute(*repository), Import: imported,
		Episodes: episodeResult, Candidates: candidateResult, Review: reviewSummary, Doctor: doctor, Sync: syncResult,
		Ready: imported.GapsAppended == 0 && doctor.Ready, Privacy: "local_only"}
	result.NextAction = workflow.NextAction(result.Ready, imported.GapsAppended, reviewSummary, 0, 0)
	if err := encodeIndented(result); err != nil {
		return err
	}
	if !result.Ready {
		return errors.New("onboarding completed with evidence gaps or diagnostic issues; inspect the structured result")
	}
	return nil
}

func runStatus(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	root := flags.String("root", "", "local evidence root (required)")
	repository := flags.String("repo", "", "optional portable memory repository")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("status requires --root")
	}
	store, err := ledger.Open(*root)
	if err != nil {
		return err
	}
	doctor := diagnostics.Run(context.Background(), store, diagnostics.Options{
		Repository: *repository, RequireRepository: *repository != "",
	})
	result := workflow.StatusReport{SchemaVersion: workflow.StatusReportSchema, EvidenceRoot: store.Root(),
		Repository: cleanAbsolute(*repository), Doctor: doctor, Issues: []string{}, Privacy: "local_only"}
	generation, generationErr := candidates.OpenCurrentGeneration(store)
	if generationErr != nil {
		result.Issues = append(result.Issues, generationErr.Error())
	} else {
		result.CandidateGeneration = generation.Name
		summary, summaryErr := review.Summarize(store, generation.Name)
		if summaryErr != nil {
			result.Issues = append(result.Issues, summaryErr.Error())
		} else {
			result.Review = &summary
			pending, pendingErr := currentPendingPromotions(store, generation)
			if pendingErr != nil {
				result.Issues = append(result.Issues, pendingErr.Error())
			} else {
				result.PendingPromotions = pending
			}
		}
	}
	revisions, active, populationErr := portable.LoadLocalPopulation(store)
	if populationErr != nil {
		result.Issues = append(result.Issues, populationErr.Error())
	} else {
		result.LocalRevisions, result.LocalActive = len(revisions), len(active)
	}
	if *repository != "" {
		report := portable.VerifyRepository(*repository)
		result.Portable = &report
	}
	result.WorkflowReady = doctor.Ready && len(result.Issues) == 0
	summary := review.Summary{}
	if result.Review != nil {
		summary = *result.Review
	}
	result.NextAction = workflow.NextAction(doctor.Ready, 0, summary, result.PendingPromotions, result.LocalActive)
	if err := encodeIndented(result); err != nil {
		return err
	}
	if !doctor.Ready {
		return errors.New("agent memory diagnostics are not ready")
	}
	return nil
}

func cleanAbsolute(value string) string {
	if value == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return filepath.Clean(absolute)
}

func currentPendingPromotions(store *ledger.Store, generation candidates.Generation) (int, error) {
	items, err := generation.All()
	if err != nil {
		return 0, err
	}
	validated := make([]candidates.Candidate, 0, len(items))
	for _, candidate := range items {
		status, statusErr := review.GetStatus(store, generation.Name, candidate.CandidateID)
		if statusErr != nil {
			return 0, statusErr
		}
		if status.ReviewStatus == review.StatusValidated {
			validated = append(validated, candidate)
		}
	}
	histories, err := promotion.ListHistories(store)
	if err != nil {
		return 0, err
	}
	return unpromotedValidatedCount(generation.Name, validated, histories), nil
}

func unpromotedValidatedCount(generation string, validated []candidates.Candidate, histories []promotion.History) int {
	promoted := map[string]struct{}{}
	for _, history := range histories {
		for _, revision := range history.Revisions {
			if revision.Source == nil {
				continue
			}
			key := strings.Join([]string{revision.Source.CandidateGeneration, revision.Source.CandidateID,
				revision.Source.CandidateContentSHA256}, "\x00")
			promoted[key] = struct{}{}
		}
	}
	pending := 0
	for _, candidate := range validated {
		key := strings.Join([]string{generation, candidate.CandidateID, candidate.ContentSHA256}, "\x00")
		if _, exists := promoted[key]; !exists {
			pending++
		}
	}
	return pending
}

func resolveCandidateGeneration(store *ledger.Store, supplied string) (candidates.Generation, error) {
	if supplied != "" {
		return candidates.OpenGeneration(store, supplied)
	}
	return candidates.OpenCurrentGeneration(store)
}
