package agentassessment

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestControlledOpenAIRunPreservesExactExchangeAndCreatesProvisionalAssessment(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, submission := openAITestFixture(t,
		"The user requires local evidence storage.")
	submissionBytes, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes := openAITestResponse(t, "resp_test", "gpt-5.4-mini-2026-01-01", submissionBytes)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/" {
			t.Errorf("unexpected request target: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer sk-test-key-not-real" {
			t.Error("authorization header was not attached to the intended request")
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		assertOpenAIRequestContract(t, body, payloadBytes)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "req_test")
		_, _ = writer.Write(responseBytes)
	}))
	defer server.Close()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	result, err := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
		ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
		ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
		client: newOpenAIHTTPClient(), endpoint: server.URL, now: func() time.Time { return fixed },
		loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || result.Assessment == nil || result.Assessment.AssessmentID == "" ||
		result.OpenAIRequestID != "req_test" || result.ObservedModel != "gpt-5.4-mini-2026-01-01" {
		t.Fatalf("controlled run did not close: %+v hits=%d", result, hits.Load())
	}
	requestData, err := os.ReadFile(filepath.Join(filepath.Dir(result.AttemptPath), "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	responseData, err := os.ReadFile(filepath.Join(filepath.Dir(result.ObservationPath), "response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(responseData, responseBytes) {
		t.Fatal("exact provider response bytes were not preserved")
	}
	var attempt OpenAIAttempt
	if data, readErr := os.ReadFile(result.AttemptPath); readErr != nil {
		t.Fatal(readErr)
	} else if decodeErr := json.Unmarshal(data, &attempt); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	var observation OpenAIObservation
	if data, readErr := os.ReadFile(result.ObservationPath); readErr != nil {
		t.Fatal(readErr)
	} else if decodeErr := json.Unmarshal(data, &observation); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if attempt.RequestSHA256 != hashBytes(requestData) || observation.ResponseSHA256 != hashBytes(responseData) ||
		observation.ResponseCaptureStatus != "complete" || observation.AttemptID != attempt.AttemptID ||
		observation.RequestSHA256 != attempt.RequestSHA256 {
		t.Fatal("stored attempt and observation do not bind the exact exchange")
	}
	for _, data := range [][]byte{requestData, responseData} {
		if bytes.Contains(data, []byte("sk-test-key-not-real")) {
			t.Fatal("API key was written to a local artifact")
		}
	}
	assessmentData, err := os.ReadFile(result.Assessment.AssessmentPath)
	if err != nil {
		t.Fatal(err)
	}
	var assessment Assessment
	if err := json.Unmarshal(assessmentData, &assessment); err != nil {
		t.Fatal(err)
	}
	if assessment.Assessor.Provenance != "controlled_observation" ||
		assessment.Harness.Source != "controlled_openai_responses" ||
		assessment.Harness.ToolsRegistered == nil || *assessment.Harness.ToolsRegistered ||
		assessment.Authority != "provisional_only" {
		t.Fatalf("controlled provenance is unsafe: %+v", assessment)
	}
	records := 0
	if err := store.VisitRecords(func(ledger.Record) error { records++; return nil }); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatal("controlled assessment appended to the evidence ledger")
	}
	verifiedAttempt, err := verifyStoredOpenAIAttempt(store, result.AttemptID, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	loader := fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes)
	if _, err := verifyStoredOpenAIObservation(store, result.ObservationID, verifiedAttempt, loader); err != nil {
		t.Fatal(err)
	}
	forged := observation
	forged.HTTPStatus = http.StatusTooManyRequests
	forged.ObservationID = ""
	forged.ObservationContentSHA256 = ""
	forgedDigest, err := identityDigest(forged)
	if err != nil {
		t.Fatal(err)
	}
	forged.ObservationContentSHA256 = forgedDigest
	forged.ObservationID = "agent-openai-observation-" + forgedDigest
	forgedData, err := marshalIndented(forged)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := writeImmutableArtifactSet(store, "agent-openai-observations", forged.ObservationID,
		map[string][]byte{"observation.json": forgedData, "response.json": responseData},
		maximumOpenAIObservationBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyStoredOpenAIObservation(store, forged.ObservationID, verifiedAttempt, loader); err == nil {
		t.Fatal("OpenAI observation verifier accepted a forged validated HTTP error")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(result.AttemptPath), "request.json"),
		append(requestData, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyStoredOpenAIAttempt(store, result.AttemptID, server.URL); err == nil {
		t.Fatal("OpenAI attempt verifier accepted a modified request")
	}
}

func TestControlledOpenAIRunBlocksDisclosureAndSensitiveTextBeforeNetwork(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		confirmation string
		apiKey       string
		model        string
	}{
		{name: "wrong confirmation", text: "Clean local evidence.", confirmation: "agent-payload-" + strings.Repeat("f", 64), apiKey: "sk-test-key-not-real"},
		{name: "sensitive text", text: "token=ghp_" + strings.Repeat("A1", 18), apiKey: "sk-test-key-not-real"},
		{name: "missing API key", text: "Clean local evidence."},
		{name: "unsupported reasoning policy", text: "Clean local evidence.", apiKey: "sk-test-key-not-real", model: "gpt-5-mini"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, projectionBytes, payload, payloadBytes, _ := openAITestFixture(t, test.text)
			confirmation := test.confirmation
			if confirmation == "" {
				confirmation = payload.PayloadID
			}
			model := test.model
			if model == "" {
				model = "gpt-5.4-mini"
			}
			var hits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				hits.Add(1)
			}))
			defer server.Close()
			store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
				ProjectionID: projection.ProjectionID, Model: model,
				ConfirmRemoteDisclosureID: confirmation, Credential: test.apiKey,
				client: server.Client(), endpoint: server.URL,
				loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
			})
			if err == nil || hits.Load() != 0 {
				t.Fatalf("unsafe preflight reached network: err=%v hits=%d", err, hits.Load())
			}
			if _, statErr := os.Stat(filepath.Join(store.Root(), "derived", "evaluations", "agent-openai-attempts")); !os.IsNotExist(statErr) {
				t.Fatal("failed remote preflight created an attempt artifact")
			}
			assertEmptyLedger(t, store)
		})
	}
}

