package agentassessment

import (
	"net/http"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	OpenAIAttemptSchema     = "legacy-agent-assessment-openai-attempt/v1alpha1"
	OpenAIObservationSchema = "legacy-agent-assessment-openai-observation/v1alpha1"
	OpenAIRunResultSchema   = "legacy-agent-assessment-openai-run-result/v1alpha1"
	OpenAIHarnessVersion    = "controlled-openai-responses/v1alpha1"
)

type ResponsesRequestPolicy struct {
	Store             bool   `json:"store"`
	ToolsRegistered   bool   `json:"tools_registered"`
	ToolChoice        string `json:"tool_choice"`
	ReasoningEffort   string `json:"reasoning_effort"`
	ResponseFormat    string `json:"response_format"`
	StrictOutput      bool   `json:"strict_output"`
	Background        bool   `json:"background"`
	ConversationState bool   `json:"conversation_state"`
	Streaming         bool   `json:"streaming"`
	Truncation        string `json:"truncation"`
	AutomaticRetries  int    `json:"automatic_retries"`
	EnvironmentProxy  bool   `json:"environment_proxy"`
	Redirects         bool   `json:"redirects"`
}

type RemoteDisclosureObservation struct {
	Confirmed             bool   `json:"confirmed"`
	ConfirmationPayloadID string `json:"confirmation_payload_id"`
	DataClassification    string `json:"data_classification"`
	Destination           string `json:"destination"`
	ConfirmedAt           string `json:"confirmed_at"`
}

type RemoteSecretScanObservation struct {
	ScannerVersion   string   `json:"scanner_version"`
	FieldsScanned    int      `json:"fields_scanned"`
	UniqueTexts      int      `json:"unique_texts"`
	BytesScanned     int64    `json:"bytes_scanned"`
	ContentSetSHA256 string   `json:"content_set_sha256"`
	Findings         int      `json:"findings"`
	Categories       []string `json:"categories"`
}

type OpenAIAttempt struct {
	SchemaVersion        string                      `json:"schema_version"`
	AttemptID            string                      `json:"attempt_id"`
	AttemptContentSHA256 string                      `json:"attempt_content_sha256"`
	ProjectionID         string                      `json:"projection_id"`
	PayloadID            string                      `json:"payload_id"`
	PayloadContentSHA256 string                      `json:"payload_content_sha256"`
	PayloadFileSHA256    string                      `json:"payload_file_sha256"`
	RequestedModel       string                      `json:"requested_model"`
	EndpointOrigin       string                      `json:"endpoint_origin"`
	EndpointPath         string                      `json:"endpoint_path"`
	PromptSHA256         string                      `json:"prompt_sha256"`
	OutputSchemaSHA256   string                      `json:"output_schema_sha256"`
	RequestSHA256        string                      `json:"request_sha256"`
	RequestBytes         int64                       `json:"request_bytes"`
	Disclosure           RemoteDisclosureObservation `json:"disclosure"`
	SecretScan           RemoteSecretScanObservation `json:"secret_scan"`
	RequestPolicy        ResponsesRequestPolicy      `json:"request_policy"`
	StartedAt            string                      `json:"started_at"`
	Authority            string                      `json:"authority"`
	ArtifactStorage      string                      `json:"artifact_storage"`
}

type OpenAIObservation struct {
	SchemaVersion            string `json:"schema_version"`
	ObservationID            string `json:"observation_id"`
	ObservationContentSHA256 string `json:"observation_content_sha256"`
	AttemptID                string `json:"attempt_id"`
	AttemptContentSHA256     string `json:"attempt_content_sha256"`
	RequestSHA256            string `json:"request_sha256"`
	HTTPStatus               int    `json:"http_status"`
	ResponseCaptureStatus    string `json:"response_capture_status"`
	ResponseSHA256           string `json:"response_sha256,omitempty"`
	ResponseBytes            int64  `json:"response_bytes"`
	ResponsePrefixSHA256     string `json:"response_prefix_sha256,omitempty"`
	ResponsePrefixBytes      int64  `json:"response_prefix_bytes"`
	ContentType              string `json:"content_type,omitempty"`
	OpenAIRequestID          string `json:"openai_request_id,omitempty"`
	ResponseID               string `json:"response_id,omitempty"`
	RequestedModel           string `json:"requested_model"`
	ObservedModel            string `json:"observed_model,omitempty"`
	ResponseStatus           string `json:"response_status,omitempty"`
	Outcome                  string `json:"outcome"`
	FailureCode              string `json:"failure_code,omitempty"`
	SubmissionSHA256         string `json:"submission_sha256,omitempty"`
	CompletedAt              string `json:"completed_at"`
	Authority                string `json:"authority"`
	ArtifactStorage          string `json:"artifact_storage"`
}

type OpenAIRunOptions struct {
	ProjectionID              string
	Model                     string
	ConfirmRemoteDisclosureID string
	APIKey                    string
	client                    *http.Client
	endpoint                  string
	now                       func() time.Time
	loadVerified              func(*ledger.Store, string) (Projection, []byte, BlindPayload, []byte, error)
}

type OpenAIRunResult struct {
	SchemaVersion   string            `json:"schema_version"`
	ProjectionID    string            `json:"projection_id,omitempty"`
	PayloadID       string            `json:"payload_id,omitempty"`
	AttemptID       string            `json:"attempt_id,omitempty"`
	AttemptPath     string            `json:"attempt_path,omitempty"`
	ObservationID   string            `json:"observation_id,omitempty"`
	ObservationPath string            `json:"observation_path,omitempty"`
	OpenAIRequestID string            `json:"openai_request_id,omitempty"`
	RequestedModel  string            `json:"requested_model,omitempty"`
	ObservedModel   string            `json:"observed_model,omitempty"`
	Assessment      *AssessmentResult `json:"assessment,omitempty"`
	Authority       string            `json:"authority"`
	ArtifactStorage string            `json:"artifact_storage"`
}
