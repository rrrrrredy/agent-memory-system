package study

import (
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func externalPlanFixture() ExternalPlan {
	return ExternalPlan{
		SchemaVersion: ExternalPlanSchema,
		StudyID:       "permission-safe-planning-2026-09",
		Title:         "Persistent evolution knowledge ablation",
		CreatedAt:     time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC),
		Scenario:      "Permission-safe local repository action planning",
		Hypotheses: []ExternalHypothesis{{
			ID: "H1", Statement: "Persistent Wiki improves held-out task quality.", Direction: "greater",
		}},
		Conditions: []ExternalCondition{
			{ConditionID: "no_wiki", OptimizerContext: "current_trace_only"},
			{ConditionID: "flat_history", OptimizerContext: "chronological_history"},
			{ConditionID: "persistent_wiki", OptimizerContext: "pattern_registry"},
		},
		Replicates:             3,
		IterationsPerReplicate: 3,
		SourceModel:            "gpt-5.4-mini",
		TransferModels:         []string{"gpt-5.6-luna"},
		Datasets: ExternalDatasets{
			Failure:    ExternalDataset{Path: "datasets/failure.json", SHA256: "sha256:" + strings.Repeat("1", 64), Cases: 6},
			Protection: ExternalDataset{Path: "datasets/protection.json", SHA256: "sha256:" + strings.Repeat("2", 64), Cases: 6},
			Transfer:   ExternalDataset{Path: "datasets/transfer.json", SHA256: "sha256:" + strings.Repeat("3", 64), Cases: 6},
		},
		Metrics:    []string{"task_quality", "tool_calls", "input_tokens", "output_tokens", "wall_time_ms", "rule_lines", "rule_words", "rollback_count", "cross_model_transfer"},
		Assignment: ExternalAssignment{Method: "fixed_seed_round_robin", Seed: 260901},
		CandidateGate: ExternalCandidateGate{
			Improve: "activate", Tie: "hold", Degrade: "rollback", SecurityFailure: "block",
		},
		ClaimBoundary: "Descriptive evidence for one deterministic scenario and two model versions.",
		Privacy:       PrivacyLocalOnly,
	}
}

func TestExternalPlanRegistrationIsStrictIdempotentAndAppendOnly(t *testing.T) {
	store, err := ledger.Init(t.TempDir() + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	plan := externalPlanFixture()
	data, err := canonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExternalPlan(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := RegisterExternal(store, decoded)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RegisterExternal(store, decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Written || second.Written || first.Event != second.Event || first.FileSHA256 != second.FileSHA256 {
		t.Fatalf("external plan registration is not idempotent: first=%+v second=%+v", first, second)
	}
	changed := decoded
	changed.Title = "Changed after registration"
	if _, err := RegisterExternal(store, changed); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("changed plan reused a frozen study id: %v", err)
	}
	if report := store.Verify(); len(report.Issues) != 0 || report.RecordsChecked != 1 {
		t.Fatalf("external plan damaged the evidence ledger: %+v", report)
	}
}

func TestExternalPlanRejectsUnknownFieldsAndWeakDesigns(t *testing.T) {
	plan := externalPlanFixture()
	data, err := canonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(data), "{", `{"unknown":true,`, 1)
	if _, err := DecodeExternalPlan(strings.NewReader(withUnknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field was accepted: %v", err)
	}
	plan.Replicates = 2
	if err := validateExternalPlan(plan); err == nil || !strings.Contains(err.Error(), "envelope") {
		t.Fatalf("two-replicate design was accepted: %v", err)
	}
	plan = externalPlanFixture()
	plan.Datasets.Transfer.Path = "../unfrozen.json"
	if err := validateExternalPlan(plan); err == nil || !strings.Contains(err.Error(), "dataset") {
		t.Fatalf("traversal dataset binding was accepted: %v", err)
	}
}
