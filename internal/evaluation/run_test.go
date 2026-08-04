package evaluation

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestRunBindsEvidencePersistsAndVerifies(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	record, err := store.Append(testEvidenceEvent("source-1", ledger.KindSourceSnapshot,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	minimum := 1.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "verified-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		Thresholds: EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{{
			CaseID: "capture", Category: CategoryCaptureCoverage, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{Kind: "ledger_event", ID: record.Event.EventID, SHA256: record.RecordHash}},
			Capture:  &CaptureMeasurement{Unit: CaptureUnitEvidenceEvents, Expected: 1, Complete: 1},
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.ReleaseReady || result.Report.EvidenceReferencesChecked != 1 || result.Report.InputBlob == nil {
		t.Fatalf("verified run was not release-ready: %+v", result.Report)
	}
	if _, err := os.Stat(result.ReportPath); err != nil {
		t.Fatalf("report was not persisted: %v", err)
	}
	verification := VerifyRun(store, input.SuiteID, input.RunID)
	if len(verification.Issues) != 0 {
		t.Fatalf("verification failed: %+v", verification)
	}
	reused, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.ReportEventID != result.ReportEventID {
		t.Fatalf("identical run was not reused: %+v", reused)
	}
}

func TestRunRejectsMeasurementThatDoesNotMatchEvidence(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	record, err := store.Append(testEvidenceEvent("source-1", ledger.KindSourceSnapshot,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	minimum := 0.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "mismatch-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		Thresholds: EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{{
			CaseID: "capture", Category: CategoryCaptureCoverage, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{Kind: "ledger_event", ID: record.Event.EventID, SHA256: record.RecordHash}},
			Capture:  &CaptureMeasurement{Unit: CaptureUnitEvidenceEvents, Expected: 1, Partial: 1},
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.ReleaseReady || len(result.Report.Issues) == 0 ||
		!strings.Contains(result.Report.Issues[0], "capture counts") {
		t.Fatalf("fabricated measurement was accepted: %+v", result.Report)
	}
}

func TestRunRequiresARecordedAttestationForInterpretedMeasurements(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(250, 0).UTC()
	userRecord, err := store.Append(testEvidenceEvent("user-correction", ledger.KindUserMessage,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	measurement := CorrectionMeasurement{
		SemanticKeySHA256: repeatedSHA("a"), EligibleFollowupOpportunities: 2,
		RepeatedCorrections: 1,
	}
	attestation := correctionAttestation(now.Add(time.Second), measurement)
	attested, err := RecordAttestation(store, attestation,
		func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	maximum := 1.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "attested-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		Thresholds: EvaluationThresholds{MaximumRepeatedCorrectionRate: &maximum},
		Cases: []EvaluationCase{{
			CaseID: "correction", Category: CategoryRepeatedCorrection, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{
				{Kind: "ledger_event", ID: userRecord.Event.EventID, SHA256: userRecord.RecordHash},
				{Kind: "ledger_event", ID: attested.EventID, SHA256: attested.RecordHash},
			},
			Correction: &measurement,
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.ReleaseReady || result.Report.AttestedReferences != 1 ||
		result.Report.EvidenceReferencesChecked != 2 {
		t.Fatalf("recorded attestation did not support the run: %+v", result.Report)
	}

	input.RunID = "run-2"
	input.Cases[0].Correction = &CorrectionMeasurement{
		SemanticKeySHA256: repeatedSHA("a"), EligibleFollowupOpportunities: 2,
		RepeatedCorrections: 2,
	}
	mismatched, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(4 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if mismatched.Report.ReleaseReady ||
		!containsIssue(mismatched.Report.Issues, "attestation measurement does not match") {
		t.Fatalf("mismatched attestation was accepted: %+v", mismatched.Report)
	}
}

func containsIssue(issues []string, fragment string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, fragment) {
			return true
		}
	}
	return false
}

func TestSummarizedOutcomeCannotHideHarmAsUnknown(t *testing.T) {
	if got := summarizedOutcome(map[OutcomeLabel]struct{}{
		OutcomeHelpful: {}, OutcomeHarmful: {},
	}); got != OutcomeHarmful {
		t.Fatalf("summarized outcome = %q, want harmful", got)
	}
	if got := summarizedOutcome(map[OutcomeLabel]struct{}{}); got != OutcomeUnknown {
		t.Fatalf("empty outcome = %q, want unknown", got)
	}
}

func testEvidenceEvent(id string, kind ledger.EventKind, status ledger.CompletenessStatus,
	now time.Time) ledger.Event {
	payload := ledger.InlinePayload("text", "text/plain", "evidence")
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
		ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "test-adapter",
			AdapterVersion: "test-adapter/v1", DeviceID: "device-test", ThreadID: "thread-test"},
		Payload: &payload, Completeness: ledger.Completeness{Status: status},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
}
