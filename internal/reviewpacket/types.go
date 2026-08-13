package reviewpacket

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const (
	PacketSchemaVersion = "candidate-review-packet/v1alpha1"
	BuildSchemaVersion  = "candidate-review-packet-build-result/v1alpha1"
	PrivacyLocalOnly    = "local_only"
	MaxItems            = 1000
)

type Item struct {
	Candidate              candidates.Candidate `json:"candidate"`
	TextSHA256             string               `json:"text_sha256"`
	ReviewStatus           review.Status        `json:"review_status"`
	LastReviewRecordSHA256 string               `json:"last_review_record_sha256,omitempty"`
}

type Packet struct {
	SchemaVersion          string    `json:"schema_version"`
	PacketID               string    `json:"packet_id"`
	CreatedAt              time.Time `json:"created_at"`
	Generation             string    `json:"generation"`
	CandidatesSHA256       string    `json:"candidates_sha256"`
	SourceEvidenceRecords  int       `json:"source_evidence_records"`
	SourceEvidenceSHA256   string    `json:"source_evidence_sha256"`
	CurrentEvidenceRecords int       `json:"current_evidence_records"`
	CurrentEvidenceSHA256  string    `json:"current_evidence_sha256"`
	Items                  []Item    `json:"items"`
	Privacy                string    `json:"privacy"`
}

type BuildOptions struct {
	Generation string
	Statuses   []candidates.ReviewStatus
	Limit      int
	Now        func() time.Time
}

type BuildResult struct {
	SchemaVersion string `json:"schema_version"`
	PacketID      string `json:"packet_id"`
	Generation    string `json:"generation"`
	Items         int    `json:"items"`
	RelativePath  string `json:"relative_path"`
	Written       bool   `json:"written"`
	Privacy       string `json:"privacy"`
}
