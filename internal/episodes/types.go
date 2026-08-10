package episodes

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	EpisodeSchemaVersion    = "episode/v1alpha1"
	TimelineSchemaVersion   = "timeline-entry/v1alpha1"
	ManifestSchemaVersion   = "episode-derivation-manifest/v1alpha1"
	BuildSchemaVersion      = "episode-build-result/v1alpha1"
	DerivationVersion       = "episodes/v1alpha1"
	GenerationAuditSchema   = "episode-generation-audit/v1alpha1"
	GenerationAttemptSchema = "episode-generation-attempt/v1alpha1"
)

// GenerationAttempt is committed before detector computation or any derived
// output is created. It remains evidence even when the later build fails.
type GenerationAttempt struct {
	SchemaVersion        string    `json:"schema_version"`
	AttemptID            string    `json:"attempt_id"`
	DerivationVersion    string    `json:"derivation_version"`
	SourceRecords        int       `json:"source_records"`
	SourceLastRecordHash string    `json:"source_last_record_hash"`
	StartedAt            time.Time `json:"started_at"`
	Privacy              string    `json:"privacy"`
}

type VerifiedGenerationAttempt struct {
	Attempt     GenerationAttempt
	Record      ledger.Record
	LedgerIndex int
}

type GenerationAudit struct {
	SchemaVersion        string `json:"schema_version"`
	AuditID              string `json:"audit_id"`
	DerivationVersion    string `json:"derivation_version"`
	SourceRecords        int    `json:"source_records"`
	SourceLastRecordHash string `json:"source_last_record_hash"`
	GenerationName       string `json:"generation_name"`
	ManifestSHA256       string `json:"manifest_sha256"`
	EpisodesSHA256       string `json:"episodes_sha256"`
	TimelineSHA256       string `json:"timeline_sha256"`
	Episodes             int    `json:"episodes"`
	TimelineEntries      int64  `json:"timeline_entries"`
	Compactions          int    `json:"compactions"`
	Privacy              string `json:"privacy"`
}

type VerifiedGenerationAudit struct {
	Audit       GenerationAudit
	Record      ledger.Record
	LedgerIndex int
	Generation  BuildResult
}

type ContinuityStatus string

const (
	ContinuityPreserved            ContinuityStatus = "preserved"
	ContinuityDriftEvidence        ContinuityStatus = "drift_evidence"
	ContinuityAtRisk               ContinuityStatus = "at_risk"
	ContinuityInsufficientEvidence ContinuityStatus = "insufficient_evidence"
	ContinuityNoActiveStatements   ContinuityStatus = "no_active_statements"
)

type StatementStatus string

const (
	StatementPreserved                 StatementStatus = "preserved"
	StatementCorrectionAfterCompaction StatementStatus = "correction_after_compaction"
	StatementMissingFromRepresentation StatementStatus = "missing_from_representation"
)

type BuildOptions struct {
	ShardCount int
}

type BuildResult struct {
	SchemaVersion        string `json:"schema_version"`
	DerivationVersion    string `json:"derivation_version"`
	SourceRecords        int    `json:"source_records"`
	SourceLastRecordHash string `json:"source_last_record_hash,omitempty"`
	GenerationPath       string `json:"generation_path"`
	Episodes             int    `json:"episodes"`
	TimelineEntries      int64  `json:"timeline_entries"`
	Compactions          int    `json:"compactions"`
	DriftEvidence        int    `json:"drift_evidence"`
	AtRisk               int    `json:"at_risk"`
	InsufficientEvidence int    `json:"insufficient_evidence"`
	TimelineSHA256       string `json:"timeline_sha256"`
	EpisodesSHA256       string `json:"episodes_sha256"`
	Reused               bool   `json:"reused"`
}

