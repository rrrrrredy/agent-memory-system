package agentassessment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	assessmentprompts "github.com/rrrrrredy/agent-memory-system/evals/prompts"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

// VerifyOpenAIAttempt replays the integrity and request-policy checks for a
// production OpenAI request attempt without contacting the provider.
func VerifyOpenAIAttempt(store *ledger.Store, attemptID string) (OpenAIAttempt, error) {
	return verifyStoredOpenAIAttempt(store, attemptID, openAIResponsesEndpoint)
}

// VerifyOpenAIObservation verifies a production response observation and its
// bound request attempt without contacting the provider.
func VerifyOpenAIObservation(
	store *ledger.Store, attemptID, observationID string,
) (OpenAIObservation, error) {
	attempt, err := VerifyOpenAIAttempt(store, attemptID)
	if err != nil {
		return OpenAIObservation{}, err
	}
	return verifyStoredOpenAIObservation(store, observationID, attempt, loadVerifiedProjection)
}

// VerifyControlledAssessment verifies every local artifact bound by a
// controlled assessment. It grants no review, attestation, or promotion authority.
func VerifyControlledAssessment(store *ledger.Store, assessmentID string) error {
	return verifyControlledAssessment(store, assessmentID, openAIResponsesEndpoint, loadVerifiedProjection)
}

func verifyControlledAssessment(
	store *ledger.Store, assessmentID, expectedEndpoint string,
	loader func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error),
) error {
	if !validDerivedID(assessmentID, "agent-assessment-") {
		return errors.New("controlled assessment id is invalid")
	}
	files, err := readExactArtifactSet(store, "agent-assessments", assessmentID,
		[]string{"assessment.json"}, maximumAssessmentBytes)
	if err != nil {
		return err
	}
	var assessment Assessment
	if err := decodeCanonicalJSON(files["assessment.json"], &assessment); err != nil {
		return fmt.Errorf("decode controlled assessment: %w", err)
	}
	identity := assessment
	identity.AssessmentID = ""
	identity.AssessmentContentSHA256 = ""
	digest, err := identityDigest(identity)
	if err != nil || assessment.SchemaVersion != AssessmentSchema ||
		assessment.AssessmentContentSHA256 != digest || assessment.AssessmentID != "agent-assessment-"+digest ||
		assessment.Authority != "provisional_only" || assessment.ArtifactStorage != "local_only" {
		return errors.New("controlled assessment identity is invalid")
	}
	if assessment.Assessor.Kind != "agent" || assessment.Assessor.ID != "openai-responses" ||
		assessment.Assessor.Provenance != "controlled_observation" || assessment.Assessor.Provider != "openai" ||
		assessment.Assessor.ClaimedProvider != "" || assessment.Assessor.ClaimedModel != "" ||
		assessment.Harness.Version != OpenAIHarnessVersion ||
		assessment.Harness.Source != "controlled_openai_responses" ||
		assessment.Harness.IsolationStatus != "request_policy_observed" ||
		assessment.Harness.ToolsRegistered == nil || *assessment.Harness.ToolsRegistered ||
		assessment.Harness.DataDisclosure != "remote" || assessment.Harness.DataDisclosureClaim != "" ||
		assessment.Harness.PromptSHA256 != assessmentprompts.AgentAssessmentV1SHA256() {
		return errors.New("controlled assessment provenance is invalid")
	}
	attempt, err := verifyStoredOpenAIAttempt(store, assessment.Harness.AttemptID, expectedEndpoint)
	if err != nil {
		return err
	}
	observation, err := verifyStoredOpenAIObservation(store, assessment.Harness.ObservationID, attempt, loader)
	if err != nil {
		return err
	}
	if assessment.Harness.AttemptContentSHA256 != attempt.AttemptContentSHA256 ||
		assessment.Harness.ObservationContentSHA256 != observation.ObservationContentSHA256 ||
		observation.Outcome != "validated_submission" ||
		assessment.Assessor.RequestedModel != attempt.RequestedModel ||
		assessment.Assessor.ObservedModel != observation.ObservedModel ||
		assessment.AssessedAt != observation.CompletedAt {
		return errors.New("controlled assessment artifact bindings are invalid")
	}
	projection, projectionBytes, payload, payloadBytes, err := loader(store, assessment.ProjectionID)
	if err != nil {
		return err
	}
	if assessment.ProjectionContentSHA256 != projection.ProjectionContentSHA256 ||
		assessment.ProjectionFileSHA256 != hashBytes(projectionBytes) ||
		assessment.PayloadID != payload.PayloadID || assessment.PayloadContentSHA256 != payload.PayloadContentSHA256 ||
		assessment.PayloadFileSHA256 != hashBytes(payloadBytes) || attempt.ProjectionID != assessment.ProjectionID ||
		attempt.PayloadID != assessment.PayloadID {
		return errors.New("controlled assessment projection bindings are invalid")
	}
	response, err := readOpenAIResponseFile(store, observation)
	if err != nil {
		return err
	}
	_, submissionBytes, _, err := extractOpenAISubmission(response)
	if err != nil || hashBytes(submissionBytes) != assessment.Harness.ExtractedSubmissionSHA256 {
		return errors.New("controlled assessment submission binding is invalid")
	}
	submission, _, err := decodeSubmission(bytes.NewReader(submissionBytes))
	if err != nil || validateSubmission(projection, submission) != nil ||
		!reflect.DeepEqual(submission.CandidateAssessments, assessment.CandidateAssessments) ||
		!reflect.DeepEqual(submission.CompactionAssessments, assessment.CompactionAssessments) {
		return errors.New("controlled assessment decisions do not match the verified response")
	}
	return nil
}

