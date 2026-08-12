package workflow

import (
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const (
	OnboardResultSchema = "agent-memory-onboard-result/v1alpha1"
	StatusReportSchema  = "agent-memory-status/v1alpha1"
)

type OnboardResult struct {
	SchemaVersion string                   `json:"schema_version"`
	Agent         string                   `json:"agent"`
	EvidenceRoot  string                   `json:"evidence_root"`
	Repository    string                   `json:"repository,omitempty"`
	Import        adapterjsonl.Result      `json:"import"`
	Episodes      episodes.BuildResult     `json:"episodes"`
	Candidates    candidates.BuildResult   `json:"candidates"`
	Review        review.Summary           `json:"review"`
	Doctor        diagnostics.Report       `json:"doctor"`
	Sync          *gitsync.BootstrapResult `json:"sync,omitempty"`
	Ready         bool                     `json:"ready"`
	NextAction    string                   `json:"next_action"`
	Privacy       string                   `json:"privacy"`
}

type StatusReport struct {
	SchemaVersion       string                       `json:"schema_version"`
	EvidenceRoot        string                       `json:"evidence_root"`
	Repository          string                       `json:"repository,omitempty"`
	Doctor              diagnostics.Report           `json:"doctor"`
	CandidateGeneration string                       `json:"candidate_generation,omitempty"`
	Review              *review.Summary              `json:"review,omitempty"`
	PendingPromotions   int                          `json:"pending_promotions"`
	LocalRevisions      int                          `json:"local_revisions"`
	LocalActive         int                          `json:"local_active"`
	Portable            *portable.VerificationReport `json:"portable,omitempty"`
	WorkflowReady       bool                         `json:"workflow_ready"`
	NextAction          string                       `json:"next_action"`
	Issues              []string                     `json:"issues"`
	Privacy             string                       `json:"privacy"`
}

func NextAction(diagnosticsReady bool, gaps int, summary review.Summary, pendingPromotions, active int) string {
	switch {
	case gaps > 0:
		return "repair_capture_gaps"
	case !diagnosticsReady:
		return "repair_diagnostics"
	case summary.SchemaVersion == "":
		return "derive_current_candidates"
	case summary.Pending > 0:
		return "review_candidates"
	case pendingPromotions > 0:
		return "promote_validated_candidates"
	case active > 0:
		return "export_and_sync"
	default:
		return "capture_more_evidence"
	}
}
