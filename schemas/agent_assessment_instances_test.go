package schemas_test

import (
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/agentassessment"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestAgentAssessmentProjectionInstancesMatchPublishedSchemas(t *testing.T) {
	hash := strings.Repeat("a", 64)
	blockID := "blind-evidence-" + hash
	itemID := "agent-item-" + hash
	projection := agentassessment.Projection{
		SchemaVersion: agentassessment.ProjectionSchema,
		ProjectionID:  "agent-projection-" + hash, ProjectionContentSHA256: hash,
		PayloadID: "agent-payload-" + hash, PayloadContentSHA256: hash,
		QueueID: "review-queue-" + hash, QueueSHA256: hash,
		PackID: "review-pack-" + hash, PackSHA256: hash,
		CorpusID: "corpus-" + hash, CorpusContentSHA256: hash,
		CandidateManifestSHA256: hash, CandidatesSHA256: hash, EpisodesSHA256: hash,
		SourceEvidencePrefix: agentassessment.SourcePrefix{Records: 1, LastRecordHash: hash},
		ExtractionVersion:    agentassessment.ExtractionVersion,
		EvidenceBlocks: []agentassessment.EvidenceBlock{{
			BlockID: blockID, RecordSHA256: hash, Sequence: 1,
			Kind: ledger.KindUserMessage, Agent: ledger.AgentCodex,
			Completeness: ledger.CompletenessComplete, PayloadSHA256: hash,
			PayloadBytes: 19, ExtractedSHA256: hash, Text: "Keep evidence local.",
			UntrustedContent: true,
		}},
		CandidateItems: []agentassessment.CandidateItem{{
			ItemID: itemID, SubjectSHA256: hash, Text: "Keep evidence local.",
			UntrustedContent: true, EvidenceBlockIDs: []string{blockID}, EvidenceComplete: true,
		}},
		CompactionItems: []agentassessment.CompactionItem{{
			ItemID: itemID, CheckpointEvidenceBlockIDs: []string{blockID},
			CheckpointEvidenceComplete: true, AllUnitsProjected: true,
			Units: []agentassessment.CompactionUnit{{
				UnitID: "assessment-unit-" + hash, SubjectSHA256: hash,
				Statement: "Keep evidence local.", UntrustedContent: true,
				SourceEvidenceBlockIDs:         []string{blockID},
				RepresentationEvidenceBlockIDs: []string{blockID}, EvidenceComplete: true,
			}},
		}},
		Privacy: "local_only",
	}
	payload := agentassessment.BlindPayload{
		SchemaVersion: agentassessment.BlindPayloadSchema,
		PayloadID:     projection.PayloadID, PayloadContentSHA256: hash,
		EvidenceBlocks: []agentassessment.BlindEvidenceBlock{{
			BlockID: blockID, Order: 1, Role: "user", Text: "Keep evidence local.",
			UntrustedContent: true,
		}},
		CandidateItems: []agentassessment.BlindCandidateItem{{
			ItemID: itemID, Text: "Keep evidence local.", UntrustedContent: true,
			EvidenceBlockIDs: []string{blockID},
		}},
		CompactionItems: []agentassessment.BlindCompactionItem{{
			ItemID: itemID, CheckpointEvidenceBlockIDs: []string{blockID},
			Units: []agentassessment.BlindCompactionUnit{{
				UnitID: "assessment-unit-" + hash, Statement: "Keep evidence local.",
				UntrustedContent: true, SourceEvidenceBlockIDs: []string{blockID},
				RepresentationEvidenceBlockIDs: []string{blockID},
			}},
		}},
		ArtifactStorage: "local_only", DataClassification: "selected_unredacted_evidence",
	}
	result := agentassessment.ProjectionResult{
		SchemaVersion: agentassessment.ProjectionResultSchema,
		ProjectionID:  projection.ProjectionID, ProjectionPath: "local-projection.json",
		PayloadID: projection.PayloadID, PayloadPath: "local-payload.json",
		QueueID: projection.QueueID, CandidateItems: 1, CompactionItems: 1,
		CompactionUnits: 1, EvidenceBlocks: 1, Privacy: "local_only",
	}
	submission := agentassessment.Submission{
		SchemaVersion: agentassessment.SubmissionSchema, PayloadID: projection.PayloadID,
		CandidateAssessments: []agentassessment.CandidateAssessment{{
			ItemID: itemID, Judgment: "supported_reusable",
			ReasonCodes: []string{"direct_user_support"}, EvidenceBlockIDs: []string{blockID},
		}},
		CompactionAssessments: []agentassessment.CompactionAssessment{{
			ItemID: itemID, UnitID: "assessment-unit-" + hash,
			Judgment: "preserved", ReasonCodes: []string{"representation_preserves_statement"},
			EvidenceBlockIDs: []string{blockID},
		}},
	}
	assessment := agentassessment.Assessment{
		SchemaVersion: agentassessment.AssessmentSchema,
		AssessmentID:  "agent-assessment-" + hash, AssessmentContentSHA256: hash,
		ProjectionID: projection.ProjectionID, ProjectionContentSHA256: hash,
		ProjectionFileSHA256: hash,
		PayloadID:            projection.PayloadID, PayloadContentSHA256: hash, PayloadFileSHA256: hash,
		Assessor: agentassessment.Assessor{Kind: "agent", ID: "ranker", Provenance: "external_claim",
			ClaimedProvider: "provider", ClaimedModel: "model"},
		Harness: agentassessment.HarnessProvenance{
			Version: "v1", PromptSHA256: hash, Source: "external_submission",
			IsolationStatus: "unverified_external", ToolsRegistered: nil,
			DataDisclosureClaim: "remote", ExtractedSubmissionSHA256: hash,
		},
		AssessedAt: "2026-08-05T00:00:00Z", Authority: "provisional_only",
		CandidateAssessments:  submission.CandidateAssessments,
		CompactionAssessments: submission.CompactionAssessments, ArtifactStorage: "local_only",
	}
	assessmentResult := agentassessment.AssessmentResult{
		SchemaVersion: agentassessment.AssessmentResultSchema,
		AssessmentID:  assessment.AssessmentID, AssessmentPath: "local-assessment.json",
		ProjectionID: projection.ProjectionID, PayloadID: projection.PayloadID,
		CandidateItems: 1, CompactionUnits: 1, Authority: "provisional_only",
		ArtifactStorage: "local_only",
	}
	controlledAssessment := assessment
	controlledAssessment.Assessor = agentassessment.Assessor{
		Kind: "agent", ID: "openai-responses", Provenance: "controlled_observation",
		Provider: "openai", RequestedModel: "gpt-5.4-mini", ObservedModel: "gpt-5.4-mini-2026-08-01",
	}
	toolsRegistered := false
	controlledAssessment.Harness = agentassessment.HarnessProvenance{
		Version: agentassessment.OpenAIHarnessVersion, PromptSHA256: hash,
		Source: "controlled_openai_responses", IsolationStatus: "request_policy_observed",
		ToolsRegistered: &toolsRegistered, DataDisclosure: "remote",
		ExtractedSubmissionSHA256: hash,
		AttemptID:                 "agent-openai-attempt-" + hash, AttemptContentSHA256: hash,
		ObservationID: "agent-openai-observation-" + hash, ObservationContentSHA256: hash,
	}
	validatePublishedInstance(t, "legacy-agent-assessment-projection.schema.json", projection)
	validatePublishedInstance(t, "legacy-agent-assessment-payload.schema.json", payload)
	validatePublishedInstance(t, "legacy-agent-assessment-projection-result.schema.json", result)
	validatePublishedInstance(t, "legacy-agent-assessment-submission.schema.json", submission)
	validatePublishedInstance(t, "legacy-agent-assessment.schema.json", assessment)
	validatePublishedInstance(t, "legacy-agent-assessment.schema.json", controlledAssessment)
	validatePublishedInstance(t, "legacy-agent-assessment-result.schema.json", assessmentResult)

	mismatched := controlledAssessment
	mismatched.Harness = assessment.Harness
	rejectPublishedInstance(t, "legacy-agent-assessment.schema.json", mismatched)
}
