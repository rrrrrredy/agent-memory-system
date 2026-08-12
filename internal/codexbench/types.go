package codexbench

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	PlanSchema          = "codex-memory-benchmark-plan/v1alpha1"
	SealedPlanSchema    = "codex-memory-benchmark-sealed-plan/v1alpha1"
	ReportSchema        = "codex-memory-benchmark-report/v1alpha1"
	VerificationSchema  = "codex-memory-benchmark-verification/v1alpha1"
	PublicReceiptSchema = "codex-memory-benchmark-public-receipt/v1alpha1"
	RunnerVersion       = "codex-memory-benchmark/v1alpha1"
	RawJSONLMediaType   = "application/vnd.openai.codex.exec-event+jsonl"
	ReportMediaType     = "application/vnd.agentmem.codex-memory-benchmark+json"
)

type Plan struct {
	SchemaVersion  string `json:"schema_version"`
	SuiteID        string `json:"suite_id"`
	Model          string `json:"model"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Tasks          []Task `json:"tasks"`
	Privacy        string `json:"privacy"`
}

type Task struct {
	TaskID        string   `json:"task_id"`
	ClusterID     string   `json:"cluster_id"`
	Prompt        string   `json:"prompt"`
	ToolPolicy    string   `json:"tool_policy"`
	MemoryContext string   `json:"memory_context,omitempty"`
	InjectionID   string   `json:"injection_id,omitempty"`
	Workspace     string   `json:"workspace"`
	OracleOverlay string   `json:"oracle_overlay"`
	OracleCommand []string `json:"oracle_command"`
}

type Artifact struct {
	Path   string         `json:"path"`
	SHA256 string         `json:"sha256"`
	Bytes  int64          `json:"bytes"`
	Blob   ledger.BlobRef `json:"blob"`
}

type SealedTask struct {
	TaskID              string                      `json:"task_id"`
	ClusterID           string                      `json:"cluster_id"`
	PromptSHA256        string                      `json:"prompt_sha256"`
	MemorySHA256        string                      `json:"memory_sha256"`
	MemorySource        string                      `json:"memory_source"`
	InjectionID         string                      `json:"injection_id,omitempty"`
	RetrievalReceiptID  string                      `json:"retrieval_receipt_id,omitempty"`
	ToolPolicy          string                      `json:"tool_policy"`
	MemoryReferences    []retrieval.MemoryReference `json:"memory_references"`
	WorkspaceSHA256     string                      `json:"workspace_sha256"`
	WorkspaceFiles      []Artifact                  `json:"workspace_files"`
	OracleOverlaySHA256 string                      `json:"oracle_overlay_sha256"`
	OracleFiles         []Artifact                  `json:"oracle_files"`
	OracleExecutable    Artifact                    `json:"oracle_executable"`
	OracleArguments     []string                    `json:"oracle_arguments"`
	ExecutionOrder      []string                    `json:"execution_order"`
}

type SealedPlan struct {
	SchemaVersion          string         `json:"schema_version"`
	PlanSHA256             string         `json:"plan_sha256"`
	SuiteID                string         `json:"suite_id"`
	Model                  string         `json:"model"`
	TimeoutSeconds         int            `json:"timeout_seconds"`
	CodexExecutable        Artifact       `json:"codex_executable"`
	CodexVersion           string         `json:"codex_version"`
	RunnerVersion          string         `json:"runner_version"`
	RunnerExecutable       Artifact       `json:"runner_executable"`
	EnvironmentPolicy      string         `json:"environment_policy"`
	EnvironmentNamesSHA256 string         `json:"environment_names_sha256"`
	EnvironmentNamesCount  int            `json:"environment_names_count"`
	InputPlanBlob          ledger.BlobRef `json:"input_plan_blob"`
	CreatedAt              time.Time      `json:"created_at"`
	Tasks                  []SealedTask   `json:"tasks"`
	Privacy                string         `json:"privacy"`
}

type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type ArmResult struct {
	TaskID                string          `json:"task_id"`
	Condition             string          `json:"condition"`
	ExecutionOrder        int             `json:"execution_order"`
	ThreadID              string          `json:"thread_id,omitempty"`
	CodexExitCode         int             `json:"codex_exit_code"`
	OracleExitCode        int             `json:"oracle_exit_code"`
	ToolCalls             int             `json:"tool_calls"`
	OraclePassed          bool            `json:"oracle_passed"`
	AgentMessageSHA256    string          `json:"agent_message_sha256,omitempty"`
	RawEventsBlob         ledger.BlobRef  `json:"raw_events_blob"`
	AgentMessageBlob      *ledger.BlobRef `json:"agent_message_blob,omitempty"`
	StderrBlob            *ledger.BlobRef `json:"stderr_blob,omitempty"`
	OracleOutputBlob      ledger.BlobRef  `json:"oracle_output_blob"`
	WorkspaceBeforeSHA256 string          `json:"workspace_before_sha256"`
	WorkspaceAfterSHA256  string          `json:"workspace_after_sha256"`
	Usage                 Usage           `json:"usage"`
	StartedAt             time.Time       `json:"started_at"`
	FinishedAt            time.Time       `json:"finished_at"`
	StartedEventID        string          `json:"started_event_id"`
	ResultEventID         string          `json:"result_event_id"`
	Issues                []string        `json:"issues"`
}

type PairResult struct {
	TaskID       string    `json:"task_id"`
	ClusterID    string    `json:"cluster_id"`
	Baseline     ArmResult `json:"baseline"`
	ToolPolicy   string    `json:"tool_policy"`
	MemorySource string    `json:"memory_source"`
	Treatment    ArmResult `json:"treatment"`
	Outcome      string    `json:"outcome"`
	TokenDelta   int       `json:"token_delta"`
}

type Summary struct {
	Pairs                  int     `json:"pairs"`
	DistinctClusters       int     `json:"distinct_clusters"`
	BaselinePasses         int     `json:"baseline_passes"`
	ToolFreePairs          int     `json:"tool_free_pairs"`
	VerifiedRetrievalPairs int     `json:"verified_retrieval_pairs"`
	TreatmentPasses        int     `json:"treatment_passes"`
	Wins                   int     `json:"wins"`
	Ties                   int     `json:"ties"`
	Losses                 int     `json:"losses"`
	DiscordantPairs        int     `json:"discordant_pairs"`
	OneSidedSignTestP      float64 `json:"one_sided_sign_test_p"`
	SuccessRateDelta       float64 `json:"success_rate_delta"`
	MeanTokenDelta         float64 `json:"mean_token_delta"`
}

type Report struct {
	SchemaVersion string         `json:"schema_version"`
	ReportSHA256  string         `json:"report_sha256"`
	PlanSHA256    string         `json:"plan_sha256"`
	PlanBlob      ledger.BlobRef `json:"plan_blob"`
	InputPlanBlob ledger.BlobRef `json:"input_plan_blob"`
	PlanEventID   string         `json:"plan_event_id"`
	SuiteID       string         `json:"suite_id"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    time.Time      `json:"finished_at"`
	Pairs         []PairResult   `json:"pairs"`
	Summary       Summary        `json:"summary"`
	Authority     string         `json:"authority"`
	EfficacyClaim string         `json:"efficacy_claim"`
	Issues        []string       `json:"issues"`
	Privacy       string         `json:"privacy"`
}

