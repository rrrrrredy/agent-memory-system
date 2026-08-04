package promotion

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const (
	RequestSchemaVersion      = "memory-promotion-request/v1alpha1"
	RevisionSchemaVersion     = "promoted-memory-revision/v1alpha1"
	EventSchemaVersion        = "memory-promotion-event/v1alpha1"
	RecordSchemaVersion       = "memory-promotion-record/v1alpha1"
	ApplyResultSchemaVersion  = "memory-promotion-apply-result/v1alpha1"
	StatusSchemaVersion       = "promoted-memory-status/v1alpha1"
	ScanResultSchemaVersion   = "candidate-promotion-scan/v1alpha1"
	VerificationSchemaVersion = "memory-promotion-verification/v1alpha1"
)

type Action string

const (
	ActionPromote   Action = "promote"
	ActionSupersede Action = "supersede"
	ActionRevoke    Action = "revoke"
)

type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

type Approver struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type CandidateReference struct {
	Generation              string `json:"generation"`
	CandidateID             string `json:"candidate_id"`
	CandidateContentSHA256  string `json:"candidate_content_sha256"`
	ExpectedReviewRecordSHA string `json:"expected_review_record_sha256"`
}

type Request struct {
	SchemaVersion      string                 `json:"schema_version"`
	Approver           Approver               `json:"approver"`
	Action             Action                 `json:"action"`
	MemoryID           string                 `json:"memory_id,omitempty"`
	ExpectedRevisionID string                 `json:"expected_revision_id,omitempty"`
	Candidate          *CandidateReference    `json:"candidate,omitempty"`
	Redactions         []secretscan.Redaction `json:"redactions,omitempty"`
	ExpectedTextSHA256 string                 `json:"expected_text_sha256,omitempty"`
	Reason             string                 `json:"reason"`
}

type Source struct {
	CandidateGeneration    string         `json:"candidate_generation"`
	CandidatesSHA256       string         `json:"candidates_sha256"`
	CandidateID            string         `json:"candidate_id"`
	CandidateContentSHA256 string         `json:"candidate_content_sha256"`
	SemanticKeySHA256      string         `json:"semantic_key_sha256"`
	ReviewEventID          string         `json:"review_event_id"`
	ReviewRecordSHA256     string         `json:"review_record_sha256"`
	ReviewScope            review.Scope   `json:"review_scope"`
	ReviewBasis            []review.Basis `json:"review_basis"`
}

type ScanAttestation struct {
	ScannerVersion     string                 `json:"scanner_version"`
	SourceTextSHA256   string                 `json:"source_text_sha256"`
	FindingIDs         []string               `json:"finding_ids"`
	Redactions         []secretscan.Redaction `json:"redactions"`
	RedactedTextSHA256 string                 `json:"redacted_text_sha256"`
}

type Revision struct {
	SchemaVersion                      string                   `json:"schema_version"`
	MemoryID                           string                   `json:"memory_id"`
	RevisionID                         string                   `json:"revision_id"`
	ParentRevisionID                   string                   `json:"parent_revision_id,omitempty"`
	Action                             Action                   `json:"action"`
	Status                             Status                   `json:"status"`
	Kind                               candidates.CandidateKind `json:"kind"`
	Text                               string                   `json:"text,omitempty"`
	TextSHA256                         string                   `json:"text_sha256,omitempty"`
	Scope                              review.Scope             `json:"scope"`
	Source                             *Source                  `json:"source,omitempty"`
	Scan                               *ScanAttestation         `json:"scan,omitempty"`
	RequiresExplicitRuleChangeApproval bool                     `json:"requires_explicit_rule_change_approval"`
	RuleChangeAuthorization            string                   `json:"rule_change_authorization"`
	RecordedAt                         time.Time                `json:"recorded_at"`
	OriginDeviceID                     string                   `json:"origin_device_id"`
	ApproverID                         string                   `json:"approver_id"`
	Reason                             string                   `json:"reason"`
	Privacy                            string                   `json:"privacy"`
}

type Event struct {
	SchemaVersion string    `json:"schema_version"`
	EventID       string    `json:"event_id"`
	RecordedAt    time.Time `json:"recorded_at"`
	DeviceID      string    `json:"device_id"`
	Approver      Approver  `json:"approver"`
	RequestSHA256 string    `json:"request_sha256"`
	Revision      Revision  `json:"revision"`
	Privacy       string    `json:"privacy"`
}

type Record struct {
	SchemaVersion        string `json:"schema_version"`
	Sequence             int64  `json:"sequence"`
	Event                Event  `json:"event"`
	PreviousRecordSHA256 string `json:"previous_record_sha256"`
	RecordSHA256         string `json:"record_sha256"`
}

type ApplyResult struct {
	SchemaVersion string   `json:"schema_version"`
	EventID       string   `json:"event_id"`
	Sequence      int64    `json:"sequence"`
	RecordSHA256  string   `json:"record_sha256"`
	Revision      Revision `json:"revision"`
	Privacy       string   `json:"privacy"`
}

type StatusResult struct {
	SchemaVersion       string   `json:"schema_version"`
	MemoryID            string   `json:"memory_id"`
	CurrentRevisionID   string   `json:"current_revision_id"`
	Status              Status   `json:"status"`
	RevisionCount       int      `json:"revision_count"`
	SourceReviewCurrent bool     `json:"source_review_current"`
	ExportEligible      bool     `json:"export_eligible"`
	Revision            Revision `json:"revision"`
	Privacy             string   `json:"privacy"`
}

type ScanResult struct {
	SchemaVersion                      string            `json:"schema_version"`
	CandidateGeneration                string            `json:"candidate_generation"`
	CandidateID                        string            `json:"candidate_id"`
	CandidateContentSHA256             string            `json:"candidate_content_sha256"`
	RequiresExplicitRuleChangeApproval bool              `json:"requires_explicit_rule_change_approval"`
	Report                             secretscan.Report `json:"report"`
	Privacy                            string            `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion    string   `json:"schema_version"`
	RecordsChecked   int      `json:"records_checked"`
	MemoriesChecked  int      `json:"memories_checked"`
	LastRecordSHA256 string   `json:"last_record_sha256,omitempty"`
	Issues           []string `json:"issues"`
}
