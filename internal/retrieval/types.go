package retrieval

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const (
	RequestSchemaVersion          = "memory-retrieval-request/v1alpha1"
	ResultSchemaVersion           = "memory-retrieval-result/v1alpha1"
	ReceiptSchemaVersion          = "memory-retrieval-receipt/v1alpha1"
	ContextSchemaVersion          = "memory-context/v1alpha1"
	InjectionReceiptSchemaVersion = "memory-injection-receipt/v1alpha1"
	AdoptionRequestSchemaVersion  = "memory-adoption-request/v1alpha1"
	AdoptionReceiptSchemaVersion  = "memory-adoption-receipt/v1alpha1"
	VerificationSchemaVersion     = "memory-receipt-verification/v1alpha1"
	AdapterVersion                = "v1alpha1"
	PrivacyLocalOnly              = "local_only"
	DefaultLimit                  = 5
	MaximumLimit                  = 20
	DefaultTokenBudget            = 800
	MaximumTokenBudget            = 4096
	DefaultByteBudget             = 4096
	MaximumByteBudget             = 64 * 1024
	MaximumQueryBytes             = 64 * 1024
)

type DeliveryChannel string

const (
	ChannelCLI            DeliveryChannel = "cli"
	ChannelMCP            DeliveryChannel = "mcp"
	ChannelCodexHook      DeliveryChannel = "codex_hook"
	ChannelClaudeHook     DeliveryChannel = "claude_code_hook"
	ChannelOpenCodePlugin DeliveryChannel = "opencode_plugin"
	ChannelHarness        DeliveryChannel = "harness"
)

