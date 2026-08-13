package study

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

const (
	DraftSchema              = "longitudinal-study-draft/v1alpha1"
	PlanSchema               = "longitudinal-study-plan/v1alpha1"
	CreateResultSchema       = "longitudinal-study-create-result/v1alpha1"
	ObserveRequestSchema     = "longitudinal-study-observation-request/v1alpha1"
	ObservationSchema        = "longitudinal-study-observation/v1alpha1"
	ObserveResultSchema      = "longitudinal-study-observe-result/v1alpha1"
	ReportSchema             = "longitudinal-study-report/v1alpha1"
	VerificationSchema       = "longitudinal-study-verification/v1alpha1"
	AcceptanceSchema         = "longitudinal-study-acceptance/v1alpha1"
	OutcomeEvidenceSchema    = "longitudinal-study-outcome-evidence/v1alpha1"
	PlanMediaType            = "application/vnd.agentmem.longitudinal-study-plan+json"
	OutcomeEvidenceMediaType = "application/vnd.agentmem.longitudinal-study-outcome+json"
	ObservationMediaType     = "application/vnd.agentmem.longitudinal-study-observation+json"
	AdapterVersion           = "longitudinal-study/v1alpha1"
	PrivacyLocalOnly         = "local_only"
	AssignmentPolicy         = "content_hash_counterbalanced/v1alpha1"
	ClaimBoundary            = "prospective local built-in acceptance evidence; descriptive association only; not independent causal or provider certification"
)

type Condition string