func verifyStoredOpenAIAttempt(
	store *ledger.Store, attemptID, expectedEndpoint string,
) (OpenAIAttempt, error) {
	if !validDerivedID(attemptID, "agent-openai-attempt-") {
		return OpenAIAttempt{}, errors.New("OpenAI attempt id is invalid")
	}
	files, err := readExactArtifactSet(store, "agent-openai-attempts", attemptID,
		[]string{"attempt.json", "request.json"}, maximumOpenAIAttemptBytes)
	if err != nil {
		return OpenAIAttempt{}, err
	}
	var attempt OpenAIAttempt
	if err := decodeCanonicalJSON(files["attempt.json"], &attempt); err != nil {
		return OpenAIAttempt{}, fmt.Errorf("decode OpenAI attempt: %w", err)
	}
	identity := attempt
	identity.AttemptID = ""
	identity.AttemptContentSHA256 = ""
	digest, err := identityDigest(identity)
	if err != nil || attempt.SchemaVersion != OpenAIAttemptSchema ||
		attempt.AttemptContentSHA256 != digest || attempt.AttemptID != "agent-openai-attempt-"+digest {
		return OpenAIAttempt{}, errors.New("OpenAI attempt identity is invalid")
	}
	if !validDerivedID(attempt.ProjectionID, "agent-projection-") ||
		!validDerivedID(attempt.PayloadID, "agent-payload-") ||
		!validSHA256(attempt.PayloadContentSHA256) || !validSHA256(attempt.PayloadFileSHA256) ||
		!validSHA256(attempt.PromptSHA256) || !validSHA256(attempt.OutputSchemaSHA256) ||
		attempt.RequestSHA256 != hashBytes(files["request.json"]) ||
		attempt.RequestBytes != int64(len(files["request.json"])) ||
		attempt.Authority != "provisional_only" || attempt.ArtifactStorage != "local_only" ||
		attempt.PromptSHA256 != assessmentprompts.AgentAssessmentV1SHA256() {
		return OpenAIAttempt{}, errors.New("OpenAI attempt metadata is invalid")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, attempt.StartedAt)
	if err != nil || startedAt.IsZero() || attempt.Disclosure.ConfirmedAt != attempt.StartedAt {
		return OpenAIAttempt{}, errors.New("OpenAI attempt time is invalid")
	}
	expectedOrigin, expectedPath, err := endpointParts(expectedEndpoint)
	if err != nil || attempt.EndpointOrigin != expectedOrigin || attempt.EndpointPath != expectedPath ||
		!attempt.Disclosure.Confirmed || attempt.Disclosure.ConfirmationPayloadID != attempt.PayloadID ||
		attempt.Disclosure.DataClassification != "selected_unredacted_evidence" ||
		attempt.Disclosure.Destination != expectedOrigin {
		return OpenAIAttempt{}, errors.New("OpenAI attempt disclosure is invalid")
	}
	expectedPolicy := ResponsesRequestPolicy{
		Store: false, ToolsRegistered: false, ToolChoice: "none", ResponseFormat: "json_schema",
		ReasoningEffort: "none",
		StrictOutput:    true, Background: false, ConversationState: false, Streaming: false,
		Truncation: "disabled", AutomaticRetries: 0, EnvironmentProxy: false, Redirects: false,
	}
	if !reflect.DeepEqual(attempt.RequestPolicy, expectedPolicy) || attempt.SecretScan.Findings != 0 ||
		len(attempt.SecretScan.Categories) != 0 || attempt.SecretScan.FieldsScanned < 1 ||
		attempt.SecretScan.UniqueTexts < 1 || attempt.SecretScan.BytesScanned < 1 ||
		!validSHA256(attempt.SecretScan.ContentSetSHA256) {
		return OpenAIAttempt{}, errors.New("OpenAI attempt request policy is invalid")
	}
	if err := verifyOpenAIRequestArtifact(attempt, files["request.json"]); err != nil {
		return OpenAIAttempt{}, err
	}
	return attempt, nil
}