type Verification struct {
	SchemaVersion  string   `json:"schema_version"`
	ReportSHA256   string   `json:"report_sha256,omitempty"`
	PlanSHA256     string   `json:"plan_sha256,omitempty"`
	RecordsChecked int      `json:"records_checked"`
	BlobsChecked   int      `json:"blobs_checked"`
	Issues         []string `json:"issues"`
	Privacy        string   `json:"privacy"`
}

type PublicReceipt struct {
	SchemaVersion          string    `json:"schema_version"`
	ReceiptSHA256          string    `json:"receipt_sha256"`
	SuiteID                string    `json:"suite_id"`
	SuiteSHA256            string    `json:"suite_sha256"`
	ReportSHA256           string    `json:"report_sha256"`
	PlanSHA256             string    `json:"plan_sha256"`
	InputPlanSHA256        string    `json:"input_plan_sha256"`
	CodexExecutableSHA256  string    `json:"codex_executable_sha256"`
	CodexVersion           string    `json:"codex_version"`
	RunnerExecutableSHA256 string    `json:"runner_executable_sha256"`
	RunnerVersion          string    `json:"runner_version"`
	OracleExecutableSHA256 string    `json:"oracle_executable_sha256"`
	ModelSelector          string    `json:"model_selector"`
	ToolPolicy             string    `json:"tool_policy"`
	StartedAt              time.Time `json:"started_at"`
	FinishedAt             time.Time `json:"finished_at"`
	Summary                Summary   `json:"summary"`
	Authority              string    `json:"authority"`
	EfficacyClaim          string    `json:"efficacy_claim"`
	RecordsChecked         int       `json:"records_checked"`
	BlobsChecked           int       `json:"blobs_checked"`
	Limitations            []string  `json:"limitations"`
	Privacy                string    `json:"privacy"`
}

type Options struct {
	PlanPath   string
	CodexPath  string
	OutputRoot string
	Now        func() time.Time
}
