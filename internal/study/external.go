package study

import (
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	ExternalPlanSchema           = "runtime-evolution-study.v1"
	ExternalPlanMediaType        = "application/vnd.runtime-evolution-study+json;version=1"
	ExternalRegisterResultSchema = "runtime-evolution-study-registration.v1"
	ExternalAdapterVersion       = "runtime-evolution-study/v1"
)

var externalHypothesisID = regexp.MustCompile(`^H[1-9][0-9]*$`)

type ExternalHypothesis struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Direction string `json:"direction"`
}

type ExternalCondition struct {
	ConditionID      string `json:"condition_id"`
	OptimizerContext string `json:"optimizer_context"`
}

type ExternalDataset struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Cases  int    `json:"cases"`
}

type ExternalDatasets struct {
	Failure    ExternalDataset `json:"failure"`
	Protection ExternalDataset `json:"protection"`
	Transfer   ExternalDataset `json:"transfer"`
}

type ExternalAssignment struct {
	Method string `json:"method"`
	Seed   int    `json:"seed"`
}

type ExternalCandidateGate struct {
	Improve         string `json:"improve"`
	Tie             string `json:"tie"`
	Degrade         string `json:"degrade"`
	SecurityFailure string `json:"security_failure"`
}

type ExternalArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ExternalToolArtifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type ExternalArtifacts struct {
	InitialSkill    ExternalArtifact     `json:"initial_skill"`
	Harness         ExternalArtifact     `json:"harness"`
	HarnessTests    ExternalArtifact     `json:"harness_tests"`
	ExecutorSchema  ExternalArtifact     `json:"executor_schema"`
	OptimizerSchema ExternalArtifact     `json:"optimizer_schema"`
	SecurityGuard   ExternalToolArtifact `json:"security_guard"`
}

type ExternalPlan struct {
	SchemaVersion          string                `json:"schema_version"`
	StudyID                string                `json:"study_id"`
	Title                  string                `json:"title"`
	CreatedAt              time.Time             `json:"created_at"`
	Scenario               string                `json:"scenario"`
	Hypotheses             []ExternalHypothesis  `json:"hypotheses"`
	Conditions             []ExternalCondition   `json:"conditions"`
	Replicates             int                   `json:"replicates"`
	IterationsPerReplicate int                   `json:"iterations_per_replicate"`
	SourceModel            string                `json:"source_model"`
	TransferModels         []string              `json:"transfer_models"`
	Datasets               ExternalDatasets      `json:"datasets"`
	Metrics                []string              `json:"metrics"`
	Assignment             ExternalAssignment    `json:"assignment"`
	CandidateGate          ExternalCandidateGate `json:"candidate_gate"`
	Artifacts              *ExternalArtifacts    `json:"artifacts,omitempty"`
	ClaimBoundary          string                `json:"claim_boundary"`
	Privacy                string                `json:"privacy"`
}

type ExternalRegisterResult struct {
	SchemaVersion string       `json:"schema_version"`
	Study         ExternalPlan `json:"study"`
	Event         BoundEvent   `json:"event"`
	FileSHA256    string       `json:"file_sha256"`
	Written       bool         `json:"written"`
	Privacy       string       `json:"privacy"`
}

func DecodeExternalPlan(reader io.Reader) (ExternalPlan, error) {
	var plan ExternalPlan
	if err := decodeStrict(reader, &plan, 2<<20); err != nil {
		return plan, fmt.Errorf("decode runtime evolution study: %w", err)
	}
	if err := validateExternalPlan(plan); err != nil {
		return plan, err
	}
	return plan, nil
}