func verifyOpenAIRequestArtifact(attempt OpenAIAttempt, data []byte) error {
	var request responsesRequest
	if err := decodeCanonicalJSON(data, &request); err != nil {
		return fmt.Errorf("decode OpenAI request: %w", err)
	}
	if request.Model != attempt.RequestedModel || request.Store || len(request.Tools) != 0 ||
		request.ToolChoice != "none" || request.Reasoning.Effort != "none" || request.Truncation != "disabled" ||
		request.MaxOutputTokens != openAIMaximumOutputTokens || len(request.Input) != 2 ||
		request.Input[0].Role != "developer" || len(request.Input[0].Content) != 1 ||
		request.Input[0].Content[0].Type != "input_text" ||
		request.Input[0].Content[0].Text != assessmentprompts.AgentAssessmentV1 ||
		request.Input[1].Role != "user" || len(request.Input[1].Content) != 1 ||
		request.Input[1].Content[0].Type != "input_text" || request.Text.Format.Type != "json_schema" ||
		request.Text.Format.Name != "legacy_agent_assessment_submission" || !request.Text.Format.Strict {
		return errors.New("OpenAI request contract is invalid")
	}
	payloadBytes := []byte(request.Input[1].Content[0].Text)
	var payload BlindPayload
	if err := decodeCanonicalJSON(payloadBytes, &payload); err != nil {
		return errors.New("OpenAI request blind payload is invalid")
	}
	identity := payload
	identity.PayloadID = ""
	identity.PayloadContentSHA256 = ""
	digest, err := identityDigest(identity)
	if err != nil || payload.SchemaVersion != BlindPayloadSchema || payload.PayloadContentSHA256 != digest ||
		payload.PayloadID != "agent-payload-"+digest || payload.PayloadID != attempt.PayloadID ||
		attempt.PayloadContentSHA256 != digest || attempt.PayloadFileSHA256 != hashBytes(payloadBytes) {
		return errors.New("OpenAI request blind payload identity is invalid")
	}
	scan, err := scanBlindPayload(payload)
	if err != nil || !reflect.DeepEqual(scan, attempt.SecretScan) {
		return errors.New("OpenAI request secret-scan binding is invalid")
	}
	expectedSchema := providerSubmissionSchema(payload.PayloadID)
	schemaBytes, err := json.Marshal(request.Text.Format.Schema)
	expectedSchemaBytes, expectedErr := json.Marshal(expectedSchema)
	if err != nil || expectedErr != nil || attempt.OutputSchemaSHA256 != hashBytes(schemaBytes) ||
		!bytes.Equal(schemaBytes, expectedSchemaBytes) {
		return errors.New("OpenAI request output schema is invalid")
	}
	expectedRequest, expectedOutputSchemaSHA, err := buildOpenAIRequest(request.Model, payload, payloadBytes)
	if err != nil || expectedOutputSchemaSHA != attempt.OutputSchemaSHA256 || !bytes.Equal(expectedRequest, data) {
		return errors.New("OpenAI request does not match the controlled request builder")
	}
	return nil
}