type Manifest struct {
	SchemaVersion        string `json:"schema_version"`
	DerivationVersion    string `json:"derivation_version"`
	Privacy              string `json:"privacy"`
	SourceRecords        int    `json:"source_records"`
	SourceLastRecordHash string `json:"source_last_record_hash,omitempty"`
	Episodes             int    `json:"episodes"`
	TimelineEntries      int64  `json:"timeline_entries"`
	Compactions          int    `json:"compactions"`
	DriftEvidence        int    `json:"drift_evidence"`
	AtRisk               int    `json:"at_risk"`
	InsufficientEvidence int    `json:"insufficient_evidence"`
	TimelineFile         string `json:"timeline_file"`
	TimelineSHA256       string `json:"timeline_sha256"`
	EpisodesFile         string `json:"episodes_file"`
	EpisodesSHA256       string `json:"episodes_sha256"`
}

type TimelineEntry struct {
	SchemaVersion       string                     `json:"schema_version"`
	Sequence            int64                      `json:"sequence"`
	EpisodeID           string                     `json:"episode_id"`
	EventID             string                     `json:"event_id"`
	Kind                ledger.EventKind           `json:"kind"`
	ObservedAt          time.Time                  `json:"observed_at"`
	RecordedAt          time.Time                  `json:"recorded_at"`
	Source              ledger.Source              `json:"source"`
	Completeness        ledger.Completeness        `json:"completeness"`
	Causality           *ledger.Causality          `json:"causality,omitempty"`
	ReasoningVisibility ledger.ReasoningVisibility `json:"reasoning_visibility,omitempty"`
}

type Episode struct {
	SchemaVersion string                 `json:"schema_version"`
	EpisodeID     string                 `json:"episode_id"`
	Agent         ledger.Agent           `json:"agent"`
	ThreadID      string                 `json:"thread_id"`
	StartedAt     time.Time              `json:"started_at"`
	EndedAt       time.Time              `json:"ended_at"`
	FirstEventID  string                 `json:"first_event_id"`
	LastEventID   string                 `json:"last_event_id"`
	EventCounts   map[string]int         `json:"event_counts"`
	Completeness  EpisodeCompleteness    `json:"completeness"`
	Statements    []Statement            `json:"statements"`
	Compactions   []CompactionCheckpoint `json:"compactions"`
	Issues        []DerivationIssue      `json:"issues,omitempty"`
	Privacy       string                 `json:"privacy"`
}

type EpisodeCompleteness struct {
	Status      ledger.CompletenessStatus `json:"status"`
	Reasons     []string                  `json:"reasons,omitempty"`
	GapEventIDs []string                  `json:"gap_event_ids,omitempty"`
}

type Statement struct {
	StatementID      string    `json:"statement_id"`
	Kind             string    `json:"kind"`
	Text             string    `json:"text"`
	EvidenceEventIDs []string  `json:"evidence_event_ids"`
	FirstSeenAt      time.Time `json:"first_seen_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
}

type CompactionCheckpoint struct {
	CheckpointID            string            `json:"checkpoint_id"`
	EventIDs                []string          `json:"event_ids"`
	ObservedAt              time.Time         `json:"observed_at"`
	RepresentationEventIDs  []string          `json:"representation_event_ids,omitempty"`
	RepresentationAvailable bool              `json:"representation_available"`
	Status                  ContinuityStatus  `json:"status"`
	Checks                  []ContinuityCheck `json:"checks,omitempty"`
	Issues                  []DerivationIssue `json:"issues,omitempty"`
}

type ContinuityCheck struct {
	StatementID            string          `json:"statement_id"`
	Coverage               float64         `json:"coverage"`
	Status                 StatementStatus `json:"status"`
	RepresentationEventIDs []string        `json:"representation_event_ids,omitempty"`
	CorrectionEventIDs     []string        `json:"correction_event_ids,omitempty"`
}

type DerivationIssue struct {
	Code     string   `json:"code"`
	EventIDs []string `json:"event_ids,omitempty"`
}