func TestControlledOpenAIRunRejectsRedirectWithoutForwardingAuthorization(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, _ := openAITestFixture(t, "Clean local evidence.")
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHits.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
		ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
		ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
		client: newOpenAIHTTPClient(), endpoint: source.URL,
		loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
	})
	if err == nil || targetHits.Load() != 0 || result.ObservationID == "" ||
		result.Assessment != nil {
		t.Fatalf("redirect boundary failed: result=%+v err=%v target_hits=%d", result, err, targetHits.Load())
	}
	var observation OpenAIObservation
	data, readErr := os.ReadFile(result.ObservationPath)
	if readErr != nil || json.Unmarshal(data, &observation) != nil ||
		observation.HTTPStatus != http.StatusTemporaryRedirect || observation.Outcome != "http_error" ||
		observation.FailureCode != "provider_http_error" || observation.ResponseCaptureStatus != "complete" {
		t.Fatal("redirect response was not preserved as an HTTP observation")
	}
	if _, readErr := os.ReadFile(filepath.Join(filepath.Dir(result.ObservationPath), "response.json")); readErr != nil {
		t.Fatal("redirect response body was not preserved")
	}
	assertEmptyLedger(t, store)
}

func TestControlledOpenAIRunPreservesToolOutputButDoesNotAssessIt(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, _ := openAITestFixture(t, "Clean local evidence.")
	responseBytes := []byte(`{"id":"resp_tool","object":"response","model":"gpt-5.4-mini","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"web_search_call"}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(responseBytes)
	}))
	defer server.Close()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
		ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
		ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
		client: server.Client(), endpoint: server.URL,
		loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
	})
	if err == nil || result.ObservationID == "" || result.Assessment != nil {
		t.Fatalf("tool output created authority: result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(result.ObservationPath), "response.json"))
	if err != nil || !bytes.Equal(data, responseBytes) {
		t.Fatal("rejected response was not preserved exactly")
	}
}

func TestControlledOpenAIRunPreservesRejectedResponsesWithoutAssessment(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, validSubmission := openAITestFixture(t, "Clean local evidence.")
	invalidCoverage := validSubmission
	invalidCoverage.CandidateAssessments = nil
	invalidCoverageBytes, err := json.Marshal(invalidCoverage)
	if err != nil {
		t.Fatal(err)
	}
	validSubmissionBytes, err := json.Marshal(validSubmission)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		statusCode  int
		contentType string
		response    []byte
		outcome     string
		failureCode string
	}{
		{name: "http error", statusCode: http.StatusTooManyRequests,
			response: []byte(`{"error":{"type":"rate_limit_error"}}`),
			outcome:  "http_error", failureCode: "provider_http_error"},
		{name: "refusal", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_refusal","object":"response","model":"gpt-5.4-mini","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"cannot assess"}]}]}`),
			outcome:  "refusal", failureCode: "refusal"},
		{name: "incomplete", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_incomplete","object":"response","model":"gpt-5.4-mini","status":"incomplete","error":null,"incomplete_details":{"reason":"max_output_tokens"},"output":[]}`),
			outcome:  "incomplete", failureCode: "incomplete"},
		{name: "provider failed", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_failed","object":"response","model":"gpt-5.4-mini","status":"failed","error":{"type":"server_error"},"incomplete_details":null,"output":[]}`),
			outcome:  "provider_response_failed", failureCode: "provider_response_failed"},
		{name: "missing explicit null state", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_missing","object":"response","model":"gpt-5.4-mini","status":"completed","output":[]}`),
			outcome:  "invalid_response", failureCode: "invalid_response"},
		{name: "incomplete output message", statusCode: http.StatusOK,
			response: openAITestResponseWithMessageStatus(t, "resp_message_incomplete", "gpt-5.4-mini",
				"incomplete", validSubmissionBytes), outcome: "invalid_response", failureCode: "invalid_response"},
		{name: "invalid response object", statusCode: http.StatusOK,
			response: bytes.Replace(openAITestResponse(t, "resp_object", "gpt-5.4-mini", validSubmissionBytes),
				[]byte(`"object":"response"`), []byte(`"object":"list"`), 1),
			outcome: "invalid_response", failureCode: "invalid_response"},
		{name: "function call", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_function","object":"response","model":"gpt-5.4-mini","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"function_call","name":"unsafe"}]}`),
			outcome:  "tool_output", failureCode: "tool_output"},
		{name: "duplicate JSON key", statusCode: http.StatusOK,
			response: []byte(`{"id":"resp_one","id":"resp_two","object":"response","model":"gpt-5.4-mini","status":"completed","error":null,"incomplete_details":null,"output":[]}`),
			outcome:  "invalid_response", failureCode: "invalid_response"},
		{name: "invalid UTF-8", statusCode: http.StatusOK,
			response: append([]byte(`{"id":"resp_utf8","object":"response","model":"gpt-5.4-mini","status":"completed","error":null,"incomplete_details":null,"output":[],"bad":"`),
				append([]byte{0xff}, []byte(`"}`)...)...),
			outcome: "invalid_response", failureCode: "invalid_response"},
		{name: "unexpected content type", statusCode: http.StatusOK, contentType: "application/jsonp",
			response: openAITestResponse(t, "resp_text", "gpt-5.4-mini", validSubmissionBytes),
			outcome:  "invalid_response", failureCode: "unexpected_content_type"},
		{name: "invalid coverage", statusCode: http.StatusOK,
			response: openAITestResponse(t, "resp_invalid", "gpt-5.4-mini", invalidCoverageBytes),
			outcome:  "invalid_submission", failureCode: "submission_validation_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				contentType := test.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				writer.Header().Set("Content-Type", contentType)
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write(test.response)
			}))
			defer server.Close()
			store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
				ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
				ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
				client: server.Client(), endpoint: server.URL,
				loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
			})
			if runErr == nil || result.AttemptID == "" || result.ObservationID == "" || result.Assessment != nil {
				t.Fatalf("rejected response created authority: result=%+v err=%v", result, runErr)
			}
			stored, err := os.ReadFile(filepath.Join(filepath.Dir(result.ObservationPath), "response.json"))
			if err != nil || !bytes.Equal(stored, test.response) {
				t.Fatal("rejected response was not preserved exactly")
			}
			var observation OpenAIObservation
			data, err := os.ReadFile(result.ObservationPath)
			if err != nil || json.Unmarshal(data, &observation) != nil ||
				observation.HTTPStatus != test.statusCode || observation.ResponseCaptureStatus != "complete" ||
				observation.Outcome != test.outcome || observation.FailureCode != test.failureCode ||
				observation.ResponseSHA256 != hashBytes(test.response) {
				t.Fatalf("rejected response observation is inaccurate: %+v", observation)
			}
			if test.name == "invalid coverage" {
				forged := observation
				forged.Outcome = "validated_submission"
				forged.FailureCode = ""
				forged.SubmissionSHA256 = hashBytes(invalidCoverageBytes)
				forged.ObservationID = ""
				forged.ObservationContentSHA256 = ""
				digest, err := identityDigest(forged)
				if err != nil {
					t.Fatal(err)
				}
				forged.ObservationContentSHA256 = digest
				forged.ObservationID = "agent-openai-observation-" + digest
				forgedData, err := marshalIndented(forged)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := writeImmutableArtifactSet(store, "agent-openai-observations", forged.ObservationID,
					map[string][]byte{"observation.json": forgedData, "response.json": test.response},
					maximumOpenAIObservationBytes); err != nil {
					t.Fatal(err)
				}
				attempt, err := verifyStoredOpenAIAttempt(store, result.AttemptID, server.URL)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := verifyStoredOpenAIObservation(store, forged.ObservationID, attempt,
					fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes)); err == nil {
					t.Fatal("observation verifier accepted invalid coverage as validated")
				}
			}
			assertEmptyLedger(t, store)
		})
	}
}

