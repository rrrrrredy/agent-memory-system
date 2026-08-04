package evaluation

import (
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestCalculateMeasuresAllQualitySignalsWithoutSelfCertifying(t *testing.T) {
	minCapture, maxFalse, maxUnknown := 0.6, 0.6, 0.1
	maxCorrection, minPrecision, minRecall := 0.3, 0.5, 0.5
	maxTokens, minScore, maxHarmful := 200.0, 0.04, 0.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "quality-regression",
		RunID: "run-1", CreatedAt: time.Unix(100, 0).UTC(), SystemVersion: "test-v1",
		Privacy: "local_only",
		Thresholds: EvaluationThresholds{
			MinimumCaptureCoverage: &minCapture, MaximumFalseMemoryRate: &maxFalse,
			MaximumUnknownMemoryRate: &maxUnknown, MaximumRepeatedCorrectionRate: &maxCorrection,
			MinimumDriftPrecision: &minPrecision, MinimumDriftRecall: &minRecall,
			MaximumMeanRetrievalTokens: &maxTokens, MinimumMeanOutcomeScoreDelta: &minScore,
			MaximumHarmfulOutcomes: &maxHarmful,
		},
		Cases: []EvaluationCase{
			captureCase("capture", 3, 2, 1, 0),
			memoryCase("memory-false", "memory-1", MemoryIncorrect, "a"),
			memoryCase("memory-supported", "memory-2", MemorySupported, "b"),
			correctionCase("correction", 4, 2, 1),
			compactionCase("drift-tp", DriftDetected, DriftDetected, "1"),
			compactionCase("drift-fp", DriftPreserved, DriftDetected, "2"),
			compactionCase("drift-tn", DriftPreserved, DriftPreserved, "3"),
			compactionCase("drift-fn", DriftDetected, DriftPreserved, "4"),
			retrievalCase("retrieval-helpful", "retrieval-1", 100, 200, 1, true, OutcomeHelpful, "5"),
			retrievalCase("retrieval-empty", "retrieval-2", 300, 400, 0, false, OutcomeUnknown, "6"),
			pairedCase("pair-win", 0.5, 0.7, true, true, "7"),
			pairedCase("pair-loss", 0.8, 0.7, true, false, "8"),
		},
	}
	report, err := Calculate(input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Capture.Coverage.Value == nil || *report.Capture.Coverage.Value != 0.666667 {
		t.Fatalf("unexpected capture coverage: %+v", report.Capture.Coverage)
	}
	if report.FalseMemory.FalseRate.Value == nil || *report.FalseMemory.FalseRate.Value != 0.5 {
		t.Fatalf("unexpected false-memory rate: %+v", report.FalseMemory)
	}
	if report.Corrections.RepeatedCorrectionRate.Value == nil ||
		*report.Corrections.RepeatedCorrectionRate.Value != 0.25 {
		t.Fatalf("unexpected correction rate: %+v", report.Corrections)
	}
	if report.CompactionDrift.Precision.Value == nil || *report.CompactionDrift.Precision.Value != 0.5 ||
		report.CompactionDrift.Recall.Value == nil || *report.CompactionDrift.Recall.Value != 0.5 {
		t.Fatalf("unexpected drift metrics: %+v", report.CompactionDrift)
	}
	if report.Retrieval.MeanTokens == nil || *report.Retrieval.MeanTokens != 200 ||
		report.Retrieval.P95Tokens == nil || *report.Retrieval.P95Tokens != 300 ||
		report.Retrieval.HelpfulPerKToken == nil || *report.Retrieval.HelpfulPerKToken != 2.5 {
		t.Fatalf("unexpected retrieval metrics: %+v", report.Retrieval)
	}
	if report.Outcomes.MeanScoreDelta == nil || *report.Outcomes.MeanScoreDelta != 0.05 ||
		report.Outcomes.Wins != 1 || report.Outcomes.Losses != 1 {
		t.Fatalf("unexpected outcome metrics: %+v", report.Outcomes)
	}
	for _, gate := range report.Gates {
		if gate.Status != "pass" {
			t.Fatalf("gate should pass: %+v", gate)
		}
	}
	if report.ReleaseReady {
		t.Fatal("pure metric calculation must not self-certify a release")
	}
	if !validateReportHash(report) {
		t.Fatal("report hash does not validate")
	}
}

func TestCalculateDoesNotTreatEmptyDenominatorAsPass(t *testing.T) {
	minimum := 0.5
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "empty-denominator",
		RunID: "run-1", CreatedAt: time.Unix(100, 0).UTC(), SystemVersion: "test-v1",
		Privacy: "local_only", Thresholds: EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{captureCase("capture", 0, 0, 0, 0)},
	}
	report, err := Calculate(input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Capture.Coverage.Status != "not_evaluable" || report.Capture.Coverage.Value != nil {
		t.Fatalf("empty denominator was not explicit: %+v", report.Capture.Coverage)
	}
	if len(report.Gates) != 1 || report.Gates[0].Status != "not_evaluable" || report.ReleaseReady {
		t.Fatalf("vacuous gate unexpectedly passed: %+v", report.Gates)
	}
}

func TestCalculateKeepsUnattestedMemoryLabelNonReleaseReady(t *testing.T) {
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "invalid-memory",
		RunID: "run-1", CreatedAt: time.Unix(100, 0).UTC(), SystemVersion: "test-v1",
		Privacy: "local_only",
		Cases: []EvaluationCase{{
			CaseID: "memory", Category: CategoryFalseMemory, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{Kind: "portable_revision", ID: "revision-1", SHA256: repeatedSHA("a")}},
			Memory:   &MemoryMeasurement{MemoryID: "memory-1", Label: MemoryIncorrect, Active: true},
		}},
	}
	report, err := Calculate(input)
	if err != nil {
		t.Fatal(err)
	}
	if report.AttestedReferences != 0 || report.ReleaseReady {
		t.Fatalf("metric calculation self-certified an unattested label: %+v", report)
	}
}

