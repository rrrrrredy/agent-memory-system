package ledger

import "time"

const SchemaVersion = "evidence-event/v1alpha1"

type Agent string

const (
	AgentCodex      Agent = "codex"
	AgentClaudeCode Agent = "claude_code"
	AgentOpenCode   Agent = "opencode"
	AgentUnknown    Agent = "unknown"
)

type EventKind string

const (
	KindUserMessage    EventKind = "user_message"
	KindAgentMessage   EventKind = "agent_message"
	KindReasoning      EventKind = "reasoning"
	KindToolCall       EventKind = "tool_call"
	KindToolResult     EventKind = "tool_result"
	KindApproval       EventKind = "approval"
	KindFileChange     EventKind = "file_change"
	KindAttachment     EventKind = "attachment"
	KindSubagentEvent  EventKind = "subagent_event"
	KindCompaction     EventKind = "compaction"
	KindSystemEvent    EventKind = "system_event"
	KindSourceSnapshot EventKind = "source_snapshot"
	KindGap            EventKind = "gap"
	KindUnknown        EventKind = "unknown"
)

type ReasoningVisibility string

const (
	ReasoningRawExposed      ReasoningVisibility = "raw_exposed"
	ReasoningSummaryOnly     ReasoningVisibility = "summary_only"
	ReasoningEncryptedOpaque ReasoningVisibility = "encrypted_opaque"
	ReasoningNotExposed      ReasoningVisibility = "not_exposed"
	ReasoningNotApplicable   ReasoningVisibility = "not_applicable"
)

type CompletenessStatus string

const (
	CompletenessComplete CompletenessStatus = "complete"
	CompletenessPartial  CompletenessStatus = "partial"
	CompletenessMissing  CompletenessStatus = "missing"
	CompletenessUnknown  CompletenessStatus = "unknown"
)

type Source struct {
	Agent          Agent  `json:"agent"`
	Adapter        string `json:"adapter"`
	AdapterVersion string `json:"adapter_version"`
	DeviceID       string `json:"device_id"`
	OS             string `json:"os,omitempty"`
	ThreadID       string `json:"thread_id"`
	SessionID      string `json:"session_id,omitempty"`
	SourceEventID  string `json:"source_event_id,omitempty"`
	SourcePathHash string `json:"source_path_hash,omitempty"`
	SourceCursor   string `json:"source_cursor,omitempty"`
	ByteStart      *int64 `json:"byte_start,omitempty"`
	ByteEnd        *int64 `json:"byte_end,omitempty"`
}

type BlobRef struct {
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	RelativePath string `json:"relative_path"`
}

type Payload struct {
	Encoding  string   `json:"encoding"`
	MediaType string   `json:"media_type,omitempty"`
	Content   *string  `json:"content,omitempty"`
	Blob      *BlobRef `json:"blob,omitempty"`
	SHA256    string   `json:"sha256"`
	Bytes     int64    `json:"bytes"`
}

type ReasoningCapture struct {
	Visibility ReasoningVisibility `json:"visibility"`
	Provider   string              `json:"provider,omitempty"`
	Model      string              `json:"model,omitempty"`
	Note       string              `json:"note,omitempty"`
}

type Completeness struct {
	Status        CompletenessStatus `json:"status"`
	Reason        string             `json:"reason,omitempty"`
	ExpectedBytes *int64             `json:"expected_bytes,omitempty"`
	CapturedBytes *int64             `json:"captured_bytes,omitempty"`
}

type Causality struct {
	ParentEventIDs []string `json:"parent_event_ids,omitempty"`
	CallID         string   `json:"call_id,omitempty"`
	CompactionID   string   `json:"compaction_id,omitempty"`
}

type Privacy struct {
	Classification string `json:"classification"`
}

type Event struct {
	SchemaVersion string            `json:"schema_version"`
	EventID       string            `json:"event_id"`
	Kind          EventKind         `json:"kind"`
	ObservedAt    time.Time         `json:"observed_at"`
	RecordedAt    time.Time         `json:"recorded_at"`
	Source        Source            `json:"source"`
	Payload       *Payload          `json:"payload,omitempty"`
	Reasoning     *ReasoningCapture `json:"reasoning,omitempty"`
	Completeness  Completeness      `json:"completeness"`
	Causality     *Causality        `json:"causality,omitempty"`
	Privacy       Privacy           `json:"privacy"`
}

type Record struct {
	Event              Event  `json:"event"`
	PreviousRecordHash string `json:"previous_record_hash"`
	RecordHash         string `json:"record_hash"`
}

type VerificationReport struct {
	RecordsChecked int      `json:"records_checked"`
	BlobsChecked   int      `json:"blobs_checked"`
	LastRecordHash string   `json:"last_record_hash,omitempty"`
	Issues         []string `json:"issues"`
}
