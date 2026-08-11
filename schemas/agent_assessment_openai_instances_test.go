package schemas_test

import (
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/agentassessment"
)

func TestControlledOpenAIAssessmentInstancesMatchPublishedSchemas(t *testing.T) {
	hash := strings.Repeat("a", 64)
	attempt := agentassessment.OpenAIAttempt{
		SchemaVersion: agentassessment.OpenAIAttemptSchema,
		AttemptID:     "agent-openai-attempt-" + hash, AttemptContentSHA256: hash,
		ProjectionID: "agent-projection-" + hash, PayloadID: "agent-payload-" + hash,
		PayloadContentSHA256: hash, PayloadFileSHA256: hash, RequestedModel: "gpt-5.4-mini",
		EndpointOrigin: "https://api.openai.com", EndpointPath: "/v1/responses",
		PromptSHA256: hash, OutputSchemaSHA256: hash, RequestSHA256: hash, RequestBytes: 2048,
		Disclosure: agentassessment.RemoteDisclosureObservation{
			Confirmed: true, ConfirmationPayloadID: "agent-payload-" + hash,
			DataClassification: "selected_unredacted_evidence",
			Destination:        "https://api.openai.com", ConfirmedAt: "2026-08-05T00:00:00Z",
		},
		SecretScan: agentassessment.RemoteSecretScanObservation{
			ScannerVersion: "sensitive-scan/v1", FieldsScanned: 4, UniqueTexts: 3,
			BytesScanned: 128, ContentSetSHA256: hash, Findings: 0, Categories: []string{},
		},
		RequestPolicy: agentassessment.ResponsesRequestPolicy{
			Store: false, ToolsRegistered: false, ToolChoice: "none", ResponseFormat: "json_schema",
			ReasoningEffort: "none",
			StrictOutput:    true, Background: false, ConversationState: false, Streaming: false,
			Truncation: "disabled", AutomaticRetries: 0, EnvironmentProxy: false, Redirects: false,
		},
		StartedAt: "2026-08-05T00:00:00Z", Authority: "provisional_only",
		ArtifactStorage: "local_only",
	}
	observation := agentassessment.OpenAIObservation{
		SchemaVersion: agentassessment.OpenAIObservationSchema,
		ObservationID: "agent-openai-observation-" + hash, ObservationContentSHA256: hash,
		AttemptID: attempt.AttemptID, AttemptContentSHA256: hash, RequestSHA256: hash,
		HTTPStatus: 200, ResponseCaptureStatus: "complete", ResponseSHA256: hash, ResponseBytes: 1024,
		ResponsePrefixBytes: 0,
		ContentType:         "application/json", OpenAIRequestID: "req_test", ResponseID: "resp_test",
		RequestedModel: "gpt-5.4-mini", ObservedModel: "gpt-5.4-mini-2026-08-01",
		ResponseStatus: "completed", Outcome: "validated_submission", SubmissionSHA256: hash,
		CompletedAt: "2026-08-05T00:01:00Z", Authority: "provisional_only",
		ArtifactStorage: "local_only",
	}
	assessment := agentassessment.AssessmentResult{
		SchemaVersion: agentassessment.AssessmentResultSchema,
		AssessmentID:  "agent-assessment-" + hash, AssessmentPath: "local-assessment.json",
		ProjectionID: attempt.ProjectionID, PayloadID: attempt.PayloadID,
		CandidateItems: 1, CompactionUnits: 1, Authority: "provisional_only",
		ArtifactStorage: "local_only",
	}
	result := agentassessment.OpenAIRunResult{
		SchemaVersion: agentassessment.OpenAIRunResultSchema,
		ProjectionID:  attempt.ProjectionID, PayloadID: attempt.PayloadID,
		AttemptID: attempt.AttemptID, AttemptPath: "local-attempt.json",
		ObservationID: observation.ObservationID, ObservationPath: "local-observation.json",
		OpenAIRequestID: "req_test", RequestedModel: attempt.RequestedModel,
		ObservedModel: observation.ObservedModel, Assessment: &assessment,
		Authority: "provisional_only", ArtifactStorage: "local_only",
	}

	validatePublishedInstance(t, "legacy-agent-assessment-openai-attempt.schema.json", attempt)
	validatePublishedInstance(t, "legacy-agent-assessment-openai-observation.schema.json", observation)
	validatePublishedInstance(t, "legacy-agent-assessment-openai-run-result.schema.json", result)

	invalidAttempt := attempt
	invalidAttempt.RequestPolicy.ToolsRegistered = true
	rejectPublishedInstance(t, "legacy-agent-assessment-openai-attempt.schema.json", invalidAttempt)
	invalidObservation := observation
	invalidObservation.SubmissionSHA256 = ""
	rejectPublishedInstance(t, "legacy-agent-assessment-openai-observation.schema.json", invalidObservation)
}