func verifyStoredOpenAIObservation(
	store *ledger.Store, observationID string, attempt OpenAIAttempt,
	loader func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error),
) (OpenAIObservation, error) {
	if !validDerivedID(observationID, "agent-openai-observation-") {
		return OpenAIObservation{}, errors.New("OpenAI observation id is invalid")
	}
	root, err := secureStoreRoot(store)
	if err != nil {
		return OpenAIObservation{}, err
	}
	path := filepath.Join(root, "derived", "evaluations", "agent-openai-observations", observationID,
		"observation.json")
	data, err := readSecureFile(root, path, maximumOpenAIObservationBytes)
	if err != nil {
		return OpenAIObservation{}, err
	}
	var observation OpenAIObservation
	if err := decodeCanonicalJSON(data, &observation); err != nil {
		return OpenAIObservation{}, fmt.Errorf("decode OpenAI observation: %w", err)
	}
	identity := observation
	identity.ObservationID = ""
	identity.ObservationContentSHA256 = ""
	digest, err := identityDigest(identity)
	if err != nil || observation.SchemaVersion != OpenAIObservationSchema ||
		observation.ObservationContentSHA256 != digest ||
		observation.ObservationID != "agent-openai-observation-"+digest ||
		observation.AttemptID != attempt.AttemptID ||
		observation.AttemptContentSHA256 != attempt.AttemptContentSHA256 ||
		observation.RequestSHA256 != attempt.RequestSHA256 ||
		observation.RequestedModel != attempt.RequestedModel ||
		observation.Authority != "provisional_only" || observation.ArtifactStorage != "local_only" {
		return OpenAIObservation{}, errors.New("OpenAI observation identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.CompletedAt); err != nil ||
		observation.Outcome == "" || observation.HTTPStatus < 0 || observation.HTTPStatus > 599 {
		return OpenAIObservation{}, errors.New("OpenAI observation metadata is invalid")
	}
	response, err := readOpenAIResponseFile(store, observation)
	if err != nil {
		return OpenAIObservation{}, err
	}
	if err := verifyOpenAIObservationState(store, observation, response, attempt, loader); err != nil {
		return OpenAIObservation{}, err
	}
	return observation, nil
}

func verifyOpenAIObservationState(
	store *ledger.Store, observation OpenAIObservation, response []byte, attempt OpenAIAttempt,
	loader func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error),
) error {
	if observation.ResponseCaptureStatus == "none" || observation.ResponseCaptureStatus == "incomplete" {
		return nil
	}
	if observation.HTTPStatus < 200 || observation.HTTPStatus >= 300 {
		if observation.Outcome != "http_error" || observation.FailureCode != "provider_http_error" ||
			observation.SubmissionSHA256 != "" || observation.ResponseID != "" ||
			observation.ObservedModel != "" || observation.ResponseStatus != "" {
			return errors.New("OpenAI HTTP error observation is inconsistent")
		}
		return nil
	}
	if !isJSONMediaType(observation.ContentType) {
		if observation.Outcome != "invalid_response" || observation.FailureCode != "unexpected_content_type" ||
			observation.SubmissionSHA256 != "" || observation.ResponseID != "" ||
			observation.ObservedModel != "" || observation.ResponseStatus != "" {
			return errors.New("OpenAI content-type observation is inconsistent")
		}
		return nil
	}
	envelope, submissionBytes, parsedOutcome, parseErr := extractOpenAISubmission(response)
	if observation.ResponseID != safeHeader(envelope.ID) ||
		observation.ObservedModel != safeHeader(envelope.Model) ||
		observation.ResponseStatus != safeHeader(envelope.Status) {
		return errors.New("OpenAI response envelope does not match its observation")
	}
	if parseErr != nil {
		if observation.Outcome != parsedOutcome || observation.FailureCode != parsedOutcome ||
			observation.SubmissionSHA256 != "" {
			return errors.New("OpenAI observation failure does not match the response")
		}
		return nil
	}
	submission, _, decodeErr := decodeSubmission(bytes.NewReader(submissionBytes))
	var validationErr error
	if decodeErr == nil {
		projection, _, payload, _, loadErr := loader(store, attempt.ProjectionID)
		if loadErr != nil {
			return loadErr
		}
		if projection.ProjectionID != attempt.ProjectionID || projection.PayloadID != attempt.PayloadID ||
			payload.PayloadID != attempt.PayloadID {
			return errors.New("OpenAI observation projection binding is invalid")
		}
		validationErr = validateSubmission(projection, submission)
	}
	switch observation.Outcome {
	case "validated_submission":
		if decodeErr != nil || validationErr != nil || observation.FailureCode != "" ||
			observation.SubmissionSHA256 != hashBytes(submissionBytes) {
			return errors.New("validated OpenAI observation is invalid")
		}
	case "invalid_submission":
		if observation.SubmissionSHA256 != "" {
			return errors.New("invalid OpenAI submission has a truth hash")
		}
		if decodeErr != nil && observation.FailureCode != "submission_decode_failed" {
			return errors.New("OpenAI submission decode failure is misclassified")
		}
		if decodeErr == nil && (validationErr == nil || observation.FailureCode != "submission_validation_failed") {
			return errors.New("OpenAI submission validation failure is misclassified")
		}
	default:
		return errors.New("OpenAI observation outcome does not match a valid structured response")
	}
	return nil
}

func readOpenAIResponseFile(store *ledger.Store, observation OpenAIObservation) ([]byte, error) {
	var names []string
	switch observation.ResponseCaptureStatus {
	case "none":
		names = []string{"observation.json"}
		if observation.ResponseSHA256 != "" || observation.ResponsePrefixSHA256 != "" ||
			observation.ResponseBytes != 0 || observation.ResponsePrefixBytes != 0 || observation.HTTPStatus != 0 ||
			observation.Outcome != "transport_error" ||
			(observation.FailureCode != "timeout" && observation.FailureCode != "transport_error") ||
			observation.ContentType != "" || observation.OpenAIRequestID != "" || observation.ResponseID != "" ||
			observation.ObservedModel != "" || observation.ResponseStatus != "" || observation.SubmissionSHA256 != "" {
			return nil, errors.New("empty OpenAI observation capture is inconsistent")
		}
	case "complete":
		names = []string{"observation.json", "response.json"}
	case "incomplete":
		names = []string{"observation.json", "response-prefix.bin"}
	default:
		return nil, errors.New("OpenAI response capture status is invalid")
	}
	files, err := readExactArtifactSet(store, "agent-openai-observations", observation.ObservationID,
		names, maximumOpenAIObservationBytes)
	if err != nil {
		return nil, err
	}
	if observation.ResponseCaptureStatus == "none" {
		return nil, nil
	}
	name := "response.json"
	expectedHash := observation.ResponseSHA256
	expectedBytes := observation.ResponseBytes
	if observation.ResponseCaptureStatus == "incomplete" {
		name = "response-prefix.bin"
		expectedHash = observation.ResponsePrefixSHA256
		expectedBytes = observation.ResponsePrefixBytes
		if observation.ResponseSHA256 != "" || observation.ResponseBytes != 0 ||
			observation.Outcome != "invalid_response" ||
			observation.FailureCode != "response_too_large_or_unreadable" || observation.HTTPStatus < 100 ||
			observation.ResponseID != "" || observation.ObservedModel != "" ||
			observation.ResponseStatus != "" || observation.SubmissionSHA256 != "" {
			return nil, errors.New("incomplete OpenAI response capture is inconsistent")
		}
	} else if observation.ResponsePrefixSHA256 != "" || observation.ResponsePrefixBytes != 0 {
		return nil, errors.New("complete OpenAI response capture is inconsistent")
	}
	response := files[name]
	if !validSHA256(expectedHash) || expectedHash != hashBytes(response) || expectedBytes != int64(len(response)) {
		return nil, errors.New("OpenAI response capture hash is invalid")
	}
	return response, nil
}

func readExactArtifactSet(
	store *ledger.Store, category, identity string, names []string, maximum int64,
) (map[string][]byte, error) {
	root, err := secureStoreRoot(store)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "derived", "evaluations", category, identity)
	if err := validateExistingPath(root, directory, true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(names) {
		return nil, errors.New("Agent assessment artifact set is incomplete")
	}
	expected := make(map[string]struct{}, len(names))
	for _, name := range names {
		expected[name] = struct{}{}
	}
	files := make(map[string][]byte, len(names))
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok || entry.IsDir() {
			return nil, errors.New("Agent assessment artifact set is incomplete")
		}
		data, err := readSecureFile(root, filepath.Join(directory, entry.Name()), maximum)
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	return files, nil
}

func decodeCanonicalJSON(data []byte, target any) error {
	if len(data) == 0 {
		return errors.New("JSON artifact is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	canonical, err := marshalIndented(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("JSON artifact is not in canonical immutable encoding")
	}
	return nil
}

func identityDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func validDerivedID(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validSHA256(strings.TrimPrefix(value, prefix))
}

func endpointParts(endpoint string) (string, string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("OpenAI endpoint is invalid")
	}
	return parsed.Scheme + "://" + parsed.Host, parsed.EscapedPath(), nil
}
