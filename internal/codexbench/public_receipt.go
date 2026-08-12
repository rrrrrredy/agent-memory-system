package codexbench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

type publicSuite struct {
	SchemaVersion string            `json:"schema_version"`
	SuiteID       string            `json:"suite_id"`
	ProjectScope  string            `json:"project_scope"`
	Description   string            `json:"description"`
	ToolPolicy    string            `json:"tool_policy"`
	Tasks         []publicSuiteTask `json:"tasks"`
	Privacy       string            `json:"privacy"`
}

type publicSuiteTask struct {
	TaskID         string `json:"task_id"`
	ClusterID      string `json:"cluster_id"`
	MemoryText     string `json:"memory_text"`
	RetrievalQuery string `json:"retrieval_query"`
	Prompt         string `json:"prompt"`
	Expected       string `json:"expected"`
}

func BuildPublicReceipt(store *ledger.Store, reportPath, suitePath string) (PublicReceipt, error) {
	result := PublicReceipt{SchemaVersion: PublicReceiptSchema, Limitations: []string{}, Privacy: "synthetic_public_aggregate"}
	verification := Verify(store, reportPath)
	if len(verification.Issues) != 0 {
		return result, errors.New("benchmark must pass local evidence verification before public receipt export")
	}
	if verification.ReportSHA256 == "" {
		return result, errors.New("verified report identity is missing")
	}
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		return result, err
	}
	var report Report
	if err := decodeStrict(reportData, &report); err != nil {
		return result, err
	}
	if report.EfficacyClaim != "observed_benefit_on_sealed_suite_not_longitudinal_certification" {
		return result, errors.New("public receipt export requires an observed-benefit report")
	}
	planData, err := readBlob(store, report.PlanBlob)
	if err != nil {
		return result, err
	}
	var plan SealedPlan
	if err := decodeStrict(planData, &plan); err != nil {
		return result, err
	}
	inputData, err := readBlob(store, plan.InputPlanBlob)
	if err != nil {
		return result, err
	}
	var input Plan
	if err := decodeStrict(inputData, &input); err != nil || validatePlan(input) != nil {
		return result, errors.New("verified input plan is invalid")
	}
	suiteData, err := os.ReadFile(suitePath)
	if err != nil {
		return result, err
	}
	var suite publicSuite
	if err := decodePublicSuite(suiteData, &suite); err != nil {
		return result, fmt.Errorf("decode public suite: %w", err)
	}
	if err := validatePublicSuiteBinding(store, suite, input, plan, report); err != nil {
		return result, fmt.Errorf("public suite does not match the verified report and retrieval graph: %w", err)
	}
	oracleHashes := map[string]struct{}{}
	for _, task := range plan.Tasks {
		oracleHashes[task.OracleExecutable.SHA256] = struct{}{}
		if task.ToolPolicy != suite.ToolPolicy {
			return result, errors.New("sealed task tool policy differs from the public suite")
		}
	}
	if len(oracleHashes) != 1 {
		return result, errors.New("public receipt requires one exact oracle executable across the suite")
	}
	oracleSHA := ""
	for value := range oracleHashes {
		oracleSHA = value
	}
	result.SuiteID, result.SuiteSHA256 = report.SuiteID, sha256Hex(suiteData)
	result.ReportSHA256, result.PlanSHA256 = report.ReportSHA256, report.PlanSHA256
	result.InputPlanSHA256 = report.InputPlanBlob.SHA256
	result.CodexExecutableSHA256, result.CodexVersion = plan.CodexExecutable.SHA256, plan.CodexVersion
	result.RunnerExecutableSHA256, result.RunnerVersion = plan.RunnerExecutable.SHA256, plan.RunnerVersion
	result.OracleExecutableSHA256 = oracleSHA
	result.ModelSelector, result.ToolPolicy = plan.Model, suite.ToolPolicy
	result.StartedAt, result.FinishedAt, result.Summary = report.StartedAt, report.FinishedAt, report.Summary
	result.Authority, result.EfficacyClaim = report.Authority, report.EfficacyClaim
	result.RecordsChecked, result.BlobsChecked = verification.RecordsChecked, verification.BlobsChecked
	result.Limitations = []string{
		"frozen synthetic capability suite; not longitudinal learning certification",
		"raw Codex events, messages, stderr, and the evidence ledger remain local",
		"authenticated local Codex execution is bound by hashes but is not independently reproducible from this aggregate receipt",
	}
	result.ReceiptSHA256, err = publicReceiptSHA256(result)
	return result, err
}

func publicReceiptSHA256(receipt PublicReceipt) (string, error) {
	receipt.ReceiptSHA256 = ""
	data, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	return sha256Hex(data), nil
}

