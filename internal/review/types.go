package review

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
)

const (
	RequestSchemaVersion = "candidate-review-request/v1alpha1"
	EventSchemaVersion   = "candidate-review-event/v1alpha1"
	RecordSchemaVersion  = "candidate-review-record/v1alpha1"
	ApplySchemaVersion   = "candidate-review-apply-result/v1alpha1"
	StatusSchemaVersion  = "candidate-review-status/v1alpha1"
	ProofSchemaVersion   = "candidate-validation-proof/v1alpha1"
)

const (
	ReviewerKindCallerAttestation = "caller_attestation"
	ReviewerKindSyntheticTest     = "synthetic_test"
	ReviewerKindLegacyHuman       = "human"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusValidated   Status = "validated"
	StatusRejected    Status = "rejected"
	StatusQuarantined Status = "quarantined"
)

type Action string

const (
	ActionValidate   Action = "validate"
	ActionReject     Action = "reject"
	ActionQuarantine Action = "quarantine"
	ActionReopen     Action = "reopen"
)

type Basis string

const (
	BasisExplicitRemember         Basis = "explicit_remember"
	BasisUserCorrection           Basis = "user_correction"
	BasisStableRepetition         Basis = "stable_repetition"
	BasisOutcomeEvidence          Basis = "outcome_evidence"
	BasisExplicitUserConfirmation Basis = "explicit_user_confirmation"
)

type ScopeKind string

const (
	ScopeGlobal     ScopeKind = "global"
	ScopeAgent      ScopeKind = "agent"
	ScopeRepository ScopeKind = "repository"
	ScopeProject    ScopeKind = "project"
	ScopeTask       ScopeKind = "task"
)

type Reviewer struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func ValidReviewerKind(kind string) bool {
	switch kind {
	case ReviewerKindCallerAttestation, ReviewerKindSyntheticTest, ReviewerKindLegacyHuman:
		return true
	default:
		return false
	}
}

type Scope struct {
	Kind  ScopeKind `json:"kind"`
	Value string    `json:"value"`
}

type Request struct {
	SchemaVersion   string              `json:"schema_version"`
	Reviewer        Reviewer            `json:"reviewer"`
	ConflictGroupID string              `json:"conflict_group_id,omitempty"`
	Transitions     []TransitionRequest `json:"transitions"`
}

type TransitionRequest struct {
	CandidateID            string   `json:"candidate_id"`
	CandidateContentSHA256 string   `json:"candidate_content_sha256"`
	ExpectedStatus         Status   `json:"expected_status"`
	Action                 Action   `json:"action"`
	Scope                  *Scope   `json:"scope,omitempty"`
	Basis                  []Basis  `json:"basis,omitempty"`
	EvidenceEventIDs       []string `json:"evidence_event_ids,omitempty"`
	Reason                 string   `json:"reason"`
}

type Event struct {
	SchemaVersion             string       `json:"schema_version"`
	EventID                   string       `json:"event_id"`
	RecordedAt                time.Time    `json:"recorded_at"`
	DeviceID                  string       `json:"device_id"`
	Reviewer                  Reviewer     `json:"reviewer"`
	RequestSHA256             string       `json:"request_sha256"`
	SourceCandidateGeneration string       `json:"source_candidate_generation"`
	SourceCandidatesSHA256    string       `json:"source_candidates_sha256"`
	ConflictGroupID           string       `json:"conflict_group_id,omitempty"`
	Transitions               []Transition `json:"transitions"`
	Privacy                   string       `json:"privacy"`
}

type Transition struct {
	CandidateID            string   `json:"candidate_id"`
	CandidateContentSHA256 string   `json:"candidate_content_sha256"`
	ExpectedStatus         Status   `json:"expected_status"`
	Action                 Action   `json:"action"`
	ResultingStatus        Status   `json:"resulting_status"`
	Scope                  *Scope   `json:"scope,omitempty"`
	Basis                  []Basis  `json:"basis,omitempty"`
	EvidenceEventIDs       []string `json:"evidence_event_ids,omitempty"`
	Reason                 string   `json:"reason"`
}

type Record struct {
	SchemaVersion        string `json:"schema_version"`
	Sequence             int64  `json:"sequence"`
	Event                Event  `json:"event"`
	PreviousRecordSHA256 string `json:"previous_record_sha256"`
	RecordSHA256         string `json:"record_sha256"`
}

type ApplyResult struct {
	SchemaVersion string       `json:"schema_version"`
	EventID       string       `json:"event_id"`
	Sequence      int64        `json:"sequence"`
	RecordSHA256  string       `json:"record_sha256"`
	Transitions   []Transition `json:"transitions"`
	Privacy       string       `json:"privacy"`
}

type StatusResult struct {
	SchemaVersion            string `json:"schema_version"`
	CandidateID              string `json:"candidate_id"`
	CandidateContentSHA256   string `json:"candidate_content_sha256"`
	CandidateDerivationState string `json:"candidate_derivation_state"`
	ReviewStatus             Status `json:"review_status"`
	ReviewEvents             int    `json:"review_events"`
	LastReviewRecordSHA256   string `json:"last_review_record_sha256,omitempty"`
	Privacy                  string `json:"privacy"`
}

type VerificationReport struct {
	RecordsChecked   int      `json:"records_checked"`
	LastRecordSHA256 string   `json:"last_record_sha256,omitempty"`
	Issues           []string `json:"issues"`
}

type ValidationProof struct {
	SchemaVersion             string    `json:"schema_version"`
	SourceCandidateGeneration string    `json:"source_candidate_generation"`
	SourceCandidatesSHA256    string    `json:"source_candidates_sha256"`
	CandidateID               string    `json:"candidate_id"`
	CandidateContentSHA256    string    `json:"candidate_content_sha256"`
	ReviewEventID             string    `json:"review_event_id"`
	ReviewRecordSHA256        string    `json:"review_record_sha256"`
	ReviewSequence            int64     `json:"review_sequence"`
	ReviewedAt                time.Time `json:"reviewed_at"`
	Reviewer                  Reviewer  `json:"reviewer"`
	Scope                     Scope     `json:"scope"`
	Basis                     []Basis   `json:"basis"`
	EvidenceEventIDs          []string  `json:"evidence_event_ids,omitempty"`
	Privacy                   string    `json:"privacy"`
}

type ValidatedCandidate struct {
	Candidate candidates.Candidate
	Proof     ValidationProof
}
