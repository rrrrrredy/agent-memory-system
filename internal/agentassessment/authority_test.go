package agentassessment

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestAssessmentCannotDecodeAsAuthorityBearingRequest(t *testing.T) {
	hash := strings.Repeat("a", 64)
	assessment := Assessment{
		SchemaVersion: AssessmentSchema, AssessmentID: "agent-assessment-" + hash,
		AssessmentContentSHA256: hash, ProjectionID: "agent-projection-" + hash,
		ProjectionContentSHA256: hash, ProjectionFileSHA256: hash,
		PayloadID: "agent-payload-" + hash, PayloadContentSHA256: hash, PayloadFileSHA256: hash,
		Assessor: Assessor{Kind: "agent", ID: "ranker", ClaimedProvider: "provider",
			ClaimedModel: "model"},
		Harness: HarnessProvenance{Version: "v1", PromptSHA256: hash,
			Source: "external_submission", IsolationStatus: "unverified_external",
			ToolsRegistered: nil, DataDisclosureClaim: "unknown", ExtractedSubmissionSHA256: hash},
		AssessedAt: "2026-08-05T00:00:00Z", Authority: "provisional_only", ArtifactStorage: "local_only",
	}
	data, err := json.Marshal(assessment)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := review.DecodeRequest(bytes.NewReader(data)); err == nil {
		t.Fatal("Agent assessment decoded as a human review request")
	}
	if _, err := promotion.DecodeRequest(bytes.NewReader(data)); err == nil {
		t.Fatal("Agent assessment decoded as a promotion request")
	}
	if _, err := evaluation.DecodeAttestation(bytes.NewReader(data)); err == nil {
		t.Fatal("Agent assessment decoded as an evaluation attestation")
	}
}