func decodePublicSuite(data []byte, suite *publicSuite) error {
	if err := decodeStrict(data, suite); err != nil {
		return err
	}
	if suite.SchemaVersion != "codex-memory-benchmark-suite/v1alpha1" || !safeID(suite.SuiteID) ||
		strings.TrimSpace(suite.ProjectScope) == "" || len(suite.ProjectScope) > 256 ||
		strings.TrimSpace(suite.Description) == "" || len(suite.Description) > 1024 || suite.ToolPolicy != "forbid" ||
		len(suite.Tasks) < minimumDistinctTaskClusters || len(suite.Tasks) > maximumTasks || suite.Privacy != "synthetic_public" {
		return errors.New("public suite envelope is invalid")
	}
	taskIDs, clusterIDs := map[string]struct{}{}, map[string]struct{}{}
	for _, task := range suite.Tasks {
		if !safeID(task.TaskID) || !safeID(task.ClusterID) || strings.TrimSpace(task.MemoryText) == "" ||
			strings.TrimSpace(task.RetrievalQuery) == "" || strings.TrimSpace(task.Prompt) == "" ||
			strings.TrimSpace(task.Expected) == "" || len(task.MemoryText) > 64*1024 || len(task.RetrievalQuery) > 64*1024 ||
			len(task.Prompt) > 64*1024 || len(task.Expected) > 4096 {
			return errors.New("public suite task is invalid")
		}
		if _, duplicate := taskIDs[task.TaskID]; duplicate {
			return errors.New("public suite repeats a task id")
		}
		if _, duplicate := clusterIDs[task.ClusterID]; duplicate {
			return errors.New("public suite repeats a cluster id")
		}
		taskIDs[task.TaskID], clusterIDs[task.ClusterID] = struct{}{}, struct{}{}
	}
	return nil
}

func validatePublicSuiteBinding(store *ledger.Store, suite publicSuite, input Plan, plan SealedPlan, report Report) error {
	if err := validatePublicSuiteStatic(suite, input, plan, report); err != nil {
		return err
	}
	for index, suiteTask := range suite.Tasks {
		source, sealed := input.Tasks[index], plan.Tasks[index]
		injection, err := retrieval.ResolveVerifiedInjection(store, source.InjectionID)
		if err != nil {
			return fmt.Errorf("task %s verified injection: %w", suiteTask.TaskID, err)
		}
		if injection.InjectionID != sealed.InjectionID || injection.RetrievalReceiptID != sealed.RetrievalReceiptID {
			return fmt.Errorf("task %s injection identity differs from its sealed task", suiteTask.TaskID)
		}
		if sha256Hex([]byte(injection.Content)) != sealed.MemorySHA256 {
			return fmt.Errorf("task %s injected context differs from its sealed task", suiteTask.TaskID)
		}
		if !reflect.DeepEqual(injection.Memories, sealed.MemoryReferences) {
			return fmt.Errorf("task %s injected memory references differ from its sealed task", suiteTask.TaskID)
		}
		retrievalReceipt, err := retrieval.ResolveVerifiedRetrieval(store, injection.RetrievalReceiptID)
		if err != nil || retrievalReceipt.Request.Query != suiteTask.RetrievalQuery ||
			retrievalReceipt.Request.Context.Project != suite.ProjectScope || len(retrievalReceipt.Result.Selected) != 1 ||
			!suiteMemoryMatches(suiteTask.MemoryText, retrievalReceipt.Result.Selected[0].Text) {
			return errors.New("public suite query differs from its verified retrieval")
		}
		selected := retrievalReceipt.Result.Selected[0]
		expectedReference := retrieval.MemoryReference{MemoryID: selected.MemoryID, RevisionID: selected.RevisionID}
		if selected.TextSHA256 != sha256Hex([]byte(selected.Text)) || len(injection.Memories) != 1 ||
			!reflect.DeepEqual(injection.Memories[0], expectedReference) {
			return errors.New("public suite memory reference differs from its retrieval result")
		}
	}
	return nil
}

func validatePublicSuiteStatic(suite publicSuite, input Plan, plan SealedPlan, report Report) error {
	if suite.SuiteID != report.SuiteID || input.SuiteID != suite.SuiteID || plan.SuiteID != suite.SuiteID ||
		len(suite.Tasks) != len(input.Tasks) || len(suite.Tasks) != len(plan.Tasks) || len(suite.Tasks) != len(report.Pairs) {
		return errors.New("public suite population differs from the verified population")
	}
	for index, suiteTask := range suite.Tasks {
		source, sealed, pair := input.Tasks[index], plan.Tasks[index], report.Pairs[index]
		if source.TaskID != suiteTask.TaskID || sealed.TaskID != suiteTask.TaskID || pair.TaskID != suiteTask.TaskID ||
			source.ClusterID != suiteTask.ClusterID || sealed.ClusterID != suiteTask.ClusterID || pair.ClusterID != suiteTask.ClusterID ||
			source.Prompt != suiteTask.Prompt || source.ToolPolicy != suite.ToolPolicy || sealed.ToolPolicy != suite.ToolPolicy ||
			pair.ToolPolicy != suite.ToolPolicy || source.InjectionID == "" || sealed.InjectionID != source.InjectionID ||
			sealed.MemorySource != "verified_injection" || pair.MemorySource != "verified_injection" ||
			len(source.OracleCommand) != 2 || len(sealed.OracleArguments) != 1 ||
			source.OracleCommand[1] != sha256Hex([]byte(strings.TrimSpace(suiteTask.Expected))) ||
			sealed.OracleArguments[0] != source.OracleCommand[1] {
			return errors.New("public suite task differs from its sealed task")
		}
	}
	return nil
}

func suiteMemoryMatches(suiteValue, selectedValue string) bool {
	return suiteValue == selectedValue+"."
}
