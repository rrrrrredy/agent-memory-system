package agentassessment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateSubmissionRequiresExactBlindCoverageAndEvidence(t *testing.T) {
	projection, submission := assessmentFixture()
	if err := validateSubmission(projection, submission); err != nil {
		t.Fatal(err)
	}

	missing := submission
	missing.CandidateAssessments = nil
	if err := validateSubmission(projection, missing); err == nil || !strings.Contains(err.Error(), "coverage") {
		t.Fatalf("missing assessment was accepted: %v", err)
	}

	unknownEvidence := submission
	unknownEvidence.CandidateAssessments = cloneCandidates(submission.CandidateAssessments)
	unknownEvidence.CandidateAssessments[0].EvidenceBlockIDs = []string{"blind-evidence-" + strings.Repeat("f", 64)}
	if err := validateSubmission(projection, unknownEvidence); err == nil ||
		!strings.Contains(err.Error(), "not available") {
		t.Fatalf("unknown evidence was accepted: %v", err)
	}

	incomplete := submission
	incomplete.CandidateAssessments = cloneCandidates(submission.CandidateAssessments)
	projection.CandidateItems[0].EvidenceComplete = false
	if err := validateSubmission(projection, incomplete); err == nil ||
		!strings.Contains(err.Error(), "incomplete evidence") {
		t.Fatalf("positive judgment on incomplete evidence was accepted: %v", err)
	}
}

func TestValidateSubmissionRejectsCompactionPreservedWithoutRepresentationReference(t *testing.T) {
	projection, submission := assessmentFixture()
	submission.CompactionAssessments[0].EvidenceBlockIDs = []string{
		projection.CompactionItems[0].CheckpointEvidenceBlockIDs[0],
	}
	if err := validateSubmission(projection, submission); err == nil ||
		!strings.Contains(err.Error(), "no representation evidence") {
		t.Fatalf("preserved judgment without representation evidence was accepted: %v", err)
	}
}

func TestDecodeSubmissionRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	unknown := `{"schema_version":"legacy-agent-assessment-submission/v1alpha1","payload_id":"agent-payload-`
	unknown = unknown + strings.Repeat("a", 64) +
		`","candidate_assessments":[],"compaction_assessments":[],"reviewer_kind":"human"}`
	if _, _, err := decodeSubmission(strings.NewReader(unknown)); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown submission field was accepted: %v", err)
	}
	if _, _, err := decodeSubmission(strings.NewReader(`{} {}`)); err == nil {
		t.Fatal("multiple submission values were accepted")
	}
}

func TestExternalAssessmentProvenanceKeepsIsolationUnverified(t *testing.T) {
	_, submission := assessmentFixture()
	data, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	assessor, harness, assessedAt, err := validateAssessmentOptions(AssessmentOptions{
		AssessorID: "quality-ranker", ClaimedProvider: "example-provider",
		ClaimedModel:   "claimed-model",
		HarnessVersion: "no-tools/v1", PromptSHA256: hash,
		DataDisclosureClaim: "remote", AssessedAt: "2026-08-05T12:00:00+08:00",
	}, data)
	if err != nil {
		t.Fatal(err)
	}
	if assessor.Kind != "agent" || harness.ToolsRegistered != nil ||
		harness.IsolationStatus != "unverified_external" || harness.Source != "external_submission" ||
		harness.DataDisclosureClaim != "remote" ||
		harness.ExtractedSubmissionSHA256 != hashBytes(data) || assessedAt != "2026-08-05T04:00:00Z" {
		t.Fatalf("unsafe or incomplete provenance: assessor=%+v harness=%+v assessed=%s",
			assessor, harness, assessedAt)
	}
	encoded, err := json.Marshal(harness)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"tools_registered":null`) ||
		strings.Contains(string(encoded), `"tools_registered":false`) {
		t.Fatalf("external provenance claimed mechanical tool isolation: %s", encoded)
	}
	if _, _, _, err := validateAssessmentOptions(AssessmentOptions{
		AssessorID: "ranker", ClaimedProvider: "provider", ClaimedModel: "model",
		HarnessVersion: "v1", PromptSHA256: hash, DataDisclosureClaim: "verified_remote",
		AssessedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, data); err == nil {
		t.Fatal("verified disclosure claim was accepted from an external submission")
	}
}

func assessmentFixture() (Projection, Submission) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	blockA := "blind-evidence-" + hashA
	blockB := "blind-evidence-" + hashB
	candidateItem := "agent-item-" + hashA
	compactionItem := "agent-item-" + hashB
	unitID := "assessment-unit-" + hashA
	projection := Projection{
		ProjectionID: "agent-projection-" + hashA, PayloadID: "agent-payload-" + hashA,
		CandidateItems: []CandidateItem{{
			ItemID: candidateItem, SubjectSHA256: hashA,
			EvidenceBlockIDs: []string{blockA}, EvidenceComplete: true,
		}},
		CompactionItems: []CompactionItem{{
			ItemID: compactionItem, CheckpointEvidenceBlockIDs: []string{blockA},
			CheckpointEvidenceComplete: true, AllUnitsProjected: true,
			Units: []CompactionUnit{{
				UnitID: unitID, SubjectSHA256: hashB,
				SourceEvidenceBlockIDs:         []string{blockA},
				RepresentationEvidenceBlockIDs: []string{blockB}, EvidenceComplete: true,
			}},
		}},
	}
	submission := Submission{
		SchemaVersion: SubmissionSchema, PayloadID: projection.PayloadID,
		CandidateAssessments: []CandidateAssessment{{
			ItemID: candidateItem, Judgment: "supported_reusable",
			ReasonCodes: []string{"direct_user_support"}, EvidenceBlockIDs: []string{blockA},
		}},
		CompactionAssessments: []CompactionAssessment{{
			ItemID: compactionItem, UnitID: unitID, Judgment: "preserved",
			ReasonCodes:      []string{"representation_preserves_statement"},
			EvidenceBlockIDs: []string{blockA, blockB},
		}},
	}
	return projection, submission
}

func cloneCandidates(values []CandidateAssessment) []CandidateAssessment {
	result := append([]CandidateAssessment{}, values...)
	for index := range result {
		result[index].ReasonCodes = append([]string{}, result[index].ReasonCodes...)
		result[index].EvidenceBlockIDs = append([]string{}, result[index].EvidenceBlockIDs...)
	}
	return result
}