func TestControlledOpenAIRunPreservesTransportFailureWithoutAssessment(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, _ := openAITestFixture(t, "Clean local evidence.")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
		ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
		ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
		client: client, endpoint: "https://api.openai.invalid/v1/responses",
		loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
	})
	if runErr == nil || result.AttemptID == "" || result.ObservationID == "" || result.Assessment != nil {
		t.Fatalf("transport failure created authority: result=%+v err=%v", result, runErr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(result.ObservationPath), "response.json")); !os.IsNotExist(err) {
		t.Fatal("transport failure fabricated a provider response")
	}
	var observation OpenAIObservation
	data, err := os.ReadFile(result.ObservationPath)
	if err != nil || json.Unmarshal(data, &observation) != nil || observation.FailureCode != "timeout" {
		t.Fatal("transport failure observation is incomplete")
	}
	assertEmptyLedger(t, store)
}

func TestControlledOpenAIRunMarksPartialResponseCapture(t *testing.T) {
	projection, projectionBytes, payload, payloadBytes, _ := openAITestFixture(t, "Clean local evidence.")
	prefix := []byte(`{"id":"truncated"`)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: &failingReadCloser{reader: bytes.NewReader(prefix)},
		}, nil
	})}
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := RunOpenAIAssessment(context.Background(), store, OpenAIRunOptions{
		ProjectionID: projection.ProjectionID, Model: "gpt-5.4-mini",
		ConfirmRemoteDisclosureID: payload.PayloadID, Credential: "sk-test-key-not-real",
		client: client, endpoint: "https://api.openai.invalid/v1/responses",
		loadVerified: fixedOpenAILoader(projection, projectionBytes, payload, payloadBytes),
	})
	if runErr == nil || result.ObservationID == "" || result.Assessment != nil {
		t.Fatalf("partial response capture created authority: result=%+v err=%v", result, runErr)
	}
	var observation OpenAIObservation
	data, err := os.ReadFile(result.ObservationPath)
	if err != nil || json.Unmarshal(data, &observation) != nil ||
		observation.ResponseCaptureStatus != "incomplete" || observation.ResponseSHA256 != "" ||
		observation.ResponsePrefixSHA256 != hashBytes(prefix) ||
		observation.ResponsePrefixBytes != int64(len(prefix)) {
		t.Fatalf("partial response capture was misrepresented: %+v", observation)
	}
	stored, err := os.ReadFile(filepath.Join(filepath.Dir(result.ObservationPath), "response-prefix.bin"))
	if err != nil || !bytes.Equal(stored, prefix) {
		t.Fatal("partial response prefix was not preserved exactly")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(result.ObservationPath), "response.json")); !os.IsNotExist(err) {
		t.Fatal("partial response was mislabeled as a complete response")
	}
	assertEmptyLedger(t, store)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type failingReadCloser struct {
	reader *bytes.Reader
}