func RegisterExternal(store *ledger.Store, plan ExternalPlan) (ExternalRegisterResult, error) {
	result := ExternalRegisterResult{SchemaVersion: ExternalRegisterResultSchema, Study: plan, Privacy: PrivacyLocalOnly}
	if store == nil {
		return result, errors.New("local evidence store is required")
	}
	if err := validateExternalPlan(plan); err != nil {
		return result, err
	}
	data, err := canonicalJSON(plan)
	if err != nil {
		return result, err
	}
	contentSHA := digest(data)
	result.FileSHA256 = contentSHA
	eventID := "external-study-" + contentSHA
	var existing *ledger.Record
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		if record.Event.EventID == eventID && (record.Event.Payload == nil || record.Event.Payload.MediaType != ExternalPlanMediaType) {
			return errors.New("runtime evolution study event identity conflicts with another record")
		}
		if record.Event.Payload == nil || record.Event.Payload.MediaType != ExternalPlanMediaType || record.Event.Payload.Content == nil {
			return nil
		}
		var retained ExternalPlan
		if err := decodeStrict(strings.NewReader(*record.Event.Payload.Content), &retained, 2<<20); err != nil {
			return fmt.Errorf("decode retained runtime evolution study: %w", err)
		}
		if retained.StudyID != plan.StudyID {
			return nil
		}
		if record.Event.Payload.SHA256 != contentSHA {
			return errors.New("runtime evolution study id already exists with different content")
		}
		copyRecord := record
		existing = &copyRecord
		return nil
	})
	if err != nil {
		return result, err
	}
	if existing != nil {
		result.Event = BoundEvent{EventID: existing.Event.EventID, RecordSHA256: existing.RecordHash}
		return result, closeAppender(appender, nil)
	}
	payload := ledger.InlinePayload("json", ExternalPlanMediaType, string(data))
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          ledger.KindEvaluationTrialPlan,
		ObservedAt:    plan.CreatedAt.UTC(),
		RecordedAt:    plan.CreatedAt.UTC(),
		Source: ledger.Source{
			Agent:          ledger.AgentUnknown,
			Adapter:        "agentmem-external-study",
			AdapterVersion: ExternalAdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       plan.StudyID,
			SourceEventID:  eventID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	records, err := appender.AppendBatch([]ledger.Event{event})
	if err != nil {
		return result, closeAppender(appender, err)
	}
	if err := closeAppender(appender, nil); err != nil {
		return result, err
	}
	result.Event = BoundEvent{EventID: eventID, RecordSHA256: records[0].RecordHash}
	result.Written = true
	return result, nil
}

func validateExternalPlan(plan ExternalPlan) error {
	if plan.SchemaVersion != ExternalPlanSchema || plan.Privacy != PrivacyLocalOnly ||
		!externalSlug(plan.StudyID) || plan.CreatedAt.IsZero() ||
		!boundedText(plan.Title, 1, 256) || !boundedText(plan.Scenario, 1, 2048) ||
		!boundedText(plan.SourceModel, 1, 128) || !boundedText(plan.ClaimBoundary, 1, 2048) ||
		plan.Replicates < 3 || plan.Replicates > 100 ||
		plan.IterationsPerReplicate < 3 || plan.IterationsPerReplicate > 100 {
		return errors.New("runtime evolution study envelope is invalid")
	}
	if err := validateExternalHypotheses(plan.Hypotheses); err != nil {
		return err
	}
	if err := validateExternalConditions(plan.Conditions); err != nil {
		return err
	}
	if err := validateExternalStrings(plan.TransferModels, 1, 16, map[string]struct{}{}); err != nil {
		return fmt.Errorf("runtime evolution transfer models are invalid: %w", err)
	}
	allowedMetrics := map[string]struct{}{
		"task_quality": {}, "tool_calls": {}, "input_tokens": {}, "output_tokens": {},
		"wall_time_ms": {}, "rule_lines": {}, "rule_words": {}, "rollback_count": {}, "cross_model_transfer": {},
	}
	if err := validateExternalStrings(plan.Metrics, 5, len(allowedMetrics), allowedMetrics); err != nil {
		return fmt.Errorf("runtime evolution metrics are invalid: %w", err)
	}
	for _, dataset := range []ExternalDataset{plan.Datasets.Failure, plan.Datasets.Protection, plan.Datasets.Transfer} {
		if !externalRelativePath(dataset.Path) || !externalSHA256(dataset.SHA256) || dataset.Cases < 1 || dataset.Cases > 100000 {
			return errors.New("runtime evolution dataset binding is invalid")
		}
	}
	if plan.Assignment.Method != "fixed_seed_round_robin" ||
		plan.CandidateGate.Improve != "activate" || plan.CandidateGate.Tie != "hold" ||
		plan.CandidateGate.Degrade != "rollback" || plan.CandidateGate.SecurityFailure != "block" {
		return errors.New("runtime evolution assignment or candidate gate is invalid")
	}
	if plan.Artifacts != nil {
		for _, artifact := range []ExternalArtifact{
			plan.Artifacts.InitialSkill, plan.Artifacts.Harness, plan.Artifacts.HarnessTests,
			plan.Artifacts.ExecutorSchema, plan.Artifacts.OptimizerSchema,
		} {
			if !externalRelativePath(artifact.Path) || !externalSHA256(artifact.SHA256) {
				return errors.New("runtime evolution artifact binding is invalid")
			}
		}
		tool := plan.Artifacts.SecurityGuard
		if !boundedText(tool.Name, 1, 128) || !boundedText(tool.Version, 1, 64) || !externalSHA256(tool.SHA256) {
			return errors.New("runtime evolution tool artifact binding is invalid")
		}
	}
	return nil
}