const (
	ConditionBaseline Condition = "baseline"
	ConditionMemory   Condition = "memory"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

type OutcomeAuthority string

const (
	AuthorityBuiltinAcceptance OutcomeAuthority = "builtin_acceptance_replay"
)

type AcceptanceAssertion struct {
	Kind           string `json:"kind"`
	Path           string `json:"path,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

type AcceptanceContract struct {
	SchemaVersion string                `json:"schema_version"`
	Mode          string                `json:"mode"`
	Assertions    []AcceptanceAssertion `json:"assertions"`
}

type TaskDraft struct {
	TaskID                 string             `json:"task_id"`
	ClusterID              string             `json:"cluster_id"`
	Prompt                 string             `json:"prompt"`
	Model                  string             `json:"model"`
	Sandbox                string             `json:"sandbox"`
	WorkingDirectory       string             `json:"working_directory"`
	TimeoutSeconds         int                `json:"timeout_seconds"`
	SkipGitRepositoryCheck bool               `json:"skip_git_repository_check"`
	Acceptance             AcceptanceContract `json:"acceptance"`
}

type Draft struct {
	SchemaVersion      string       `json:"schema_version"`
	Name               string       `json:"name"`
	Hypothesis         string       `json:"hypothesis"`
	Agent              ledger.Agent `json:"agent"`
	LoadoutID          string       `json:"loadout_id"`
	MinimumElapsedDays int          `json:"minimum_elapsed_days"`
	Tasks              []TaskDraft  `json:"tasks"`
	Privacy            string       `json:"privacy"`
}

type PlannedTask struct {
	TaskID                 string             `json:"task_id"`
	ClusterID              string             `json:"cluster_id"`
	Prompt                 string             `json:"prompt"`
	Model                  string             `json:"model"`
	Sandbox                string             `json:"sandbox"`
	WorkingDirectory       string             `json:"working_directory"`
	TimeoutSeconds         int                `json:"timeout_seconds"`
	SkipGitRepositoryCheck bool               `json:"skip_git_repository_check"`
	Acceptance             AcceptanceContract `json:"acceptance"`
	Order                  int                `json:"order"`
	Condition              Condition          `json:"condition"`
}

type Plan struct {
	SchemaVersion        string           `json:"schema_version"`
	StudyID              string           `json:"study_id"`
	CreatedAt            time.Time        `json:"created_at"`
	Name                 string           `json:"name"`
	Hypothesis           string           `json:"hypothesis"`
	Agent                ledger.Agent     `json:"agent"`
	Loadout              portable.Loadout `json:"loadout"`
	MinimumElapsedDays   int              `json:"minimum_elapsed_days"`
	AssignmentPolicy     string           `json:"assignment_policy"`
	AssignmentSeedSHA256 string           `json:"assignment_seed_sha256"`
	Tasks                []PlannedTask    `json:"tasks"`
	Privacy              string           `json:"privacy"`
}

type BoundEvent struct {
	EventID      string `json:"event_id"`
	RecordSHA256 string `json:"record_sha256"`
}

type Reporter struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type ObservationRequest struct {
	SchemaVersion      string   `json:"schema_version"`
	StudyID            string   `json:"study_id"`
	TaskID             string   `json:"task_id"`
	ExecutionReceiptID string   `json:"execution_receipt_id"`
	Reporter           Reporter `json:"reporter"`
	Reason             string   `json:"reason"`
	Privacy            string   `json:"privacy"`
}

type AssertionResult struct {
	Kind           string          `json:"kind"`
	Path           string          `json:"path,omitempty"`
	ExpectedSHA256 string          `json:"expected_sha256"`
	ActualSHA256   string          `json:"actual_sha256,omitempty"`
	Status         string          `json:"status"`
	CapturedBlob   *ledger.BlobRef `json:"captured_blob,omitempty"`
}

type OutcomeEvidence struct {
	SchemaVersion    string            `json:"schema_version"`
	EvidenceID       string            `json:"evidence_id"`
	StudyID          string            `json:"study_id"`
	TaskID           string            `json:"task_id"`
	Execution        BoundEvent        `json:"execution"`
	AcceptanceSHA256 string            `json:"acceptance_sha256"`
	Outcome          Outcome           `json:"outcome"`
	AssertionResults []AssertionResult `json:"assertion_results"`
	Evaluator        string            `json:"evaluator"`
	EvaluatedAt      time.Time         `json:"evaluated_at"`
	Privacy          string            `json:"privacy"`
}

type Observation struct {
	SchemaVersion    string           `json:"schema_version"`
	ObservationID    string           `json:"observation_id"`
	StudyID          string           `json:"study_id"`
	TaskID           string           `json:"task_id"`
	Condition        Condition        `json:"condition"`
	Execution        BoundEvent       `json:"execution"`
	Outcome          Outcome          `json:"outcome"`
	OutcomeAuthority OutcomeAuthority `json:"outcome_authority"`
	OutcomeEvidence  *BoundEvent      `json:"outcome_evidence,omitempty"`
	Reporter         Reporter         `json:"reporter"`
	Reason           string           `json:"reason"`
	ObservedAt       time.Time        `json:"observed_at"`
	Privacy          string           `json:"privacy"`
}

type CreateResult struct {
	SchemaVersion string     `json:"schema_version"`
	Plan          Plan       `json:"plan"`
	Event         BoundEvent `json:"event"`
	Written       bool       `json:"written"`
	Privacy       string     `json:"privacy"`
}

type ObserveResult struct {
	SchemaVersion string           `json:"schema_version"`
	Outcome       *OutcomeEvidence `json:"outcome,omitempty"`
	Observation   Observation      `json:"observation"`
	Event         BoundEvent       `json:"event"`
	Privacy       string           `json:"privacy"`
}

type ArmSummary struct {
	Planned                int `json:"planned"`
	Observed               int `json:"observed"`
	Successes              int `json:"successes"`
	Failures               int `json:"failures"`
	SuccessRateBasisPoints int `json:"success_rate_basis_points"`
}

type Report struct {
	SchemaVersion            string     `json:"schema_version"`
	StudyID                  string     `json:"study_id"`
	GeneratedAt              time.Time  `json:"generated_at"`
	Status                   string     `json:"status"`
	Reasons                  []string   `json:"reasons"`
	PlannedTasks             int        `json:"planned_tasks"`
	ObservedTasks            int        `json:"observed_tasks"`
	MissingTaskIDs           []string   `json:"missing_task_ids"`
	ElapsedDays              int        `json:"elapsed_days"`
	MinimumElapsedDays       int        `json:"minimum_elapsed_days"`
	BuiltinEvaluatorOutcomes int        `json:"builtin_evaluator_outcomes"`
	SyntheticObservations    int        `json:"synthetic_observations"`
	Baseline                 ArmSummary `json:"baseline"`
	Memory                   ArmSummary `json:"memory"`
	SuccessDeltaBasisPoints  int        `json:"success_delta_basis_points"`
	ClaimBoundary            string     `json:"claim_boundary"`
	Privacy                  string     `json:"privacy"`
}

type VerificationReport struct {
	SchemaVersion       string   `json:"schema_version"`
	StudiesChecked      int      `json:"studies_checked"`
	ObservationsChecked int      `json:"observations_checked"`
	Issues              []string `json:"issues"`
	Privacy             string   `json:"privacy"`
}

type CreateOptions struct {
	PortableRoot string
	Now          func() time.Time
}
