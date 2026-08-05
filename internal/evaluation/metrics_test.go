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
		QualityProfile: QualityProfileComponent, Privacy: "local_only",
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
			correctionCase("correction", 4, 1, 1),
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
		QualityProfile: QualityProfileComponent, Privacy: "local_only", Thresholds: EvaluationThresholds{MinimumCaptureCoverage: &minimum},
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

func TestCalculateSeparatesRawCaptureFromAccountedMissingSources(t *testing.T) {
	minimum := 1.0
	evaluationCase := captureCase("accounted-capture", 10, 8, 0, 2)
	evaluationCase.Capture.AccountedMissing = 2
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "accounted-capture",
		RunID: "run-1", CreatedAt: time.Unix(101, 0).UTC(), SystemVersion: "test-v1",
		QualityProfile: QualityProfileComponent, Privacy: "local_only", Thresholds: EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{evaluationCase},
	}
	report, err := Calculate(input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Capture.Coverage.Value == nil || *report.Capture.Coverage.Value != 0.8 ||
		report.Capture.AccountedCoverage == nil || report.Capture.AccountedCoverage.Value == nil ||
		*report.Capture.AccountedCoverage.Value != 1 || report.Capture.AccountedMissing != 2 {
		t.Fatalf("raw and accounted capture were conflated: %+v", report.Capture)
	}
	if len(report.Gates) != 1 || report.Gates[0].Status != "fail" {
		t.Fatalf("accounted missing sources incorrectly passed the raw capture gate: %+v", report.Gates)
	}
}

func TestCalculateRejectsAccountedMissingAboveMissingCount(t *testing.T) {
	evaluationCase := captureCase("invalid-accounted", 1, 0, 0, 1)
	evaluationCase.Capture.AccountedMissing = 2
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "invalid-accounted",
		RunID: "run-1", CreatedAt: time.Unix(102, 0).UTC(), SystemVersion: "test-v1",
		QualityProfile: QualityProfileComponent, Privacy: "local_only", Cases: []EvaluationCase{evaluationCase},
	}
	if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "capture counts") {
		t.Fatalf("invalid accounted missing count was accepted: %v", err)
	}
}

func TestCalculateKeepsUnattestedMemoryLabelNonReleaseReady(t *testing.T) {
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "invalid-memory",
		RunID: "run-1", CreatedAt: time.Unix(100, 0).UTC(), SystemVersion: "test-v1",
		QualityProfile: QualityProfileComponent, Privacy: "local_only",
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

func TestContinuousLearningProfileRequiresCompleteDiagnosticShape(t *testing.T) {
	makeInput := func() EvaluationInput {
		minimumCapture, maximumFalse, maximumUnknown := 0.9, 0.1, 0.1
		maximumCorrection, minimumPrecision, minimumRecall := 0.1, 0.9, 0.9
		maximumTokens, minimumScore, maximumCorrectionDelta := 200.0, 0.1, 0.0
		maximumHarmful, minimumSamples := 0.0, 1.0
		return EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "continuous-policy",
			RunID: "run-1", CreatedAt: time.Unix(103, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileContinuousLearning, Privacy: "local_only",
			Thresholds: EvaluationThresholds{
				MinimumCaptureCoverage: &minimumCapture, MaximumFalseMemoryRate: &maximumFalse,
				MaximumUnknownMemoryRate: &maximumUnknown, MaximumRepeatedCorrectionRate: &maximumCorrection,
				MinimumDriftPrecision: &minimumPrecision, MinimumDriftRecall: &minimumRecall,
				MaximumMeanRetrievalTokens: &maximumTokens, MinimumMeanOutcomeScoreDelta: &minimumScore,
				MaximumMeanCorrectionDelta: &maximumCorrectionDelta, MaximumHarmfulOutcomes: &maximumHarmful,
				MinimumCorrectionOpportunities: &minimumSamples, MinimumPairedOutcomePairs: &minimumSamples,
			},
			Cases: []EvaluationCase{
				captureCase("capture", 1, 1, 0, 0),
				memoryCase("memory", "memory-1", MemorySupported, "a"),
				correctionCase("correction", 1, 0, 0),
				compactionCase("drift", DriftPreserved, DriftPreserved, "b"),
				retrievalCase("retrieval", "retrieval-1", 50, 100, 1, true, OutcomeHelpful, "c"),
				pairedCase("pair", 0.5, 0.8, false, true, "d"),
			},
		}
	}

	t.Run("all gates remain measurement only", func(t *testing.T) {
		input := makeInput()
		input.Cases[3] = compactionCase("drift", DriftDetected, DriftDetected, "b")
		report, err := Calculate(input)
		if err != nil {
			t.Fatal(err)
		}
		for _, gate := range report.Gates {
			if gate.Status != "pass" {
				t.Fatalf("diagnostic gate %q did not pass: %+v", gate.Name, gate)
			}
		}
		if report.ReleaseReady || report.Authority != EvaluationAuthorityMeasurementOnly ||
			!containsIssue(report.Issues, ContinuousLearningEfficacyIssue) {
			t.Fatalf("continuous-learning diagnostics claimed efficacy: %+v", report)
		}
	})

	t.Run("missing category", func(t *testing.T) {
		input := makeInput()
		input.Cases = input.Cases[:len(input.Cases)-1]
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "requires category") {
			t.Fatalf("incomplete continuous-learning category set was accepted: %v", err)
		}
	})

	t.Run("missing threshold", func(t *testing.T) {
		input := makeInput()
		input.Thresholds.MinimumPairedOutcomePairs = nil
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "requires threshold") {
			t.Fatalf("incomplete continuous-learning threshold set was accepted: %v", err)
		}
	})

	t.Run("zero sample minimum", func(t *testing.T) {
		input := makeInput()
		zero := 0.0
		input.Thresholds.MinimumCorrectionOpportunities = &zero
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "positive evidence sample") {
			t.Fatalf("zero continuous-learning evidence minimum was accepted: %v", err)
		}
	})
}

