package schemas_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/workflow"
)

func TestWorkflowInstancesMatchPublishedSchemas(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	doctor := diagnostics.Run(context.Background(), store, diagnostics.Options{})
	if !doctor.Ready {
		t.Fatalf("fixture diagnostics are not ready: %+v", doctor.Issues)
	}
	hashA, hashB := strings.Repeat("a", 64), strings.Repeat("b", 64)
	imported := adapterjsonl.Result{SchemaVersion: adapterjsonl.ResultSchemaVersion,
		FilesExamined: 1, FilesChanged: 1, SourceSegments: 1, EventsAppended: 3,
		Kinds: map[string]int{"user_message": 1}}
	episodeBuild := episodes.BuildResult{SchemaVersion: episodes.BuildSchemaVersion,
		DerivationVersion: episodes.DerivationVersion, SourceRecords: 3,
		SourceLastRecordHash: hashA, GenerationPath: "episodes-v1alpha1-example",
		Episodes: 1, TimelineEntries: 3, TimelineSHA256: hashA, EpisodesSHA256: hashB}
	candidateBuild := candidates.BuildResult{SchemaVersion: candidates.BuildSchemaVersion,
		DerivationVersion:           candidates.DerivationVersion,
		SourceEpisodeGeneration:     "episodes-v1alpha1-example",
		SourceEpisodeManifestSHA256: hashA, SourceEpisodesSHA256: hashB,
		SourceEpisodes: 1, GenerationPath: "candidates-v1alpha1-example",
		Observations: 1, Candidates: 1, ReviewReady: 1, CandidatesSHA256: hashA}
	reviewSummary := review.Summary{SchemaVersion: review.SummarySchemaVersion,
		Generation: "candidates-v1alpha1-example", Total: 1, Pending: 1, Privacy: "local_only"}
	bootstrap := gitsync.BootstrapResult{SchemaVersion: gitsync.BootstrapSchemaVersion,
		Branch: gitsync.DefaultBranch, Head: strings.Repeat("c", 40), Privacy: "private_git"}
	onboard := workflow.OnboardResult{SchemaVersion: workflow.OnboardResultSchema,
		Agent: "codex", EvidenceRoot: "evidence", Repository: "portable", Import: imported,
		Episodes: episodeBuild, Candidates: candidateBuild, Review: reviewSummary, Doctor: doctor,
		Sync: &bootstrap, Ready: true, NextAction: "review_candidates", Privacy: "local_only"}
	status := workflow.StatusReport{SchemaVersion: workflow.StatusReportSchema,
		EvidenceRoot: "evidence", Doctor: doctor, CandidateGeneration: reviewSummary.Generation,
		Review: &reviewSummary, WorkflowReady: true, NextAction: "review_candidates",
		Issues: []string{}, Privacy: "local_only"}

	for _, instance := range []struct {
		schema string
		value  any
	}{
		{"candidate-review-summary.schema.json", reviewSummary},
		{"git-sync-bootstrap-result.schema.json", bootstrap},
		{"agent-memory-onboard-result.schema.json", onboard},
		{"agent-memory-status.schema.json", status},
	} {
		t.Run(instance.schema, func(t *testing.T) {
			validatePublishedInstance(t, instance.schema, instance.value)
		})
	}

	invalid := map[string]any{
		"schema_version": workflow.StatusReportSchema, "evidence_root": "evidence",
		"doctor": doctor, "local_revisions": 0, "local_active": 0,
		"workflow_ready": true, "next_action": "auto_promote", "issues": []string{},
		"privacy": "local_only",
	}
	rejectPublishedInstance(t, "agent-memory-status.schema.json", invalid)
}
