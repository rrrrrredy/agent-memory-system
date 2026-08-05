package agentassessment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	maximumSubmissionBytes = 16 << 20
	maximumAssessmentBytes = 32 << 20
)

var (
	candidateJudgments = stringSetOf(
		"supported_reusable", "supported_context_specific", "unsupported_or_incorrect",
		"possible_duplicate_or_conflict", "unsafe_or_untrusted", "insufficient_evidence",
	)
	candidateReasonCodes = stringSetOf(
		"direct_user_support", "repeated_user_support", "user_correction_support",
		"explicit_remember_support", "contradicting_evidence", "no_direct_support",
		"context_dependent", "duplicate_content", "conflicting_content",
		"untrusted_instruction", "evidence_gap",
	)
	compactionJudgments   = stringSetOf("preserved", "drift", "insufficient_evidence")
	compactionReasonCodes = stringSetOf(
		"representation_preserves_statement", "representation_omits_statement",
		"post_compaction_correction", "missing_representation", "evidence_gap",
	)
)

// ImportExternalAssessment validates an externally produced Agent submission
// against freshly regenerated local bindings and stores only a provisional
// derived artifact. External execution claims remain unverified. This function
// never appends evidence or calls review or promotion APIs.
func ImportExternalAssessment(
	store *ledger.Store, projectionID string, reader io.Reader, options AssessmentOptions,
) (AssessmentResult, error) {
	result := AssessmentResult{
		SchemaVersion: AssessmentResultSchema, Authority: "provisional_only", ArtifactStorage: "local_only",
	}
	projection, projectionBytes, payload, payloadBytes, err := loadVerifiedProjection(store, projectionID)
	if err != nil {
		return result, err
	}
	submission, submissionBytes, err := decodeSubmission(reader)
	if err != nil {
		return result, err
	}
	if err := validateSubmission(projection, submission); err != nil {
		return result, err
	}
	assessor, harness, assessedAt, err := validateAssessmentOptions(options, submissionBytes)
	if err != nil {
		return result, err
	}
	assessment := Assessment{
		SchemaVersion: AssessmentSchema,
		ProjectionID:  projection.ProjectionID, ProjectionContentSHA256: projection.ProjectionContentSHA256,
		ProjectionFileSHA256: hashBytes(projectionBytes), PayloadID: payload.PayloadID,
		PayloadContentSHA256: payload.PayloadContentSHA256, PayloadFileSHA256: hashBytes(payloadBytes),
		Assessor: assessor, Harness: harness, AssessedAt: assessedAt,
		Authority: "provisional_only", CandidateAssessments: submission.CandidateAssessments,
		CompactionAssessments: submission.CompactionAssessments, ArtifactStorage: "local_only",
	}
	identityBytes, err := json.Marshal(assessment)
	if err != nil {
		return result, fmt.Errorf("encode Agent assessment identity: %w", err)
	}
	assessment.AssessmentContentSHA256 = hashBytes(identityBytes)
	assessment.AssessmentID = "agent-assessment-" + assessment.AssessmentContentSHA256
	data, err := marshalIndented(assessment)
	if err != nil {
		return result, fmt.Errorf("encode Agent assessment: %w", err)
	}
	path, reused, err := writeImmutableArtifact(store, "agent-assessments", assessment.AssessmentID,
		"assessment.json", data, maximumAssessmentBytes)
	if err != nil {
		return result, err
	}
	result.AssessmentID = assessment.AssessmentID
	result.AssessmentPath = path
	result.ProjectionID = projection.ProjectionID
	result.PayloadID = payload.PayloadID
	result.CandidateItems = len(assessment.CandidateAssessments)
	result.CompactionUnits = len(assessment.CompactionAssessments)
	result.Reused = reused
	return result, nil
}

