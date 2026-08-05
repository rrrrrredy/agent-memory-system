package agentassessment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	assessmentprompts "github.com/rrrrrredy/agent-memory-system/evals/prompts"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	openAIResponsesEndpoint       = "https://api.openai.com/v1/responses"
	maximumOpenAIAttemptBytes     = 8 << 20
	maximumOpenAIObservationBytes = 40 << 20
)

func RunOpenAIAssessment(
	ctx context.Context, store *ledger.Store, options OpenAIRunOptions,
) (OpenAIRunResult, error) {
	result := OpenAIRunResult{
		SchemaVersion: OpenAIRunResultSchema, Authority: "provisional_only", ArtifactStorage: "local_only",
	}
	if ctx == nil || store == nil {
		return result, errors.New("context and store are required")
	}
	loader := options.loadVerified
	if loader == nil {
		loader = loadVerifiedProjection
	}
	projection, _, payload, payloadBytes, err := loader(store, options.ProjectionID)
	if err != nil {
		return result, err
	}
	result.ProjectionID = projection.ProjectionID
	result.PayloadID = payload.PayloadID
	result.RequestedModel = options.Model
	if options.ConfirmRemoteDisclosureID != payload.PayloadID {
		return result, errors.New("remote disclosure confirmation must exactly match the payload id")
	}
	if !safeAPIKey(options.APIKey) {
		return result, errors.New("OPENAI_API_KEY is missing or invalid")
	}
	scan, err := scanBlindPayload(payload)
	if err != nil {
		return result, err
	}
	if scan.Findings != 0 {
		return result, fmt.Errorf("remote assessment blocked by %d sensitive finding(s)", scan.Findings)
	}
	requestBytes, outputSchemaSHA, err := buildOpenAIRequest(options.Model, payload, payloadBytes)
	if err != nil {
		return result, err
	}
	endpoint, client, err := openAITransport(options)
	if err != nil {
		return result, err
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	startedAt := now().UTC()
	attempt, attemptBytes, err := buildOpenAIAttempt(projection, payload, payloadBytes,
		options.Model, endpoint, outputSchemaSHA, requestBytes, scan, startedAt)
	if err != nil {
		return result, err
	}
	attemptPaths, _, err := writeImmutableArtifactSet(store, "agent-openai-attempts", attempt.AttemptID,
		map[string][]byte{"attempt.json": attemptBytes, "request.json": requestBytes}, maximumOpenAIAttemptBytes)
	if err != nil {
		return result, err
	}
	result.AttemptID = attempt.AttemptID
	result.AttemptPath = attemptPaths["attempt.json"]
	attempt, err = verifyStoredOpenAIAttempt(store, attempt.AttemptID, endpoint)
	if err != nil {
		return result, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBytes))
	if err != nil {
		return result, errors.New("create OpenAI request")
	}
	request.Header.Set("Authorization", "Bearer "+options.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "agent-memory-system/controlled-assessment")
	response, transportErr := client.Do(request)
	if transportErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		observation := baseOpenAIObservation(attempt, now().UTC())
		observation.Outcome = "transport_error"
		observation.FailureCode = transportFailureCode(transportErr)
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, nil,
			errors.New("OpenAI request failed before a response was received"))
	}
	defer response.Body.Close()
	responseBytes, readErr := io.ReadAll(io.LimitReader(response.Body, maximumOpenAIResponseBytes+1))
	completedAt := now().UTC()
	observation := baseOpenAIObservation(attempt, completedAt)
	observation.HTTPStatus = response.StatusCode
	observation.ContentType = safeHeader(response.Header.Get("Content-Type"))
	observation.OpenAIRequestID = safeHeader(response.Header.Get("X-Request-ID"))
	if readErr == nil && len(responseBytes) <= maximumOpenAIResponseBytes {
		observation.ResponseCaptureStatus = "complete"
		observation.ResponseSHA256 = hashBytes(responseBytes)
		observation.ResponseBytes = int64(len(responseBytes))
	} else {
		observation.ResponseCaptureStatus = "incomplete"
		observation.ResponsePrefixSHA256 = hashBytes(responseBytes)
		observation.ResponsePrefixBytes = int64(len(responseBytes))
		observation.Outcome = "invalid_response"
		observation.FailureCode = "response_too_large_or_unreadable"
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes,
			errors.New("OpenAI response could not be preserved completely"))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		observation.Outcome = "http_error"
		observation.FailureCode = "provider_http_error"
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes,
			errors.New("OpenAI returned a non-success status"))
	}
	if !isJSONMediaType(observation.ContentType) {
		observation.Outcome = "invalid_response"
		observation.FailureCode = "unexpected_content_type"
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes,
			errors.New("OpenAI response content type is invalid"))
	}
	envelope, submissionBytes, outcome, extractErr := extractOpenAISubmission(responseBytes)
	observation.ResponseID = safeHeader(envelope.ID)
	observation.ObservedModel = safeHeader(envelope.Model)
	observation.ResponseStatus = safeHeader(envelope.Status)
	observation.Outcome = outcome
	if extractErr != nil {
		observation.FailureCode = outcome
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes, extractErr)
	}
	submission, canonicalSubmission, err := decodeSubmission(bytes.NewReader(submissionBytes))
	if err != nil {
		observation.Outcome = "invalid_submission"
		observation.FailureCode = "submission_decode_failed"
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes,
			errors.New("OpenAI submission is invalid"))
	}
	projection, projectionBytes, payload, payloadBytes, err := loader(store, options.ProjectionID)
	if err != nil || validateSubmission(projection, submission) != nil {
		observation.Outcome = "invalid_submission"
		observation.FailureCode = "submission_validation_failed"
		return storeFailedOpenAIObservation(store, result, attempt, loader, observation, responseBytes,
			errors.New("OpenAI submission does not match the verified payload"))
	}
	observation.Outcome = "validated_submission"
	observation.SubmissionSHA256 = hashBytes(canonicalSubmission)
	observation, observationPath, err := storeOpenAIObservation(store, observation, responseBytes)
	if err != nil {
		return result, err
	}
	result.ObservationID = observation.ObservationID
	result.ObservationPath = observationPath
	result.OpenAIRequestID = observation.OpenAIRequestID
	result.ObservedModel = observation.ObservedModel
	observation, err = verifyStoredOpenAIObservation(store, observation.ObservationID, attempt, loader)
	if err != nil {
		return result, err
	}
	toolsRegistered := false
	assessor := Assessor{
		Kind: "agent", ID: "openai-responses", Provenance: "controlled_observation",
		Provider: "openai", RequestedModel: options.Model, ObservedModel: observation.ObservedModel,
	}
	harness := HarnessProvenance{
		Version: OpenAIHarnessVersion, PromptSHA256: assessmentprompts.AgentAssessmentV1SHA256(),
		Source: "controlled_openai_responses", IsolationStatus: "request_policy_observed",
		ToolsRegistered: &toolsRegistered, DataDisclosure: "remote",
		ExtractedSubmissionSHA256: hashBytes(canonicalSubmission),
		AttemptID:                 attempt.AttemptID, AttemptContentSHA256: attempt.AttemptContentSHA256,
		ObservationID:            observation.ObservationID,
		ObservationContentSHA256: observation.ObservationContentSHA256,
	}
	assessment, err := storeAssessment(store, projection, projectionBytes, payload, payloadBytes,
		submission, assessor, harness, completedAt.Format(time.RFC3339Nano))
	if err != nil {
		return result, err
	}
	if err := verifyControlledAssessment(store, assessment.AssessmentID, endpoint, loader); err != nil {
		return result, err
	}
	result.Assessment = &assessment
	return result, nil
}