func captureCase(id string, expected, complete, partial, missing int) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryCaptureCoverage, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{{Kind: "ledger_event", ID: "event-" + id, SHA256: repeatedSHA("1")}},
		Capture: &CaptureMeasurement{Unit: CaptureUnitEvidenceEvents,
			Expected: expected, Complete: complete, Partial: partial, Missing: missing},
	}
}

func memoryCase(id, memoryID string, label MemoryLabel, seed string) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryFalseMemory, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{
			{Kind: "portable_revision", ID: "revision-" + id, SHA256: repeatedSHA(seed)},
		},
		Memory: &MemoryMeasurement{MemoryID: memoryID, Label: label, Active: true},
	}
}

func correctionCase(id string, opportunities, repeated, afterMemory int) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryRepeatedCorrection, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{{Kind: "ledger_event", ID: "event-" + id, SHA256: repeatedSHA("2")}},
		Correction: &CorrectionMeasurement{SemanticKeySHA256: repeatedSHA("3"),
			EligibleFollowupOpportunities: opportunities, RepeatedCorrections: repeated,
			RepeatedCorrectionsAfterMemory: afterMemory},
	}
}

func compactionCase(id string, expected, observed DriftLabel, seed string) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryCompactionDrift, Agent: ledger.AgentCodex,
		Evidence:   []EvidenceReference{{Kind: "ledger_event", ID: "event-" + id, SHA256: repeatedSHA(seed)}},
		Compaction: &CompactionMeasurement{CheckpointID: "checkpoint-" + id, Expected: expected, Observed: observed},
	}
}

func retrievalCase(id, retrievalID string, tokens, bytes, selected int, adopted bool,
	outcome OutcomeLabel, seed string) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryRetrievalCost, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{{Kind: "ledger_event", ID: retrievalID, SHA256: repeatedSHA(seed)}},
		Retrieval: &RetrievalMeasurement{RetrievalID: retrievalID, EstimatedTokens: tokens,
			UTF8Bytes: bytes, SelectedItems: selected, Adopted: adopted, Outcome: outcome},
	}
}

func pairedCase(id string, baseline, treatment float64, baselineSuccess, treatmentSuccess bool,
	seed string) EvaluationCase {
	return EvaluationCase{
		CaseID: id, Category: CategoryPairedOutcome, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{{Kind: "ledger_event", ID: "event-" + id, SHA256: repeatedSHA(seed)}},
		PairedOutcome: &PairedOutcomeMeasurement{PairID: id,
			Baseline: TrialMeasurement{Success: baselineSuccess, Score: baseline, Errors: 2,
				UserCorrections: 1, TotalTokens: 100},
			Treatment: TrialMeasurement{Success: treatmentSuccess, Score: treatment, Errors: 1,
				UserCorrections: 0, TotalTokens: 120}},
	}
}

func repeatedSHA(seed string) string {
	return strings.Repeat(seed, 64)
}
