package agentbridge

import (
	"fmt"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	RunRequestSchema       = "native-agent-run-request/v1alpha1"
	StartedSchema          = "native-agent-execution-start/v1alpha1"
	ReceiptSchema          = "native-agent-execution-receipt/v1alpha1"
	RunResultSchema        = "native-agent-execution-result/v1alpha1"
	VerificationSchema     = "native-agent-execution-verification/v1alpha1"
	AdapterVersion         = "native-agent-bridge/v1alpha1"
	StartedMediaType       = "application/vnd.agentmem.native-agent-start+json"
	ReceiptMediaType       = "application/vnd.agentmem.native-agent-receipt+json"
	RawCodexJSONLMediaType = "application/vnd.openai.codex.exec-event+jsonl"
	PrivacyLocalOnly       = "local_only"
	ProviderAuthority      = "local_codex_cli_process; remote_provider_model_and_private_reasoning_not_independently_attested"
)

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
)

type RunRequest struct {
	SchemaVersion           string `json:"schema_version"`
	TaskID                  string `json:"task_id"`
	Prompt                  string `json:"prompt"`
	Model                   string `json:"model"`
	Sandbox                 string `json:"sandbox"`
	WorkingDirectory        string `json:"working_directory"`
	TimeoutSeconds          int    `json:"timeout_seconds"`
	SkipGitRepositoryCheck  bool   `json:"skip_git_repository_check"`
	LoadoutContextReceiptID string `json:"loadout_context_receipt_id,omitempty"`
	Privacy                 string `json:"privacy"`
}

type Artifact struct {
	Name   string         `json:"name"`
	SHA256 string         `json:"sha256"`
	Bytes  int64          `json:"bytes"`
	Blob   ledger.BlobRef `json:"blob"`
}

type BoundEvent struct {
	EventID      string `json:"event_id"`
	RecordSHA256 string `json:"record_sha256"`
}

type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type Started struct {
	SchemaVersion           string                      `json:"schema_version"`
	StartedEventID          string                      `json:"started_event_id"`
	ExecutionID             string                      `json:"execution_id"`
	TaskID                  string                      `json:"task_id"`
	RequestSHA256           string                      `json:"request_sha256"`
	RequestBlob             ledger.BlobRef              `json:"request_blob"`
	PromptSHA256            string                      `json:"prompt_sha256"`
	PromptBlob              ledger.BlobRef              `json:"prompt_blob"`
	CodexExecutable         Artifact                    `json:"codex_executable"`
	CodexVersion            string                      `json:"codex_version"`
	RunnerExecutable        Artifact                    `json:"runner_executable"`
	Arguments               []string                    `json:"arguments"`
	ArgumentsSHA256         string                      `json:"arguments_sha256"`
	EnvironmentPolicy       string                      `json:"environment_policy"`
	EnvironmentNamesSHA256  string                      `json:"environment_names_sha256"`
	EnvironmentNamesCount   int                         `json:"environment_names_count"`
	WorkingDirectorySHA256  string                      `json:"working_directory_sha256"`
	LoadoutContextReceiptID string                      `json:"loadout_context_receipt_id,omitempty"`
	MemoryReferences        []retrieval.MemoryReference `json:"memory_references"`
	StartedAt               time.Time                   `json:"started_at"`
	Privacy                 string                      `json:"privacy"`
}

type Receipt struct {
	SchemaVersion       string                     `json:"schema_version"`
	ReceiptID           string                     `json:"receipt_id"`
	ExecutionID         string                     `json:"execution_id"`
	TaskID              string                     `json:"task_id"`
	Started             BoundEvent                 `json:"started"`
	RawEventsBlob       ledger.BlobRef             `json:"raw_events_blob"`
	AgentMessageBlob    *ledger.BlobRef            `json:"agent_message_blob,omitempty"`
	StderrBlob          *ledger.BlobRef            `json:"stderr_blob,omitempty"`
	ThreadID            string                     `json:"thread_id,omitempty"`
	Events              int                        `json:"events"`
	ToolCalls           int                        `json:"tool_calls"`
	Usage               Usage                      `json:"usage"`
	ProcessExitCode     int                        `json:"process_exit_code"`
	Outcome             Outcome                    `json:"outcome"`
	FailureKind         string                     `json:"failure_kind,omitempty"`
	ReasoningVisibility ledger.ReasoningVisibility `json:"reasoning_visibility"`
	ProviderAuthority   string                     `json:"provider_authority"`
	StartedAt           time.Time                  `json:"started_at"`
	FinishedAt          time.Time                  `json:"finished_at"`
	Privacy             string                     `json:"privacy"`
}

type RunResult struct {
	SchemaVersion string  `json:"schema_version"`
	Receipt       Receipt `json:"receipt"`
	Privacy       string  `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion   string   `json:"schema_version"`
	RecordsChecked  int      `json:"records_checked"`
	BlobsChecked    int      `json:"blobs_checked"`
	ReceiptsChecked int      `json:"receipts_checked"`
	Issues          []string `json:"issues"`
	Privacy         string   `json:"privacy"`
}

type VerifiedExecution struct {
	Request RunRequest
	Started Started
	Receipt Receipt
}

type Options struct {
	CodexPath    string
	PortableRoot string
	Now          func() time.Time
}

type ExecutionError struct {
	ReceiptID   string
	FailureKind string
}

func (err *ExecutionError) Error() string {
	return fmt.Sprintf("native Codex execution failed (%s); receipt %s was preserved", err.FailureKind, err.ReceiptID)
}
