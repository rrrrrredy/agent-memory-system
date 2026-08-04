package autosync

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
)

const (
	ConfigSchemaVersion = "automatic-git-sync-config/v1alpha1"
	EventSchemaVersion  = "automatic-git-sync-event/v1alpha1"
	StatusSchemaVersion = "automatic-git-sync-status/v1alpha1"
	ResultSchemaVersion = "automatic-git-sync-result/v1alpha1"
	LocalPrivacy        = "local_only"

	StateDisabled  = "disabled"
	StateReady     = "ready"
	StateRetrying  = "retrying"
	StateSuspended = "suspended"
)

const (
	DefaultInterval                 = 15 * time.Minute
	DefaultMaximumRetry             = 6 * time.Hour
	DefaultMaximumConsecutiveErrors = 5
	minimumInterval                 = time.Minute
	maximumInterval                 = 1439 * time.Minute
)

type Config struct {
	SchemaVersion            string    `json:"schema_version"`
	Enabled                  bool      `json:"enabled"`
	EvidenceRoot             string    `json:"evidence_root,omitempty"`
	RepositoryRoot           string    `json:"repository_root"`
	ExecutablePath           string    `json:"executable_path,omitempty"`
	Remote                   string    `json:"remote"`
	IntervalSeconds          int64     `json:"interval_seconds"`
	MaximumRetrySeconds      int64     `json:"maximum_retry_seconds"`
	MaximumConsecutiveErrors int       `json:"maximum_consecutive_errors"`
	Scheduler                string    `json:"scheduler"`
	TaskID                   string    `json:"task_id"`
	UpdatedAt                time.Time `json:"updated_at"`
	Privacy                  string    `json:"privacy"`
}

type Event struct {
	SchemaVersion       string          `json:"schema_version"`
	Sequence            uint64          `json:"sequence"`
	ObservedAt          time.Time       `json:"observed_at"`
	Action              string          `json:"action"`
	Outcome             string          `json:"outcome"`
	ResultingState      string          `json:"resulting_state"`
	ConsecutiveErrors   int             `json:"consecutive_errors"`
	NextAttemptAt       *time.Time      `json:"next_attempt_at,omitempty"`
	LastAttemptAt       *time.Time      `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time      `json:"last_success_at,omitempty"`
	LastSyncOutcome     string          `json:"last_sync_outcome,omitempty"`
	ErrorCode           string          `json:"error_code,omitempty"`
	ErrorDetail         string          `json:"error_detail,omitempty"`
	Export              *ExportSummary  `json:"export,omitempty"`
	Sync                *gitsync.Result `json:"sync,omitempty"`
	ConfigSHA256        string          `json:"config_sha256,omitempty"`
	PreviousEventSHA256 string          `json:"previous_event_sha256,omitempty"`
	EventSHA256         string          `json:"event_sha256"`
	Privacy             string          `json:"privacy"`
}

type State struct {
	State             string
	ConsecutiveErrors int
	NextAttemptAt     *time.Time
	LastAttemptAt     *time.Time
	LastSuccessAt     *time.Time
	LastSyncOutcome   string
	ErrorCode         string
	LastSequence      uint64
	LastEventSHA256   string
	ConfigSHA256      string
}

type Issue struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RecoverBy string `json:"recover_by,omitempty"`
}

type Status struct {
	SchemaVersion       string     `json:"schema_version"`
	Enabled             bool       `json:"enabled"`
	State               string     `json:"state"`
	Scheduler           string     `json:"scheduler"`
	SchedulerRegistered bool       `json:"scheduler_registered"`
	IntervalSeconds     int64      `json:"interval_seconds,omitempty"`
	ConsecutiveErrors   int        `json:"consecutive_errors"`
	NextAttemptAt       *time.Time `json:"next_attempt_at,omitempty"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastSyncOutcome     string     `json:"last_sync_outcome,omitempty"`
	LastErrorCode       string     `json:"last_error_code,omitempty"`
	EventsChecked       uint64     `json:"events_checked"`
	Issues              []Issue    `json:"issues"`
	Privacy             string     `json:"privacy"`
}

type ExportSummary struct {
	MemoriesSelected   int `json:"memories_selected"`
	RevisionsProjected int `json:"revisions_projected"`
	RevisionsWritten   int `json:"revisions_written"`
}

type RunResult struct {
	SchemaVersion string          `json:"schema_version"`
	Outcome       string          `json:"outcome"`
	State         string          `json:"state"`
	Export        *ExportSummary  `json:"export,omitempty"`
	Sync          *gitsync.Result `json:"sync,omitempty"`
	NextAttemptAt *time.Time      `json:"next_attempt_at,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	Privacy       string          `json:"privacy"`
}

type EnableOptions struct {
	RepositoryRoot           string
	EvidenceRoot             string
	ExecutablePath           string
	Remote                   string
	Interval                 time.Duration
	MaximumRetry             time.Duration
	MaximumConsecutiveErrors int
}