func loadVerifiedProjection(
	store *ledger.Store, projectionID string,
) (Projection, []byte, BlindPayload, []byte, error) {
	if !strings.HasPrefix(projectionID, "agent-projection-") ||
		!validSHA256(strings.TrimPrefix(projectionID, "agent-projection-")) {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("invalid Agent projection id")
	}
	root, err := secureStoreRoot(store)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	path := filepath.Join(root, "derived", "evaluations", "agent-projections", projectionID,
		"projection.json")
	data, err := readSecureFile(root, path, maximumProjectionBytes)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("read Agent projection: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projection Projection
	if err := decoder.Decode(&projection); err != nil {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("decode Agent projection: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	if projection.SchemaVersion != ProjectionSchema || projection.ProjectionID != projectionID ||
		projection.Privacy != "local_only" || projection.ExtractionVersion != ExtractionVersion {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("Agent projection metadata is invalid")
	}
	identity := projection
	identity.ProjectionID = ""
	identity.ProjectionContentSHA256 = ""
	identityBytes, err := json.Marshal(identity)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	digest := hashBytes(identityBytes)
	if projection.ProjectionContentSHA256 != digest || projection.ProjectionID != "agent-projection-"+digest {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("Agent projection content does not match its identity")
	}
	canonical, err := marshalIndented(projection)
	if err != nil || !bytes.Equal(data, canonical) {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("Agent projection is not in its canonical immutable encoding")
	}
	expected, expectedBytes, expectedPayload, expectedPayloadBytes, err := buildProjection(store, projection.QueueID)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	if expected.ProjectionID != projection.ProjectionID || !bytes.Equal(expectedBytes, data) {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("Agent projection does not match its verified queue, pack, and ledger prefix")
	}
	payloadPath := filepath.Join(root, "derived", "evaluations", "agent-payloads", projection.PayloadID,
		"payload.json")
	payloadBytes, err := readSecureFile(root, payloadPath, maximumBlindPayloadBytes)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("read blind Agent payload: %w", err)
	}
	if !bytes.Equal(payloadBytes, expectedPayloadBytes) {
		return Projection{}, nil, BlindPayload{}, nil, errors.New("blind Agent payload does not match its verified local binding")
	}
	return projection, data, expectedPayload, payloadBytes, nil
}

func decodeSubmission(reader io.Reader) (Submission, []byte, error) {
	if reader == nil {
		return Submission{}, nil, errors.New("Agent assessment submission is required")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maximumSubmissionBytes+1))
	if err != nil {
		return Submission{}, nil, err
	}
	if len(data) == 0 || len(data) > maximumSubmissionBytes {
		return Submission{}, nil, errors.New("Agent assessment submission size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var submission Submission
	if err := decoder.Decode(&submission); err != nil {
		return Submission{}, nil, fmt.Errorf("decode Agent assessment submission: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return Submission{}, nil, err
	}
	return submission, data, nil
}

func validateSubmission(projection Projection, submission Submission) error {
	if submission.SchemaVersion != SubmissionSchema || submission.PayloadID != projection.PayloadID {
		return errors.New("Agent assessment submission metadata is invalid")
	}
	if len(submission.CandidateAssessments) != len(projection.CandidateItems) {
		return errors.New("Agent assessment candidate coverage is incomplete")
	}
	for index, expected := range projection.CandidateItems {
		actual := submission.CandidateAssessments[index]
		if actual.ItemID != expected.ItemID {
			return fmt.Errorf("Agent candidate assessment %d does not match the blind payload", index)
		}
		if err := validateAssessmentFields(actual.Judgment, actual.ReasonCodes,
			actual.EvidenceBlockIDs, candidateJudgments, candidateReasonCodes,
			expected.EvidenceBlockIDs); err != nil {
			return fmt.Errorf("Agent candidate assessment %q: %w", actual.ItemID, err)
		}
		if !expected.EvidenceComplete && actual.Judgment != "insufficient_evidence" {
			return fmt.Errorf("Agent candidate assessment %q exceeds incomplete evidence", actual.ItemID)
		}
		if actual.Judgment == "supported_reusable" && !expected.EvidenceComplete {
			return fmt.Errorf("Agent candidate assessment %q cannot be reusable", actual.ItemID)
		}
	}
	expectedUnits := 0
	for _, item := range projection.CompactionItems {
		expectedUnits += len(item.Units)
	}
	if len(submission.CompactionAssessments) != expectedUnits {
		return errors.New("Agent assessment compaction coverage is incomplete")
	}
	index := 0
	for _, item := range projection.CompactionItems {
		for _, unit := range item.Units {
			actual := submission.CompactionAssessments[index]
			if actual.ItemID != item.ItemID || actual.UnitID != unit.UnitID {
				return fmt.Errorf("Agent compaction assessment %d does not match the blind payload", index)
			}
			allowed := append([]string{}, item.CheckpointEvidenceBlockIDs...)
			allowed = append(allowed, unit.SourceEvidenceBlockIDs...)
			allowed = append(allowed, unit.RepresentationEvidenceBlockIDs...)
			allowed = append(allowed, unit.CorrectionEvidenceBlockIDs...)
			allowed = uniqueSortedStrings(allowed)
			if err := validateAssessmentFields(actual.Judgment, actual.ReasonCodes,
				actual.EvidenceBlockIDs, compactionJudgments, compactionReasonCodes, allowed); err != nil {
				return fmt.Errorf("Agent compaction assessment %q: %w", actual.UnitID, err)
			}
			if !unit.EvidenceComplete && actual.Judgment != "insufficient_evidence" {
				return fmt.Errorf("Agent compaction assessment %q exceeds incomplete evidence", actual.UnitID)
			}
			if actual.Judgment == "preserved" &&
				!hasIntersection(actual.EvidenceBlockIDs, unit.RepresentationEvidenceBlockIDs) {
				return fmt.Errorf("Agent compaction assessment %q has no representation evidence", actual.UnitID)
			}
			index++
		}
	}
	return nil
}

func validateAssessmentFields(
	judgment string, reasons, evidence []string, judgments, reasonCodes map[string]struct{}, allowed []string,
) error {
	if _, valid := judgments[judgment]; !valid {
		return errors.New("judgment is invalid")
	}
	if len(reasons) == 0 || !strictlySortedUnique(reasons) {
		return errors.New("reason codes must be nonempty, sorted, and unique")
	}
	for _, reason := range reasons {
		if _, valid := reasonCodes[reason]; !valid {
			return errors.New("reason code is invalid")
		}
	}
	if !strictlySortedUnique(evidence) && len(evidence) != 0 {
		return errors.New("evidence block ids must be sorted and unique")
	}
	allowedSet := stringSetOf(allowed...)
	for _, reference := range evidence {
		if _, valid := allowedSet[reference]; !valid {
			return errors.New("evidence block id is not available to this item")
		}
	}
	if judgment != "insufficient_evidence" && len(evidence) == 0 {
		return errors.New("non-insufficient judgment requires direct evidence")
	}
	return nil
}

func validateAssessmentOptions(
	options AssessmentOptions, submissionBytes []byte,
) (Assessor, HarnessProvenance, string, error) {
	var assessor Assessor
	var harness HarnessProvenance
	values := []struct {
		name, value string
	}{
		{"assessor id", options.AssessorID}, {"claimed provider", options.ClaimedProvider},
		{"claimed model", options.ClaimedModel},
		{"harness version", options.HarnessVersion},
	}
	for _, value := range values {
		if !safeMetadata(value.value) {
			return assessor, harness, "", fmt.Errorf("%s is invalid", value.name)
		}
	}
	if !validSHA256(options.PromptSHA256) {
		return assessor, harness, "", errors.New("prompt SHA-256 is invalid")
	}
	if options.DataDisclosureClaim != "remote" && options.DataDisclosureClaim != "local" &&
		options.DataDisclosureClaim != "unknown" {
		return assessor, harness, "", errors.New("data disclosure claim is invalid")
	}
	assessedAt, err := time.Parse(time.RFC3339Nano, options.AssessedAt)
	if err != nil || options.AssessedAt == "" {
		return assessor, harness, "", errors.New("assessed_at must be RFC3339")
	}
	assessor = Assessor{
		Kind: "agent", ID: options.AssessorID, ClaimedProvider: options.ClaimedProvider,
		ClaimedModel: options.ClaimedModel,
	}
	harness = HarnessProvenance{
		Version: options.HarnessVersion, PromptSHA256: options.PromptSHA256,
		Source: "external_submission", IsolationStatus: "unverified_external",
		ToolsRegistered: nil, DataDisclosureClaim: options.DataDisclosureClaim,
		ExtractedSubmissionSHA256: hashBytes(submissionBytes),
	}
	return assessor, harness, assessedAt.UTC().Format(time.RFC3339Nano), nil
}

func safeMetadata(value string) bool {
	return value != "" && len(value) <= 200 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func stringSetOf(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func strictlySortedUnique(values []string) bool {
	return sort.StringsAreSorted(values) && len(uniqueSortedStrings(append([]string{}, values...))) == len(values)
}

func hasIntersection(left, right []string) bool {
	values := stringSetOf(right...)
	for _, value := range left {
		if _, exists := values[value]; exists {
			return true
		}
	}
	return false
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return errors.New("multiple JSON values are not allowed")
}