func buildOpenAIAttempt(
	projection Projection, payload BlindPayload, payloadBytes []byte, model, endpoint,
	outputSchemaSHA string, requestBytes []byte, scan RemoteSecretScanObservation, startedAt time.Time,
) (OpenAIAttempt, []byte, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return OpenAIAttempt{}, nil, errors.New("OpenAI endpoint is invalid")
	}
	attempt := OpenAIAttempt{
		SchemaVersion: OpenAIAttemptSchema, ProjectionID: projection.ProjectionID,
		PayloadID: payload.PayloadID, PayloadContentSHA256: payload.PayloadContentSHA256,
		PayloadFileSHA256: hashBytes(payloadBytes), RequestedModel: model,
		EndpointOrigin: parsed.Scheme + "://" + parsed.Host, EndpointPath: parsed.EscapedPath(),
		PromptSHA256:       assessmentprompts.AgentAssessmentV1SHA256(),
		OutputSchemaSHA256: outputSchemaSHA, RequestSHA256: hashBytes(requestBytes),
		RequestBytes: int64(len(requestBytes)),
		Disclosure: RemoteDisclosureObservation{
			Confirmed: true, ConfirmationPayloadID: payload.PayloadID,
			DataClassification: payload.DataClassification,
			Destination:        parsed.Scheme + "://" + parsed.Host,
			ConfirmedAt:        startedAt.Format(time.RFC3339Nano),
		},
		SecretScan: scan,
		RequestPolicy: ResponsesRequestPolicy{
			Store: false, ToolsRegistered: false, ToolChoice: "none",
			ReasoningEffort: "none",
			ResponseFormat:  "json_schema", StrictOutput: true, Background: false,
			ConversationState: false, Streaming: false, Truncation: "disabled",
			AutomaticRetries: 0, EnvironmentProxy: false, Redirects: false,
		},
		StartedAt: startedAt.Format(time.RFC3339Nano), Authority: "provisional_only",
		ArtifactStorage: "local_only",
	}
	identityBytes, err := json.Marshal(attempt)
	if err != nil {
		return OpenAIAttempt{}, nil, err
	}
	attempt.AttemptContentSHA256 = hashBytes(identityBytes)
	attempt.AttemptID = "agent-openai-attempt-" + attempt.AttemptContentSHA256
	data, err := marshalIndented(attempt)
	return attempt, data, err
}