func (reader *failingReadCloser) Read(destination []byte) (int, error) {
	if reader.reader.Len() != 0 {
		return reader.reader.Read(destination)
	}
	return 0, io.ErrUnexpectedEOF
}

func (*failingReadCloser) Close() error { return nil }

func assertEmptyLedger(t *testing.T, store *ledger.Store) {
	t.Helper()
	records := 0
	if err := store.VisitRecords(func(ledger.Record) error { records++; return nil }); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatal("controlled assessment changed the evidence ledger")
	}
}

func assertOpenAIRequestContract(t *testing.T, body map[string]any, payloadBytes []byte) {
	t.Helper()
	wanted := []string{"input", "max_output_tokens", "model", "reasoning", "store", "text", "tool_choice", "tools", "truncation"}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, wanted) || body["store"] != false || body["tool_choice"] != "none" ||
		body["truncation"] != "disabled" || len(body["tools"].([]any)) != 0 {
		t.Fatalf("unsafe OpenAI request contract: keys=%v body=%v", keys, body)
	}
	if body["reasoning"].(map[string]any)["effort"] != "none" {
		t.Fatal("OpenAI request did not disable reasoning-token consumption")
	}
	inputs := body["input"].([]any)
	user := inputs[1].(map[string]any)
	content := user["content"].([]any)[0].(map[string]any)
	if content["text"] != string(payloadBytes) {
		t.Fatal("OpenAI request did not contain exactly the canonical blind payload")
	}
	format := body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true {
		t.Fatal("OpenAI structured output was not strict")
	}
	for _, forbidden := range []string{"background", "conversation", "metadata", "previous_response_id", "prompt", "stream"} {
		if _, exists := body[forbidden]; exists {
			t.Fatalf("OpenAI request exposed forbidden field %q", forbidden)
		}
	}
}

