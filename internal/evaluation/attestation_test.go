package evaluation

import (
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestRecordAttestationIsAppendOnlyAndMeasurementBound(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0).UTC()
	attestation := correctionAttestation(now, CorrectionMeasurement{
		SemanticKeySHA256:             repeatedSHA("a"),
		InitialCorrectionAttemptID:    "task-attempt-" + repeatedSHA("1"),
		AttemptIDs:                    []string{"task-attempt-" + repeatedSHA("2")},
		EligibleFollowupOpportunities: 1, RepeatedCorrections: 1,
		RepeatedCorrectionsAfterMemory: 1,
	})
	result, err := RecordAttestation(store, attestation, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordHash == "" || result.Reused {
		t.Fatalf("attestation was not appended: %+v", result)
	}
	reused, err := RecordAttestation(store, attestation, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.RecordHash != result.RecordHash || reused.EventID != result.EventID {
		t.Fatalf("identical attestation was not reused: %+v", reused)
	}

	var record ledger.Record
	if err := store.VisitRecords(func(candidate ledger.Record) error {
		if candidate.Event.EventID == result.EventID {
			record = candidate
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mismatch := attestation.evaluationCase()
	mismatch.Correction = &CorrectionMeasurement{
		SemanticKeySHA256:             repeatedSHA("a"),
		InitialCorrectionAttemptID:    "task-attempt-" + repeatedSHA("1"),
		AttemptIDs:                    []string{"task-attempt-" + repeatedSHA("2")},
		EligibleFollowupOpportunities: 1, RepeatedCorrections: 0,
		RepeatedCorrectionsAfterMemory: 0,
	}
	if err := validateAttestationRecord(store, mismatch, record); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("attestation accepted a different measurement: %v", err)
	}
}

func TestAttestationRequiresHumanForFalseMemoryAndCompactionLabels(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	attestation := EvaluationAttestation{
		SchemaVersion: EvaluationAttestationSchema, AttestationID: "label-1", CaseID: "memory-1",
		Category: CategoryFalseMemory, Agent: ledger.AgentCodex,
		Attestor: Attestor{Kind: "harness", ID: "scorer"}, AttestedAt: now,
		Reason: "Automated label without independent human review.",
		Memory: &MemoryMeasurement{MemoryID: "memory-1", Label: MemoryIncorrect, Active: true},
	}
	if err := validateAttestation(attestation); err == nil || !strings.Contains(err.Error(), "human attestor") {
		t.Fatalf("harness self-certified a false-memory label: %v", err)
	}
	attestation.AttestationID = "drift-1"
	attestation.CaseID = "drift-1"
	attestation.Category = CategoryCompactionDrift
	attestation.Memory = nil
	attestation.Compaction = &CompactionExpectation{
		CheckpointID: "checkpoint-1", Expected: DriftDetected}
	if err := validateAttestation(attestation); err == nil || !strings.Contains(err.Error(), "human attestor") {
		t.Fatalf("harness self-certified a compaction label: %v", err)
	}
}

func TestDecodeAttestationRejectsUnknownFields(t *testing.T) {
	input := `{"schema_version":"learning-evaluation-attestation/v1alpha1","unknown":true}`
	if _, err := DecodeAttestation(strings.NewReader(input)); err == nil {
		t.Fatal("unknown attestation field was accepted")
	}
}

func correctionAttestation(now time.Time, measurement CorrectionMeasurement) EvaluationAttestation {
	return EvaluationAttestation{
		SchemaVersion: EvaluationAttestationSchema, AttestationID: "correction-label-1",
		CaseID: "correction", Category: CategoryRepeatedCorrection, Agent: ledger.AgentCodex,
		Attestor: Attestor{Kind: "human", ID: "owner"}, AttestedAt: now,
		Reason:     "The follow-up opportunities and repeated correction were reviewed.",
		Correction: &measurement,
	}
}
