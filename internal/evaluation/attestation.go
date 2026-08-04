package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeAttestation(reader io.Reader) (EvaluationAttestation, error) {
	if reader == nil {
		return EvaluationAttestation{}, errors.New("evaluation attestation reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var attestation EvaluationAttestation
	if err := decoder.Decode(&attestation); err != nil {
		return EvaluationAttestation{}, fmt.Errorf("decode evaluation attestation: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return EvaluationAttestation{}, err
	}
	if err := validateAttestation(attestation); err != nil {
		return EvaluationAttestation{}, err
	}
	return attestation, nil
}

func RecordAttestation(store *ledger.Store, attestation EvaluationAttestation,
	now func() time.Time) (result AttestationResult, returnedErr error) {
	result = AttestationResult{
		SchemaVersion: AttestationResultSchema,
		AttestationID: attestation.AttestationID,
		Privacy:       "local_only",
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := validateAttestation(attestation); err != nil {
		return result, err
	}
	if now == nil {
		now = time.Now
	}
	data, err := marshalIndented(attestation)
	if err != nil {
		return result, fmt.Errorf("encode evaluation attestation: %w", err)
	}
	eventID, err := expectedAttestationEventID(attestation)
	if err != nil {
		return result, err
	}
	result.EventID = eventID
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          ledger.KindEvaluationAttestation,
		ObservedAt:    attestation.AttestedAt.UTC(),
		RecordedAt:    now().UTC(),
		Source: ledger.Source{
			Agent:          attestation.Agent,
			Adapter:        evaluationAdapterName,
			AdapterVersion: evaluationAdapterVersion,
			DeviceID:       store.DeviceID(),
			OS:             runtime.GOOS,
			ThreadID:       attestation.CaseID,
			SourceEventID:  attestation.AttestationID,
			SourceCursor:   "attestation:" + attestation.AttestationID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}

	var existing *ledger.Record
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		if record.Event.EventID == eventID {
			copy := record
			existing = &copy
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan evaluation attestations: %w", err)
	}
	defer func() {
		if closeErr := appender.Close(); returnedErr == nil && closeErr != nil {
			returnedErr = closeErr
		}
	}()
	if existing != nil {
		if err := validateAttestationRecord(store, attestation.evaluationCase(), *existing); err != nil {
			return result, errors.New("evaluation attestation event id collision")
		}
		result.RecordHash = existing.RecordHash
		result.Reused = true
		if err := appender.Close(); err != nil {
			return result, err
		}
		return result, nil
	}
	records, err := appender.AppendBatch([]ledger.Event{event})
	if err != nil {
		return result, fmt.Errorf("append evaluation attestation: %w", err)
	}
	if len(records) != 1 {
		return result, errors.New("append evaluation attestation returned no record")
	}
	result.RecordHash = records[0].RecordHash
	if err := appender.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func validateAttestation(attestation EvaluationAttestation) error {
	if attestation.SchemaVersion != EvaluationAttestationSchema {
		return fmt.Errorf("unsupported evaluation attestation schema %q", attestation.SchemaVersion)
	}
	if !safeIdentifier(attestation.AttestationID) || !safeIdentifier(attestation.CaseID) {
		return errors.New("attestation_id and case_id must be safe identifiers")
	}
	if !validAgent(attestation.Agent) || attestation.Agent == ledger.AgentUnknown {
		return errors.New("evaluation attestation must identify a supported agent")
	}
	if attestation.Attestor.Kind != "human" && attestation.Attestor.Kind != "harness" {
		return errors.New("evaluation attestor must be human or harness")
	}
	if strings.TrimSpace(attestation.Attestor.ID) == "" ||
		strings.TrimSpace(attestation.Reason) == "" || attestation.AttestedAt.IsZero() {
		return errors.New("evaluation attestation requires attestor, time, and reason")
	}
	if err := validateCaseMeasurement(attestation.evaluationCase()); err != nil {
		return fmt.Errorf("evaluation attestation measurement: %w", err)
	}
	if (attestation.Category == CategoryFalseMemory ||
		attestation.Category == CategoryCompactionDrift) && attestation.Attestor.Kind != "human" {
		return errors.New("false-memory and compaction-drift labels require a human attestor")
	}
	return nil
}

func (attestation EvaluationAttestation) evaluationCase() EvaluationCase {
	return EvaluationCase{
		CaseID: attestation.CaseID, Category: attestation.Category, Agent: attestation.Agent,
		Capture: attestation.Capture, Memory: attestation.Memory, Correction: attestation.Correction,
		Compaction: attestation.Compaction, Retrieval: attestation.Retrieval,
		PairedOutcome: attestation.PairedOutcome,
	}
}

func expectedAttestationEventID(attestation EvaluationAttestation) (string, error) {
	data, err := json.Marshal(attestation)
	if err != nil {
		return "", fmt.Errorf("encode evaluation attestation identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return adapterjsonl.DeterministicID("evaluation-attestation", attestation.AttestationID,
		hex.EncodeToString(digest[:])), nil
}

func validateAttestationRecord(store *ledger.Store, evaluationCase EvaluationCase,
	record ledger.Record) error {
	if record.Event.Kind != ledger.KindEvaluationAttestation {
		return errors.New("referenced event is not an evaluation attestation")
	}
	data, err := eventPayload(store, record.Event)
	if err != nil {
		return fmt.Errorf("read evaluation attestation: %w", err)
	}
	attestation, err := DecodeAttestation(bytes.NewReader(data))
	if err != nil {
		return err
	}
	expectedID, err := expectedAttestationEventID(attestation)
	if err != nil || record.Event.EventID != expectedID {
		return errors.New("evaluation attestation event identity is invalid")
	}
	if record.Event.Source.Agent != attestation.Agent ||
		record.Event.Source.Adapter != evaluationAdapterName ||
		record.Event.Source.AdapterVersion != evaluationAdapterVersion ||
		record.Event.Source.ThreadID != attestation.CaseID ||
		record.Event.Source.SourceEventID != attestation.AttestationID ||
		!record.Event.ObservedAt.UTC().Equal(attestation.AttestedAt.UTC()) ||
		record.Event.Completeness.Status != ledger.CompletenessComplete ||
		record.Event.Privacy.Classification != "local_only" {
		return errors.New("evaluation attestation event envelope is invalid")
	}
	if evaluationCase.CaseID != "" {
		if attestation.CaseID != evaluationCase.CaseID ||
			attestation.Category != evaluationCase.Category || attestation.Agent != evaluationCase.Agent {
			return errors.New("evaluation attestation belongs to another case")
		}
		left, leftErr := measurementSHA256(attestation.evaluationCase())
		right, rightErr := measurementSHA256(evaluationCase)
		if leftErr != nil || rightErr != nil || left != right {
			return errors.New("evaluation attestation measurement does not match the case")
		}
	}
	return nil
}

func measurementSHA256(evaluationCase EvaluationCase) (string, error) {
	binding := struct {
		Category      CaseCategory              `json:"category"`
		Capture       *CaptureMeasurement       `json:"capture,omitempty"`
		Memory        *MemoryMeasurement        `json:"memory,omitempty"`
		Correction    *CorrectionMeasurement    `json:"correction,omitempty"`
		Compaction    *CompactionMeasurement    `json:"compaction,omitempty"`
		Retrieval     *RetrievalMeasurement     `json:"retrieval,omitempty"`
		PairedOutcome *PairedOutcomeMeasurement `json:"paired_outcome,omitempty"`
	}{
		Category: evaluationCase.Category, Capture: evaluationCase.Capture,
		Memory: evaluationCase.Memory, Correction: evaluationCase.Correction,
		Compaction: evaluationCase.Compaction, Retrieval: evaluationCase.Retrieval,
		PairedOutcome: evaluationCase.PairedOutcome,
	}
	data, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("encode evaluation measurement: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
