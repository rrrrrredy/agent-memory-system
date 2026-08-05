// Package capturesupervisor coordinates periodic, local-only capture across
// supported agents without modifying an agent's own configuration.
package capturesupervisor

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	ConfigSchemaVersion    = "capture-supervisor-config/v1alpha1"
	InventorySchemaVersion = "capture-supervisor-inventory/v1alpha1"
	EventSchemaVersion     = "capture-supervisor-event/v1alpha1"
	RunSchemaVersion       = "capture-supervisor-run-result/v1alpha1"
	StatusSchemaVersion    = "capture-supervisor-status/v1alpha1"
	LocalPrivacy           = "local_only"
)

type SourceKind string

const (
	SourceCodexRollouts  SourceKind = "codex_rollouts"
	SourceClaudeHome     SourceKind = "claude_home"
	SourceOpenCodeNative SourceKind = "opencode_native"
	SourceOpenCodeEvents SourceKind = "opencode_events"
)

type Source struct {
	ID          string       `json:"id"`
	Agent       ledger.Agent `json:"agent"`
	Kind        SourceKind   `json:"kind"`
	Required    bool         `json:"required"`
	Path        string       `json:"path,omitempty"`
	BinaryPath  string       `json:"binary_path,omitempty"`
	StagingRoot string       `json:"staging_root,omitempty"`
}

type Config struct {
	SchemaVersion          string   `json:"schema_version"`
	IntervalSeconds        int64    `json:"interval_seconds"`
	FullReconcileEveryRuns int      `json:"full_reconcile_every_runs"`
	SourceTimeoutSeconds   int64    `json:"source_timeout_seconds"`
	Sources                []Source `json:"sources"`
	Privacy                string   `json:"privacy"`
}

type InventoryItem struct {
	IdentitySHA256 string     `json:"identity_sha256"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	ContentSHA256  string     `json:"content_sha256,omitempty"`
	Bytes          int64      `json:"bytes,omitempty"`
	ModifiedAt     *time.Time `json:"modified_at,omitempty"`
	LastSeenRunID  string     `json:"last_seen_run_id,omitempty"`
}

type Inventory struct {
	SchemaVersion      string          `json:"schema_version"`
	RunID              string          `json:"run_id"`
	SourceID           string          `json:"source_id"`
	SourceConfigSHA256 string          `json:"source_config_sha256"`
	Agent              ledger.Agent    `json:"agent"`
	Kind               SourceKind      `json:"kind"`
	Phase              string          `json:"phase"`
	ObservedAt         time.Time       `json:"observed_at"`
	Status             string          `json:"status"`
	Items              []InventoryItem `json:"items"`
	Available          int             `json:"available"`
	Missing            int             `json:"missing"`
	Unreadable         int             `json:"unreadable"`
	Unverified         int             `json:"unverified"`
	Coverage           string          `json:"coverage"`
	InventorySHA256    string          `json:"inventory_sha256"`
	Privacy            string          `json:"privacy"`
}

type SourceResult struct {
	SourceID           string       `json:"source_id"`
	Agent              ledger.Agent `json:"agent"`
	Kind               SourceKind   `json:"kind"`
	Required           bool         `json:"required"`
	SourceConfigSHA256 string       `json:"source_config_sha256"`
	InventorySHA256    string       `json:"inventory_sha256,omitempty"`
	Outcome            string       `json:"outcome"`
	FilesExamined      int          `json:"files_examined"`
	FilesChanged       int          `json:"files_changed"`
	EventsAppended     int          `json:"events_appended"`
	GapsAppended       int          `json:"gaps_appended"`
	BytesCaptured      int64        `json:"bytes_captured"`
	ErrorCode          string       `json:"error_code,omitempty"`
	ErrorDetailSHA256  string       `json:"error_detail_sha256,omitempty"`
}

type Event struct {
	SchemaVersion       string        `json:"schema_version"`
	Sequence            uint64        `json:"sequence"`
	EventID             string        `json:"event_id"`
	RunID               string        `json:"run_id,omitempty"`
	ObservedAt          time.Time     `json:"observed_at"`
	Action              string        `json:"action"`
	Outcome             string        `json:"outcome"`
	SourceID            string        `json:"source_id,omitempty"`
	Source              *SourceResult `json:"source,omitempty"`
	InventorySHA256     string        `json:"inventory_sha256,omitempty"`
	ConfigSHA256        string        `json:"config_sha256"`
	FullReconcile       bool          `json:"full_reconcile,omitempty"`
	PreviousEventSHA256 string        `json:"previous_event_sha256,omitempty"`
	EventSHA256         string        `json:"event_sha256"`
	Privacy             string        `json:"privacy"`
}

type RunResult struct {
	SchemaVersion string         `json:"schema_version"`
	RunID         string         `json:"run_id"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    time.Time      `json:"finished_at"`
	Outcome       string         `json:"outcome"`
	FullReconcile bool           `json:"full_reconcile"`
	Sources       []SourceResult `json:"sources"`
	AuditSequence uint64         `json:"audit_sequence"`
	Privacy       string         `json:"privacy"`
}

type SourceStatus struct {
	SourceID      string       `json:"source_id"`
	Agent         ledger.Agent `json:"agent"`
	Kind          SourceKind   `json:"kind"`
	Required      bool         `json:"required"`
	LastRunID     string       `json:"last_run_id,omitempty"`
	ConfigSHA256  string       `json:"config_sha256,omitempty"`
	LastAttemptAt *time.Time   `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time   `json:"last_success_at,omitempty"`
	LastOutcome   string       `json:"last_outcome,omitempty"`
	LastErrorCode string       `json:"last_error_code,omitempty"`
}

type Issue struct {
	Code      string `json:"code"`
	SourceID  string `json:"source_id,omitempty"`
	Message   string `json:"message"`
	RecoverBy string `json:"recover_by,omitempty"`
}

type Status struct {
	SchemaVersion      string         `json:"schema_version"`
	Configured         bool           `json:"configured"`
	ConfigSHA256       string         `json:"config_sha256,omitempty"`
	AuditEventsChecked uint64         `json:"audit_events_checked"`
	RunsCompleted      uint64         `json:"runs_completed"`
	LastRunID          string         `json:"last_run_id,omitempty"`
	LastRunStartedAt   *time.Time     `json:"last_run_started_at,omitempty"`
	LastRunFinishedAt  *time.Time     `json:"last_run_finished_at,omitempty"`
	LastRunOutcome     string         `json:"last_run_outcome,omitempty"`
	RunIncomplete      bool           `json:"run_incomplete"`
	OperationLocked    bool           `json:"operation_locked"`
	Sources            []SourceStatus `json:"sources"`
	IntegrityReady     bool           `json:"integrity_ready"`
	CaptureReady       bool           `json:"capture_ready"`
	Ready              bool           `json:"ready"`
	Issues             []Issue        `json:"issues"`
	Warnings           []Issue        `json:"warnings"`
	Privacy            string         `json:"privacy"`
}

type StatusOptions struct {
	RequireConfigured bool
	RequireHealthy    bool
	RequiredAgents    []ledger.Agent
	MaximumAge        time.Duration
	Now               func() time.Time
}

type RunOptions struct {
	Now func() time.Time
}