func TestEvaluationRejectsRepeatedMeasuredSubjectsAcrossCases(t *testing.T) {
	t.Run("correction attempt", func(t *testing.T) {
		first := correctionCase("correction-1", 1, 0, 0)
		second := correctionCase("correction-2", 1, 0, 0)
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-correction",
			RunID: "run-1", CreatedAt: time.Unix(104, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate correction attempt was accepted: %v", err)
		}
	})

	t.Run("paired attempts", func(t *testing.T) {
		first := pairedCase("pair-1", 0.2, 0.8, false, true, "a")
		second := pairedCase("pair-2", 0.2, 0.8, false, true, "b")
		second.PairedOutcome.BaselineAttemptID = first.PairedOutcome.BaselineAttemptID
		second.PairedOutcome.TreatmentAttemptID = first.PairedOutcome.TreatmentAttemptID
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-pair",
			RunID: "run-1", CreatedAt: time.Unix(105, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate paired attempts were accepted: %v", err)
		}
	})

	t.Run("retrieval", func(t *testing.T) {
		first := retrievalCase("retrieval-1", "retrieval-shared", 10, 20, 1, true, OutcomeHelpful, "a")
		second := retrievalCase("retrieval-2", "retrieval-shared", 10, 20, 1, true, OutcomeHelpful, "b")
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-retrieval",
			RunID: "run-1", CreatedAt: time.Unix(106, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate retrieval was accepted: %v", err)
		}
	})

	t.Run("memory", func(t *testing.T) {
		first := memoryCase("memory-1", "memory-shared", MemorySupported, "a")
		second := memoryCase("memory-2", "memory-shared", MemorySupported, "b")
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-memory",
			RunID: "run-1", CreatedAt: time.Unix(107, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate memory was accepted: %v", err)
		}
	})

	t.Run("compaction checkpoint", func(t *testing.T) {
		first := compactionCase("drift-1", DriftPreserved, DriftPreserved, "a")
		second := compactionCase("drift-2", DriftPreserved, DriftPreserved, "b")
		second.Evidence[0] = first.Evidence[0]
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-compaction",
			RunID: "run-1", CreatedAt: time.Unix(108, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate compaction evidence was accepted: %v", err)
		}
	})

	t.Run("capture evidence", func(t *testing.T) {
		first := captureCase("capture-1", 1, 1, 0, 0)
		second := captureCase("capture-2", 1, 1, 0, 0)
		second.Evidence[0] = first.Evidence[0]
		input := EvaluationInput{
			SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "duplicate-capture",
			RunID: "run-1", CreatedAt: time.Unix(109, 0).UTC(), SystemVersion: "test-v1",
			QualityProfile: QualityProfileComponent, Privacy: "local_only",
			Cases: []EvaluationCase{first, second},
		}
		if _, err := Calculate(input); err == nil || !strings.Contains(err.Error(), "repeats measured subject") {
			t.Fatalf("duplicate capture evidence was accepted: %v", err)
		}
	})
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
	attemptIDs := make([]string, 0, opportunities)
	for index := 0; index < opportunities; index++ {
		attemptIDs = append(attemptIDs, "task-attempt-"+strings.Repeat(string(rune('a'+index)), 64))
	}
	return EvaluationCase{
		CaseID: id, Category: CategoryRepeatedCorrection, Agent: ledger.AgentCodex,
		Evidence: []EvidenceReference{{Kind: "ledger_event", ID: "event-" + id, SHA256: repeatedSHA("2")}},
		Correction: &CorrectionMeasurement{SemanticKeySHA256: repeatedSHA("3"), AttemptIDs: attemptIDs,
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
			BaselineAttemptID:  "task-attempt-" + sha256Hex([]byte("baseline:"+id)),
			TreatmentAttemptID: "task-attempt-" + sha256Hex([]byte("treatment:"+id)),
			Baseline: TrialMeasurement{Success: baselineSuccess, Score: baseline, Errors: 2,
				UserCorrections: 1, TotalTokens: 100},
			Treatment: TrialMeasurement{Success: treatmentSuccess, Score: treatment, Errors: 1,
				UserCorrections: 0, TotalTokens: 120}},
	}
}

func repeatedSHA(seed string) string {
	return strings.Repeat(seed, 64)
}
