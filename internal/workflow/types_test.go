package workflow

import (
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestNextActionPrioritizesCaptureAndDiagnosticRepair(t *testing.T) {
	pending := review.Summary{SchemaVersion: review.SummarySchemaVersion, Pending: 1}
	tests := []struct {
		name             string
		diagnosticsReady bool
		gaps             int
		summary          review.Summary
		want             string
	}{
		{name: "capture gap", diagnosticsReady: false, gaps: 1, summary: pending, want: "repair_capture_gaps"},
		{name: "diagnostic issue", diagnosticsReady: false, summary: pending, want: "repair_diagnostics"},
		{name: "missing derivation", diagnosticsReady: true, want: "derive_current_candidates"},
		{name: "review", diagnosticsReady: true, summary: pending, want: "review_candidates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NextAction(test.diagnosticsReady, test.gaps, test.summary, 0, 0); got != test.want {
				t.Fatalf("NextAction() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNextActionUsesCurrentPendingPromotionsNotGlobalActiveCount(t *testing.T) {
	summary := review.Summary{SchemaVersion: review.SummarySchemaVersion, Validated: 1}
	if got := NextAction(true, 0, summary, 1, 1); got != "promote_validated_candidates" {
		t.Fatalf("current validated candidate was hidden by an older active memory: %q", got)
	}
	if got := NextAction(true, 0, summary, 0, 1); got != "export_and_sync" {
		t.Fatalf("already promoted candidate did not advance to export: %q", got)
	}
}
