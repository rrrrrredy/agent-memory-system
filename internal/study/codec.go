package study

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

func DecodeDraft(reader io.Reader) (Draft, error) {
	var draft Draft
	if err := decodeStrict(reader, &draft, 512*1024); err != nil {
		return draft, fmt.Errorf("decode longitudinal study draft: %w", err)
	}
	if err := validateDraft(draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func DecodeObservationRequest(reader io.Reader) (ObservationRequest, error) {
	var request ObservationRequest
	if err := decodeStrict(reader, &request, 256*1024); err != nil {
		return request, fmt.Errorf("decode longitudinal study observation: %w", err)
	}
	if err := validateObservationRequest(request); err != nil {
		return ObservationRequest{}, err
	}
	return request, nil
}

func decodeStrict(reader io.Reader, target any, limit int64) error {
	if reader == nil {
		return errors.New("reader is required")
	}
	decoder := json.NewDecoder(io.LimitReader(reader, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("input contains trailing JSON")
	}
	return nil
}

func validateDraft(draft Draft) error {
	if draft.SchemaVersion != DraftSchema || draft.Privacy != PrivacyLocalOnly ||
		strings.TrimSpace(draft.Name) == "" || len([]byte(draft.Name)) > 128 ||
		strings.TrimSpace(draft.Hypothesis) == "" || len([]byte(draft.Hypothesis)) > 2048 ||
		draft.Agent != "codex" || !validPrefixedHash(draft.LoadoutID, "loadout-") ||
		draft.MinimumElapsedDays < 1 || draft.MinimumElapsedDays > 365 ||
		len(draft.Tasks) < 4 || len(draft.Tasks) > 500 || len(draft.Tasks)%2 != 0 {
		return errors.New("longitudinal study draft is invalid")
	}
	seenTasks, seenClusters := map[string]struct{}{}, map[string]struct{}{}
	for _, task := range draft.Tasks {
		if err := validateTaskContract(task); err != nil {
			return err
		}
		if _, exists := seenTasks[task.TaskID]; exists {
			return errors.New("longitudinal study task ids must be unique")
		}
		if _, exists := seenClusters[task.ClusterID]; exists {
			return errors.New("longitudinal study cluster ids must be unique")
		}
		seenTasks[task.TaskID], seenClusters[task.ClusterID] = struct{}{}, struct{}{}
	}
	return nil
}

func validateObservationRequest(request ObservationRequest) error {
	if request.SchemaVersion != ObserveRequestSchema || request.Privacy != PrivacyLocalOnly ||
		!validPrefixedHash(request.StudyID, "study-") || !safeID(request.TaskID) ||
		!validPrefixedHash(request.ExecutionReceiptID, "native-agent-receipt-") ||
		strings.TrimSpace(request.Reporter.ID) == "" || len(request.Reporter.ID) > 128 ||
		len(strings.TrimSpace(request.Reason)) < 10 || len([]byte(request.Reason)) > 2048 {
		return errors.New("longitudinal study observation request is invalid")
	}
	if request.Reporter.Kind != "caller_attestation" && request.Reporter.Kind != "synthetic_test" {
		return errors.New("longitudinal study reporter kind is invalid")
	}
	return nil
}

func buildPlan(store *ledger.Store, draft Draft, loadout portable.Loadout, createdAt time.Time) (Plan, error) {
	tasks := append([]TaskDraft(nil), draft.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	plan := Plan{
		SchemaVersion: PlanSchema, CreatedAt: createdAt.UTC(), Name: strings.TrimSpace(draft.Name),
		Hypothesis: strings.TrimSpace(draft.Hypothesis), Agent: draft.Agent, Loadout: loadout,
		MinimumElapsedDays: draft.MinimumElapsedDays, AssignmentPolicy: AssignmentPolicy,
		Tasks: make([]PlannedTask, len(tasks)), Privacy: PrivacyLocalOnly,
	}
	for index, task := range tasks {
		resolved, err := filepath.EvalSymlinks(filepath.Clean(task.WorkingDirectory))
		if err != nil {
			return Plan{}, fmt.Errorf("resolve longitudinal study working directory: %w", err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return Plan{}, errors.New("longitudinal study working directory is not a directory")
		}
		snapshot, err := snapshotWorkspace(store, resolved)
		if err != nil {
			return Plan{}, fmt.Errorf("seal longitudinal study workspace: %w", err)
		}
		assertions := append([]AcceptanceAssertion(nil), task.Acceptance.Assertions...)
		sort.Slice(assertions, func(i, j int) bool { return assertionKey(assertions[i]) < assertionKey(assertions[j]) })
		plan.Tasks[index] = PlannedTask{TaskID: task.TaskID, ClusterID: task.ClusterID,
			Prompt: task.Prompt, Model: task.Model, Sandbox: task.Sandbox, WorkingDirectory: resolved,
			TimeoutSeconds: task.TimeoutSeconds, SkipGitRepositoryCheck: task.SkipGitRepositoryCheck,
			WorkspaceSnapshot: snapshot,
			Acceptance:        AcceptanceContract{SchemaVersion: AcceptanceSchema, Mode: "all", Assertions: assertions},
			Order:             index + 1}
	}
	seed, err := assignmentSeed(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.AssignmentSeedSHA256 = seed
	id, err := planIdentity(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.StudyID = id
	offset := int(seed[len(seed)-1] - '0')
	if seed[len(seed)-1] >= 'a' {
		offset = int(seed[len(seed)-1]-'a') + 10
	}
	for index := range plan.Tasks {
		plan.Tasks[index].Condition = ConditionBaseline
		if (index+offset)%2 == 1 {
			plan.Tasks[index].Condition = ConditionMemory
		}
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func validatePlan(plan Plan) error {
	if plan.SchemaVersion != PlanSchema || plan.Privacy != PrivacyLocalOnly ||
		plan.CreatedAt.IsZero() || plan.Agent != "codex" || plan.AssignmentPolicy != AssignmentPolicy ||
		!validPrefixedHash(plan.StudyID, "study-") || !validSHA256(plan.AssignmentSeedSHA256) || portable.ValidateLoadout(plan.Loadout) != nil ||
		plan.MinimumElapsedDays < 1 || plan.MinimumElapsedDays > 365 ||
		len(plan.Tasks) < 4 || len(plan.Tasks) > 500 || len(plan.Tasks)%2 != 0 {
		return errors.New("longitudinal study plan envelope is invalid")
	}
	allowed := false
	for _, agent := range plan.Loadout.Agents {
		if agent == ledger.AgentCodex {
			allowed = true
			break
		}
	}
	if !allowed {
		return errors.New("longitudinal study loadout does not allow Codex")
	}
	expectedSeed, err := assignmentSeed(plan)
	if err != nil || expectedSeed != plan.AssignmentSeedSHA256 {
		return errors.New("longitudinal study assignment seed is invalid")
	}
	expectedID, err := planIdentity(plan)
	if err != nil || expectedID != plan.StudyID {
		return errors.New("longitudinal study plan identity is invalid")
	}
	offset := int(plan.AssignmentSeedSHA256[len(plan.AssignmentSeedSHA256)-1] - '0')
	if plan.AssignmentSeedSHA256[len(plan.AssignmentSeedSHA256)-1] >= 'a' {
		offset = int(plan.AssignmentSeedSHA256[len(plan.AssignmentSeedSHA256)-1]-'a') + 10
	}
	previous, baseline, memory := "", 0, 0
	clusters := map[string]struct{}{}
	for index, task := range plan.Tasks {
		expected := ConditionBaseline
		if (index+offset)%2 == 1 {
			expected = ConditionMemory
		}
		if err := validateTaskContract(TaskDraft{TaskID: task.TaskID, ClusterID: task.ClusterID,
			Prompt: task.Prompt, Model: task.Model, Sandbox: task.Sandbox,
			WorkingDirectory: task.WorkingDirectory, TimeoutSeconds: task.TimeoutSeconds,
			SkipGitRepositoryCheck: task.SkipGitRepositoryCheck, Acceptance: task.Acceptance}); err != nil || validateWorkspaceSnapshotEnvelope(task.WorkspaceSnapshot, task.WorkingDirectory) != nil ||
			task.TaskID <= previous || task.Order != index+1 || task.Condition != expected {
			return errors.New("longitudinal study task assignment is invalid")
		}
		if _, duplicate := clusters[task.ClusterID]; duplicate {
			return errors.New("longitudinal study cluster ids must be unique")
		}
		clusters[task.ClusterID] = struct{}{}
		previous = task.TaskID
		if task.Condition == ConditionBaseline {
			baseline++
		} else {
			memory++
		}
	}
	if baseline != memory {
		return errors.New("longitudinal study assignments are not counterbalanced")
	}
	return nil
}

func assignmentSeed(plan Plan) (string, error) {
	type assignmentEnvelope struct {
		Agent            ledger.Agent `json:"agent"`
		LoadoutID        string       `json:"loadout_id"`
		AssignmentPolicy string       `json:"assignment_policy"`
		Tasks            []TaskDraft  `json:"tasks"`
	}
	envelope := assignmentEnvelope{
		Agent: plan.Agent, LoadoutID: plan.Loadout.LoadoutID,
		AssignmentPolicy: plan.AssignmentPolicy,
		Tasks:            make([]TaskDraft, len(plan.Tasks)),
	}
	for index, task := range plan.Tasks {
		envelope.Tasks[index] = TaskDraft{TaskID: task.TaskID, ClusterID: task.ClusterID,
			Prompt: task.Prompt, Model: task.Model, Sandbox: task.Sandbox,
			WorkingDirectory: task.WorkingDirectory, TimeoutSeconds: task.TimeoutSeconds,
			SkipGitRepositoryCheck: task.SkipGitRepositoryCheck, Acceptance: task.Acceptance}
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func validateTaskContract(task TaskDraft) error {
	if !safeID(task.TaskID) || !safeID(task.ClusterID) ||
		strings.TrimSpace(task.Prompt) == "" || len([]byte(task.Prompt)) > 128*1024 ||
		strings.TrimSpace(task.Model) == "" || len(task.Model) > 128 ||
		!filepath.IsAbs(task.WorkingDirectory) || strings.ContainsRune(task.WorkingDirectory, 0) ||
		task.TimeoutSeconds < 30 || task.TimeoutSeconds > 3600 || !task.SkipGitRepositoryCheck {
		return errors.New("longitudinal study task contract is invalid")
	}
	if task.Sandbox != "read-only" && task.Sandbox != "workspace-write" {
		return errors.New("longitudinal study task sandbox is invalid")
	}
	return validateAcceptance(task.Acceptance)
}

func validateAcceptance(contract AcceptanceContract) error {
	if contract.SchemaVersion != AcceptanceSchema || contract.Mode != "all" ||
		len(contract.Assertions) == 0 || len(contract.Assertions) > 16 {
		return errors.New("longitudinal study acceptance contract is invalid")
	}
	previous := ""
	for _, assertion := range contract.Assertions {
		if !validSHA256(assertion.ExpectedSHA256) {
			return errors.New("longitudinal study acceptance digest is invalid")
		}
		switch assertion.Kind {
		case "agent_message_sha256":
			if assertion.Path != "" {
				return errors.New("agent message acceptance must not name a path")
			}
		default:
			return errors.New("longitudinal study acceptance assertion is unsupported")
		}
		key := assertionKey(assertion)
		if key <= previous {
			return errors.New("longitudinal study acceptance assertions must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func assertionKey(assertion AcceptanceAssertion) string {
	return assertion.Kind + "\x00" + filepath.ToSlash(assertion.Path)
}

func acceptanceSHA256(contract AcceptanceContract) (string, error) {
	data, err := json.Marshal(contract)
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func planIdentity(plan Plan) (string, error) {
	copyPlan := plan
	copyPlan.StudyID = ""
	copyPlan.Tasks = append([]PlannedTask(nil), plan.Tasks...)
	for index := range copyPlan.Tasks {
		copyPlan.Tasks[index].Condition = ""
	}
	data, err := json.Marshal(copyPlan)
	if err != nil {
		return "", err
	}
	return "study-" + digest(data), nil
}

func observationIdentity(observation Observation) (string, error) {
	copyObservation := observation
	copyObservation.ObservationID = ""
	data, err := json.Marshal(copyObservation)
	if err != nil {
		return "", err
	}
	return "study-observation-" + digest(data), nil
}

func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }
func canonicalEqual(data []byte, value any) bool {
	expected, err := canonicalJSON(value)
	return err == nil && bytes.Equal(data, expected)
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validPrefixedHash(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validSHA256(strings.TrimPrefix(value, prefix))
}
func safeID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c) {
			continue
		}
		return false
	}
	return true
}