func TestRemoteAssessmentRejectsInfeasibleCompleteOutput(t *testing.T) {
	payload := BlindPayload{PayloadID: "agent-payload-" + strings.Repeat("a", 64)}
	item := BlindCompactionItem{ItemID: "agent-item-" + strings.Repeat("b", 64)}
	unit := BlindCompactionUnit{UnitID: "assessment-unit-" + strings.Repeat("c", 64)}
	for index := 0; index < 1000; index++ {
		item.Units = append(item.Units, unit)
	}
	payload.CompactionItems = []BlindCompactionItem{item}
	if err := validateRemoteOutputFeasibility(payload); err == nil ||
		!strings.Contains(err.Error(), "smaller review queue") {
		t.Fatal("infeasible exact-coverage output was allowed to reach the provider")
	}
}

func TestRemoteAssessmentRequiresAReasoningNoneCapableModel(t *testing.T) {
	if !supportsReasoningNone("gpt-5.4-mini") || !supportsReasoningNone("gpt-5.6-luna-2026-08-01") ||
		supportsReasoningNone("gpt-5-mini") || supportsReasoningNone("gpt-5.4-pro") ||
		supportsReasoningNone("gpt-4.1") {
		t.Fatal("reasoning-none model capability gate is unsafe")
	}
}

func openAITestFixture(t *testing.T, evidenceText string) (Projection, []byte, BlindPayload, []byte, Submission) {
	t.Helper()
	projection, submission := assessmentFixture()
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	projection.SchemaVersion = ProjectionSchema
	projection.ProjectionContentSHA256 = hashA
	projection.EvidenceBlocks = []EvidenceBlock{
		{BlockID: "blind-evidence-" + hashA, Text: evidenceText},
		{BlockID: "blind-evidence-" + hashB, Text: "The compacted representation keeps the same rule."},
	}
	projection.CandidateItems[0].Text = "Keep raw evidence local."
	projection.CompactionItems[0].Units[0].Statement = "Keep raw evidence local."
	payload, payloadBytes, err := buildBlindPayload(projection)
	if err != nil {
		t.Fatal(err)
	}
	projection.PayloadID = payload.PayloadID
	projection.PayloadContentSHA256 = payload.PayloadContentSHA256
	projectionBytes, err := marshalIndented(projection)
	if err != nil {
		t.Fatal(err)
	}
	submission.PayloadID = payload.PayloadID
	return projection, projectionBytes, payload, payloadBytes, submission
}

func fixedOpenAILoader(
	projection Projection, projectionBytes []byte, payload BlindPayload, payloadBytes []byte,
) func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error) {
	return func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error) {
		return projection, projectionBytes, payload, payloadBytes, nil
	}
}

func openAITestResponse(t *testing.T, responseID, model string, submission []byte) []byte {
	return openAITestResponseWithMessageStatus(t, responseID, model, "completed", submission)
}

func openAITestResponseWithMessageStatus(
	t *testing.T, responseID, model, messageStatus string, submission []byte,
) []byte {
	t.Helper()
	value := map[string]any{
		"id": responseID, "object": "response", "model": model, "status": "completed",
		"error": nil, "incomplete_details": nil,
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "status": messageStatus,
			"content": []any{map[string]any{"type": "output_text", "text": string(submission)}},
		}},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
