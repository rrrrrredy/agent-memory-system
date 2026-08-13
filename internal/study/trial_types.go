package study

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
)

const (
	TrialReservationSchema    = "longitudinal-study-trial-reservation/v1alpha1"
	TrialTerminalSchema       = "longitudinal-study-trial-terminal/v1alpha1"
	RunTaskResultSchema       = "longitudinal-study-run-result/v1alpha1"
	TrialReservationMediaType = "application/vnd.agentmem.longitudinal-study-trial-reservation+json"
	TrialTerminalMediaType    = "application/vnd.agentmem.longitudinal-study-trial-terminal+json"
)

type TrialReservation struct {
	SchemaVersion     string                 `json:"schema_version"`
	TrialID           string                 `json:"trial_id"`
	StudyID           string                 `json:"study_id"`
	TaskID            string                 `json:"task_id"`
	Condition         Condition              `json:"condition"`
	Plan              BoundEvent             `json:"plan"`
	Request           agentbridge.RunRequest `json:"request"`
	WorkspaceSnapshot WorkspaceSnapshot      `json:"workspace_snapshot"`
	ReservedAt        time.Time              `json:"reserved_at"`
	Privacy           string                 `json:"privacy"`
}

type TrialTerminal struct {
	SchemaVersion string      `json:"schema_version"`
	TerminalID    string      `json:"terminal_id"`
	StudyID       string      `json:"study_id"`
	TaskID        string      `json:"task_id"`
	Trial         BoundEvent  `json:"trial"`
	Execution     *BoundEvent `json:"execution,omitempty"`
	Outcome       string      `json:"outcome"`
	FailureKind   string      `json:"failure_kind,omitempty"`
	FinishedAt    time.Time   `json:"finished_at"`
	Privacy       string      `json:"privacy"`
}

type RunTaskResult struct {
	SchemaVersion string                 `json:"schema_version"`
	Trial         TrialReservation       `json:"trial"`
	TrialEvent    BoundEvent             `json:"trial_event"`
	Execution     *agentbridge.RunResult `json:"execution,omitempty"`
	Terminal      TrialTerminal          `json:"terminal"`
	TerminalEvent BoundEvent             `json:"terminal_event"`
	Privacy       string                 `json:"privacy"`
}

type RunTaskOptions struct {
	PortableRoot string
	CodexPath    string
	Now          func() time.Time
}