func baseOpenAIObservation(attempt OpenAIAttempt, completedAt time.Time) OpenAIObservation {
	return OpenAIObservation{
		SchemaVersion: OpenAIObservationSchema, AttemptID: attempt.AttemptID,
		AttemptContentSHA256: attempt.AttemptContentSHA256, RequestSHA256: attempt.RequestSHA256,
		RequestedModel: attempt.RequestedModel, CompletedAt: completedAt.Format(time.RFC3339Nano),
		ResponseCaptureStatus: "none",
		Authority:             "provisional_only", ArtifactStorage: "local_only",
	}
}

func storeFailedOpenAIObservation(
	store *ledger.Store, result OpenAIRunResult, attempt OpenAIAttempt,
	loader func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error),
	observation OpenAIObservation,
	responseBytes []byte, returned error,
) (OpenAIRunResult, error) {
	stored, path, err := storeOpenAIObservation(store, observation, responseBytes)
	if err != nil {
		return result, errors.Join(returned, err)
	}
	result.ObservationID = stored.ObservationID
	result.ObservationPath = path
	result.OpenAIRequestID = stored.OpenAIRequestID
	result.ObservedModel = stored.ObservedModel
	if _, verifyErr := verifyStoredOpenAIObservation(store, stored.ObservationID, attempt, loader); verifyErr != nil {
		return result, errors.Join(returned, verifyErr)
	}
	return result, returned
}

func storeOpenAIObservation(
	store *ledger.Store, observation OpenAIObservation, responseBytes []byte,
) (OpenAIObservation, string, error) {
	identityBytes, err := json.Marshal(observation)
	if err != nil {
		return observation, "", err
	}
	observation.ObservationContentSHA256 = hashBytes(identityBytes)
	observation.ObservationID = "agent-openai-observation-" + observation.ObservationContentSHA256
	data, err := marshalIndented(observation)
	if err != nil {
		return observation, "", err
	}
	files := map[string][]byte{"observation.json": data}
	if observation.ResponseCaptureStatus == "complete" {
		files["response.json"] = responseBytes
	} else if observation.ResponseCaptureStatus == "incomplete" {
		files["response-prefix.bin"] = responseBytes
	}
	paths, _, err := writeImmutableArtifactSet(store, "agent-openai-observations",
		observation.ObservationID, files, maximumOpenAIObservationBytes)
	if err != nil {
		return observation, "", err
	}
	return observation, paths["observation.json"], nil
}

func openAITransport(options OpenAIRunOptions) (string, *http.Client, error) {
	if options.endpoint != "" || options.client != nil {
		if options.endpoint == "" || options.client == nil {
			return "", nil, errors.New("test OpenAI transport injection is incomplete")
		}
		return options.endpoint, options.client, nil
	}
	return openAIResponsesEndpoint, newOpenAIHTTPClient(), nil
}

func newOpenAIHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: nil, DisableCompression: true,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport, Timeout: 10 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func safeAPIKey(value string) bool {
	return value != "" && len(value) <= 512 && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func safeHeader(value string) string {
	value = strings.TrimSpace(value)
	if !safeMetadata(value) {
		return ""
	}
	return value
}

func transportFailureCode(err error) string {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	return "transport_error"
}