type Context struct {
	Agent      ledger.Agent    `json:"agent"`
	ThreadID   string          `json:"thread_id,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Repository string          `json:"repository,omitempty"`
	Project    string          `json:"project,omitempty"`
	Task       string          `json:"task,omitempty"`
	Channel    DeliveryChannel `json:"channel"`
}

type Request struct {
	SchemaVersion string  `json:"schema_version"`
	Query         string  `json:"query,omitempty"`
	MemoryID      string  `json:"memory_id,omitempty"`
	Context       Context `json:"context"`
	Limit         int     `json:"limit"`
	TokenBudget   int     `json:"token_budget"`
	ByteBudget    int     `json:"byte_budget"`
}

type MemoryReference struct {
	MemoryID   string `json:"memory_id"`
	RevisionID string `json:"revision_id"`
}

type Match struct {
	MemoryReference
	Kind               candidates.CandidateKind `json:"kind"`
	ScopeKind          review.ScopeKind         `json:"scope_kind"`
	ScopeValue         string                   `json:"scope_value"`
	EvidenceBasis      []review.Basis           `json:"evidence_basis"`
	Text               string                   `json:"text"`
	TextSHA256         string                   `json:"text_sha256"`
	Score              int                      `json:"score"`
	MatchedTerms       []string                 `json:"matched_terms"`
	EstimatedTokens    int                      `json:"estimated_tokens"`
	Bytes              int                      `json:"bytes"`
	ScopeSpecificity   int                      `json:"scope_specificity"`
	SelectionRationale []string                 `json:"selection_rationale"`
}

type ExclusionCounts struct {
	ScopeMismatch  int `json:"scope_mismatch"`
	NoLexicalMatch int `json:"no_lexical_match"`
	ResultLimit    int `json:"result_limit"`
	TokenBudget    int `json:"token_budget"`
	ByteBudget     int `json:"byte_budget"`
}

type Result struct {
	SchemaVersion           string          `json:"schema_version"`
	ReceiptID               string          `json:"receipt_id"`
	Status                  string          `json:"status"`
	SelectorSHA256          string          `json:"selector_sha256"`
	RepositoryStateSHA256   string          `json:"repository_state_sha256,omitempty"`
	RepositoryIssues        []string        `json:"repository_issues"`
	ActiveMemories          int             `json:"active_memories"`
	ApplicableMemories      int             `json:"applicable_memories"`
	Selected                []Match         `json:"selected"`
	SelectedEstimatedTokens int             `json:"selected_estimated_tokens"`
	SelectedBytes           int             `json:"selected_bytes"`
	TokenBudget             int             `json:"token_budget"`
	ByteBudget              int             `json:"byte_budget"`
	Exclusions              ExclusionCounts `json:"exclusions"`
	ContextGaps             []string        `json:"context_gaps"`
	Privacy                 string          `json:"privacy"`
}

type Receipt struct {
	SchemaVersion      string    `json:"schema_version"`
	ReceiptID          string    `json:"receipt_id"`
	RecordedAt         time.Time `json:"recorded_at"`
	PortableRoot       string    `json:"portable_root"`
	PortableRootSHA256 string    `json:"portable_root_sha256"`
	Request            Request   `json:"request"`
	RequestSHA256      string    `json:"request_sha256"`
	Result             Result    `json:"result"`
	Privacy            string    `json:"privacy"`
}

type ContextResult struct {
	SchemaVersion   string            `json:"schema_version"`
	InjectionID     string            `json:"injection_id,omitempty"`
	Retrieval       Result            `json:"retrieval"`
	Memories        []MemoryReference `json:"memories"`
	Content         string            `json:"content,omitempty"`
	ContentSHA256   string            `json:"content_sha256,omitempty"`
	ContentBytes    int               `json:"content_bytes"`
	EstimatedTokens int               `json:"estimated_tokens"`
	Privacy         string            `json:"privacy"`
}

type InjectionReceipt struct {
	SchemaVersion      string            `json:"schema_version"`
	InjectionID        string            `json:"injection_id"`
	RetrievalReceiptID string            `json:"retrieval_receipt_id"`
	RecordedAt         time.Time         `json:"recorded_at"`
	Channel            DeliveryChannel   `json:"channel"`
	Content            string            `json:"content"`
	ContentSHA256      string            `json:"content_sha256"`
	ContentBytes       int               `json:"content_bytes"`
	EstimatedTokens    int               `json:"estimated_tokens"`
	Memories           []MemoryReference `json:"memories"`
	Privacy            string            `json:"privacy"`
}

type Reporter struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Adoption string

const (
	AdoptionAdopted    Adoption = "adopted"
	AdoptionNotAdopted Adoption = "not_adopted"
	AdoptionUnknown    Adoption = "unknown"
)

type Outcome string

const (
	OutcomeHelpful Outcome = "helpful"
	OutcomeNeutral Outcome = "neutral"
	OutcomeHarmful Outcome = "harmful"
	OutcomeUnknown Outcome = "unknown"
)

type AdoptionItem struct {
	MemoryReference
	Adoption Adoption `json:"adoption"`
	Outcome  Outcome  `json:"outcome"`
	Reason   string   `json:"reason"`
}

type AdoptionRequest struct {
	SchemaVersion           string         `json:"schema_version"`
	Reporter                Reporter       `json:"reporter"`
	RetrievalReceiptID      string         `json:"retrieval_receipt_id"`
	InjectionID             string         `json:"injection_id,omitempty"`
	Items                   []AdoptionItem `json:"items"`
	OutcomeEvidenceEventIDs []string       `json:"outcome_evidence_event_ids,omitempty"`
}

type AdoptionReceipt struct {
	SchemaVersion           string         `json:"schema_version"`
	AdoptionID              string         `json:"adoption_id"`
	RecordedAt              time.Time      `json:"recorded_at"`
	Reporter                Reporter       `json:"reporter"`
	RetrievalReceiptID      string         `json:"retrieval_receipt_id"`
	InjectionID             string         `json:"injection_id,omitempty"`
	Items                   []AdoptionItem `json:"items"`
	OutcomeEvidenceEventIDs []string       `json:"outcome_evidence_event_ids,omitempty"`
	RequestSHA256           string         `json:"request_sha256"`
	Privacy                 string         `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion     string   `json:"schema_version"`
	RetrievalsChecked int      `json:"retrievals_checked"`
	InjectionsChecked int      `json:"injections_checked"`
	AdoptionsChecked  int      `json:"adoptions_checked"`
	OpenRetrievalIDs  []string `json:"open_retrieval_ids"`
	Issues            []string `json:"issues"`
	Privacy           string   `json:"privacy"`
}
