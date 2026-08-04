package ruleapproval

import "time"

const (
	RequestSchemaVersion      = "rule-change-approval-request/v1alpha1"
	EventSchemaVersion        = "rule-change-approval-event/v1alpha1"
	RecordSchemaVersion       = "rule-change-approval-record/v1alpha1"
	ApplyResultSchemaVersion  = "rule-change-approval-result/v1alpha1"
	StatusSchemaVersion       = "rule-change-approval-status/v1alpha1"
	VerificationSchemaVersion = "rule-change-approval-verification/v1alpha1"
)

type Surface string

const (
	SurfaceAgentsMD   Surface = "agents_md"
	SurfaceSkill      Surface = "skill"
	SurfaceHook       Surface = "hook"
	SurfacePlugin     Surface = "plugin"
	SurfaceGlobalRule Surface = "global_rule"
)

type Status string

const (
	StatusNotAuthorized Status = "not_authorized"
	StatusAuthorized    Status = "authorized"
)

type Action string

const (
	ActionAuthorize Action = "authorize"
	ActionRevoke    Action = "revoke"
)

type Approver struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Request struct {
	SchemaVersion  string   `json:"schema_version"`
	Approver       Approver `json:"approver"`
	MemoryID       string   `json:"memory_id"`
	RevisionID     string   `json:"revision_id"`
	Surface        Surface  `json:"surface"`
	Target         string   `json:"target"`
	ExpectedStatus Status   `json:"expected_status"`
	Action         Action   `json:"action"`
	Reason         string   `json:"reason"`
}

type Event struct {
	SchemaVersion   string    `json:"schema_version"`
	EventID         string    `json:"event_id"`
	RecordedAt      time.Time `json:"recorded_at"`
	DeviceID        string    `json:"device_id"`
	Approver        Approver  `json:"approver"`
	RequestSHA256   string    `json:"request_sha256"`
	MemoryID        string    `json:"memory_id"`
	RevisionID      string    `json:"revision_id"`
	Surface         Surface   `json:"surface"`
	Target          string    `json:"target"`
	ExpectedStatus  Status    `json:"expected_status"`
	Action          Action    `json:"action"`
	ResultingStatus Status    `json:"resulting_status"`
	Reason          string    `json:"reason"`
	Privacy         string    `json:"privacy"`
}

type Record struct {
	SchemaVersion        string `json:"schema_version"`
	Sequence             int64  `json:"sequence"`
	Event                Event  `json:"event"`
	PreviousRecordSHA256 string `json:"previous_record_sha256"`
	RecordSHA256         string `json:"record_sha256"`
}

type ApplyResult struct {
	SchemaVersion string  `json:"schema_version"`
	EventID       string  `json:"event_id"`
	Sequence      int64   `json:"sequence"`
	RecordSHA256  string  `json:"record_sha256"`
	MemoryID      string  `json:"memory_id"`
	RevisionID    string  `json:"revision_id"`
	Surface       Surface `json:"surface"`
	Target        string  `json:"target"`
	Status        Status  `json:"status"`
	Privacy       string  `json:"privacy"`
}

type StatusResult struct {
	SchemaVersion  string  `json:"schema_version"`
	MemoryID       string  `json:"memory_id"`
	RevisionID     string  `json:"revision_id"`
	Surface        Surface `json:"surface"`
	Target         string  `json:"target"`
	ApprovalStatus Status  `json:"approval_status"`
	Effective      bool    `json:"effective"`
	Events         int     `json:"events"`
	Privacy        string  `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion    string   `json:"schema_version"`
	RecordsChecked   int      `json:"records_checked"`
	ApprovalsChecked int      `json:"approvals_checked"`
	LastRecordSHA256 string   `json:"last_record_sha256,omitempty"`
	Issues           []string `json:"issues"`
}