func validateExternalHypotheses(values []ExternalHypothesis) error {
	if len(values) < 1 || len(values) > 32 {
		return errors.New("runtime evolution hypotheses are invalid")
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !externalHypothesisID.MatchString(value.ID) || !boundedText(value.Statement, 1, 2048) ||
			(value.Direction != "greater" && value.Direction != "less" && value.Direction != "different" && value.Direction != "descriptive") {
			return errors.New("runtime evolution hypothesis is invalid")
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("runtime evolution hypothesis ids must be unique")
		}
		seen[value.ID] = struct{}{}
	}
	return nil
}

func validateExternalConditions(values []ExternalCondition) error {
	expected := map[string]string{
		"no_wiki": "current_trace_only", "flat_history": "chronological_history", "persistent_wiki": "pattern_registry",
	}
	if len(values) != len(expected) {
		return errors.New("runtime evolution study requires exactly three conditions")
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		context, ok := expected[value.ConditionID]
		if !ok || context != value.OptimizerContext {
			return errors.New("runtime evolution condition is invalid")
		}
		if _, duplicate := seen[value.ConditionID]; duplicate {
			return errors.New("runtime evolution conditions must be unique")
		}
		seen[value.ConditionID] = struct{}{}
	}
	return nil
}

func validateExternalStrings(values []string, minimum, maximum int, allowed map[string]struct{}) error {
	if len(values) < minimum || len(values) > maximum {
		return errors.New("list length is invalid")
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if !boundedText(value, 1, 128) {
			return errors.New("list value is invalid")
		}
		if len(allowed) != 0 {
			if _, ok := allowed[value]; !ok {
				return errors.New("list value is unsupported")
			}
		}
		if _, duplicate := seen[value]; duplicate {
			return errors.New("list values must be unique")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func externalRelativePath(value string) bool {
	return value != "" && !strings.Contains(value, `\`) && !strings.HasPrefix(value, "/") &&
		!strings.Contains(value, ":") && path.Clean(value) == value && value != "." &&
		!strings.HasPrefix(value, "../")
}

func externalSHA256(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validSHA256(strings.TrimPrefix(value, "sha256:"))
}

func externalSlug(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		if character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return !strings.Contains(value, "--")
}

func boundedText(value string, minimum, maximum int) bool {
	trimmed := strings.TrimSpace(value)
	return len([]byte(trimmed)) >= minimum && len([]byte(trimmed)) <= maximum && trimmed == value
}
