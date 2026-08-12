package codexbench

import (
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

type benchmarkArmStart struct {
	SchemaVersion   string `json:"schema_version"`
	PlanSHA256      string `json:"plan_sha256"`
	TaskID          string `json:"task_id"`
	Condition       string `json:"condition"`
	PromptSHA256    string `json:"prompt_sha256"`
	WorkspaceSHA256 string `json:"workspace_sha256"`
	CodexSHA256     string `json:"codex_sha256"`
	Model           string `json:"model"`
	ToolPolicy      string `json:"tool_policy"`
}

func Verify(store *ledger.Store, reportPath string) Verification {
	result := Verification{SchemaVersion: VerificationSchema, Issues: []string{}, Privacy: "local_only"}
	if store == nil || reportPath == "" {
		result.Issues = append(result.Issues, "store and report path are required")
		return result
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("read report: %v", err))
		return result
	}
	var report Report
	if err := decodeStrict(data, &report); err != nil || report.SchemaVersion != ReportSchema || report.Privacy != "local_only" {
		result.Issues = append(result.Issues, "benchmark report envelope is invalid")
		return result
	}
	result.ReportSHA256, result.PlanSHA256 = report.ReportSHA256, report.PlanSHA256
	digest, err := reportSHA256(report)
	if err != nil || digest != report.ReportSHA256 {
		result.Issues = append(result.Issues, "benchmark report self-hash is invalid")
	}
	ledgerReport := store.Verify()
	result.RecordsChecked = ledgerReport.RecordsChecked
	for _, issue := range ledgerReport.Issues {
		result.Issues = append(result.Issues, "evidence ledger: "+issue)
	}
	records := map[string]ledger.Record{}
	checkedBlobs := map[string]struct{}{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, duplicate := records[record.Event.EventID]; duplicate {
			return errors.New("duplicate event id")
		}
		records[record.Event.EventID] = record
		if record.Event.Payload != nil && record.Event.Payload.Blob != nil {
			checkedBlobs[blobKey(*record.Event.Payload.Blob)] = struct{}{}
		}
		return nil
	}); err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("replay evidence ledger: %v", err))
	}
	reportRecord, reportOK := records["codex-benchmark-report-"+report.ReportSHA256]
	if !reportOK || reportRecord.Event.Payload == nil || reportRecord.Event.Payload.Blob == nil ||
		reportRecord.Event.Payload.MediaType != ReportMediaType || !containsParent(reportRecord.Event, report.PlanEventID) {
		result.Issues = append(result.Issues, "report event binding is invalid")
	} else if recordedReport, readErr := verifyReference(store, *reportRecord.Event.Payload.Blob, checkedBlobs); readErr != nil ||
		!reflect.DeepEqual(recordedReport, data) {
		result.Issues = append(result.Issues, "report event does not bind the supplied report bytes")
	}
	planRecord, ok := records[report.PlanEventID]
	if !ok || planRecord.Event.Payload == nil || planRecord.Event.Payload.Blob == nil ||
		!reflect.DeepEqual(*planRecord.Event.Payload.Blob, report.PlanBlob) {
		result.Issues = append(result.Issues, "sealed plan event or blob binding is invalid")
		return finishVerification(result, checkedBlobs)
	}
	planData, err := verifyReference(store, report.PlanBlob, checkedBlobs)
	if err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("read sealed plan: %v", err))
		return finishVerification(result, checkedBlobs)
	}
	var plan SealedPlan
	if err := decodeStrict(planData, &plan); err != nil || plan.SchemaVersion != SealedPlanSchema || plan.Privacy != "local_only" {
		result.Issues = append(result.Issues, "sealed plan envelope is invalid")
		return finishVerification(result, checkedBlobs)
	}
	planDigest, err := sealedPlanSHA256(plan)
	if err != nil || planDigest != plan.PlanSHA256 || plan.PlanSHA256 != report.PlanSHA256 ||
		!reflect.DeepEqual(plan.InputPlanBlob, report.InputPlanBlob) {
		result.Issues = append(result.Issues, "sealed plan hash or input-plan binding is invalid")
	}
	inputData, err := verifyReference(store, plan.InputPlanBlob, checkedBlobs)
	var input Plan
	if err != nil || decodeStrict(inputData, &input) != nil || validatePlan(input) != nil ||
		input.SuiteID != plan.SuiteID || input.Model != plan.Model || input.TimeoutSeconds != plan.TimeoutSeconds || len(input.Tasks) != len(plan.Tasks) {
		result.Issues = append(result.Issues, "input plan cannot be replayed against the sealed plan")
	}
	if !validBoundArtifact(store, plan.CodexExecutable, checkedBlobs) || !validBoundArtifact(store, plan.RunnerExecutable, checkedBlobs) {
		result.Issues = append(result.Issues, "runner or Codex executable binding is invalid")
	}
	sealedTasks := map[string]SealedTask{}
	inputTasks := map[string]Task{}
	memoryContexts := map[string]string{}
	for index, task := range plan.Tasks {
		if _, duplicate := sealedTasks[task.TaskID]; duplicate {
			result.Issues = append(result.Issues, "sealed plan repeats a task id")
		}
		sealedTasks[task.TaskID] = task
		if index >= len(input.Tasks) {
			continue
		}
		source := input.Tasks[index]
		memoryContext := source.MemoryContext
		expectedMemorySource := "caller_provided"
		expectedRetrievalID := ""
		expectedReferences := []retrieval.MemoryReference{}
		if source.InjectionID != "" {
			injection, injectionErr := retrieval.ResolveVerifiedInjection(store, source.InjectionID)
			if injectionErr != nil {
				result.Issues = append(result.Issues, fmt.Sprintf("sealed task %s injection cannot be replayed: %v", task.TaskID, injectionErr))
			} else {
				memoryContext = injection.Content
				expectedMemorySource = "verified_injection"
				expectedRetrievalID = injection.RetrievalReceiptID
				expectedReferences = append(expectedReferences, injection.Memories...)
			}
		}
		expectedOrder := benchmarkExecutionOrder(source, memoryContext, task.WorkspaceSHA256,
			task.OracleOverlaySHA256, task.OracleExecutable.SHA256)
		if source.TaskID != task.TaskID || source.ClusterID != task.ClusterID || source.ToolPolicy != task.ToolPolicy ||
			sha256Hex([]byte(source.Prompt)) != task.PromptSHA256 || sha256Hex([]byte(memoryContext)) != task.MemorySHA256 ||
			task.MemorySource != expectedMemorySource || task.InjectionID != source.InjectionID ||
			task.RetrievalReceiptID != expectedRetrievalID || !reflect.DeepEqual(task.MemoryReferences, expectedReferences) ||
			task.WorkspaceSHA256 != artifactSetSHA256(task.WorkspaceFiles) ||
			task.OracleOverlaySHA256 != artifactSetSHA256(task.OracleFiles) ||
			!reflect.DeepEqual(task.OracleArguments, source.OracleCommand[1:]) ||
			!reflect.DeepEqual(task.ExecutionOrder, expectedOrder) ||
			!validBoundArtifact(store, task.OracleExecutable, checkedBlobs) ||
			!validArtifacts(store, task.WorkspaceFiles, checkedBlobs) || !validArtifacts(store, task.OracleFiles, checkedBlobs) {
			result.Issues = append(result.Issues, fmt.Sprintf("sealed task %s does not match its input or artifacts", task.TaskID))
		}
		inputTasks[task.TaskID] = source
		memoryContexts[task.TaskID] = memoryContext
	}
	if len(report.Pairs) != len(plan.Tasks) {
		result.Issues = append(result.Issues, "report does not cover the complete sealed task population")
	}
	seenPairs := map[string]struct{}{}
	for _, pair := range report.Pairs {
		sealedTask, ok := sealedTasks[pair.TaskID]
		if !ok || pair.ClusterID != sealedTask.ClusterID || pair.ToolPolicy != sealedTask.ToolPolicy ||
			pair.MemorySource != sealedTask.MemorySource {
			result.Issues = append(result.Issues, fmt.Sprintf("reported pair %s is not bound to the sealed task", pair.TaskID))
			continue
		}
		if _, duplicate := seenPairs[pair.TaskID]; duplicate {
			result.Issues = append(result.Issues, fmt.Sprintf("report repeats task %s", pair.TaskID))
		}
		seenPairs[pair.TaskID] = struct{}{}
		verifyArm(store, records, report.PlanEventID, plan, sealedTask, inputTasks[pair.TaskID],
			memoryContexts[pair.TaskID], pair.Baseline, "baseline", checkedBlobs, &result)
		verifyArm(store, records, report.PlanEventID, plan, sealedTask, inputTasks[pair.TaskID],
			memoryContexts[pair.TaskID], pair.Treatment, "memory", checkedBlobs, &result)
		expectedOutcome := "tie"
		if pair.Treatment.OraclePassed && !pair.Baseline.OraclePassed {
			expectedOutcome = "win"
		} else if !pair.Treatment.OraclePassed && pair.Baseline.OraclePassed {
			expectedOutcome = "loss"
		}
		if pair.Outcome != expectedOutcome || pair.TokenDelta != totalTokens(pair.Treatment.Usage)-totalTokens(pair.Baseline.Usage) {
			result.Issues = append(result.Issues, fmt.Sprintf("pair %s outcome or token delta is invalid", pair.TaskID))
		}
	}
	rebuilt := report
	rebuilt.Issues = []string{}
	classifyReport(&rebuilt)
	if !reflect.DeepEqual(rebuilt.Summary, report.Summary) || rebuilt.Authority != report.Authority ||
		rebuilt.EfficacyClaim != report.EfficacyClaim || !reflect.DeepEqual(rebuilt.Issues, report.Issues) {
		result.Issues = append(result.Issues, "report classification does not replay from its paired results")
	}
	return finishVerification(result, checkedBlobs)
}

func verifyArm(store *ledger.Store, records map[string]ledger.Record, planEventID string, plan SealedPlan,
	sealedTask SealedTask, source Task, memoryContext string, arm ArmResult, condition string,
	checkedBlobs map[string]struct{}, result *Verification) {
	taskID := sealedTask.TaskID
	if arm.Condition != condition || arm.TaskID != taskID || arm.ExecutionOrder != conditionIndex(sealedTask.ExecutionOrder, condition) {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s arm identity is invalid", taskID, condition))
		return
	}
	started, startedOK := records[arm.StartedEventID]
	terminal, terminalOK := records[arm.ResultEventID]
	if !startedOK || !terminalOK || terminal.Event.Payload == nil || terminal.Event.Payload.Blob == nil ||
		started.Event.Payload == nil || started.Event.Payload.Blob == nil ||
		terminal.Event.Kind != ledger.KindToolResult || !containsParent(started.Event, planEventID) ||
		!containsParent(terminal.Event, arm.StartedEventID) {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s event chain is invalid", taskID, condition))
		return
	}
	startedData, startedErr := verifyReference(store, *started.Event.Payload.Blob, checkedBlobs)
	var declaration benchmarkArmStart
	resolvedSource := source
	resolvedSource.MemoryContext = memoryContext
	expectedDeclaration := benchmarkArmStart{SchemaVersion: "codex-benchmark-arm-start/v1alpha1",
		PlanSHA256: plan.PlanSHA256, TaskID: taskID, Condition: condition,
		PromptSHA256:    sha256Hex([]byte(benchmarkPrompt(resolvedSource, condition))),
		WorkspaceSHA256: sealedTask.WorkspaceSHA256, CodexSHA256: plan.CodexExecutable.SHA256,
		Model: plan.Model, ToolPolicy: sealedTask.ToolPolicy}
	if startedErr != nil || decodeStrict(startedData, &declaration) != nil ||
		!reflect.DeepEqual(declaration, expectedDeclaration) || arm.StartedEventID != "codex-benchmark-start-"+sha256Hex(startedData) ||
		arm.WorkspaceBeforeSHA256 != sealedTask.WorkspaceSHA256 {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s start declaration is invalid", taskID, condition))
	}
	data, err := verifyReference(store, *terminal.Event.Payload.Blob, checkedBlobs)
	var recorded ArmResult
	expected := arm
	expected.ResultEventID = ""
	if err != nil || decodeStrict(data, &recorded) != nil || !reflect.DeepEqual(recorded, expected) ||
		arm.ResultEventID != "codex-benchmark-result-"+sha256Hex(data) {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s result payload is invalid", taskID, condition))
	}
	rawData, rawErr := verifyReference(store, arm.RawEventsBlob, checkedBlobs)
	if rawErr != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s raw events blob is invalid", taskID, condition))
	} else if replayErr := verifyRawArmEvidence(arm, sealedTask.ToolPolicy, rawData); replayErr != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s raw events replay failed: %v", taskID, condition, replayErr))
	}
	if _, err := verifyReference(store, arm.OracleOutputBlob, checkedBlobs); err != nil {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s oracle output blob is invalid", taskID, condition))
	}
	if arm.AgentMessageBlob == nil {
		if arm.AgentMessageSHA256 != "" {
			result.Issues = append(result.Issues, fmt.Sprintf("task %s %s agent message binding is invalid", taskID, condition))
		}
	} else if message, messageErr := verifyReference(store, *arm.AgentMessageBlob, checkedBlobs); messageErr != nil ||
		sha256Hex(message) != arm.AgentMessageSHA256 {
		result.Issues = append(result.Issues, fmt.Sprintf("task %s %s agent message blob is invalid", taskID, condition))
	}
	if arm.StderrBlob != nil {
		if _, err := verifyReference(store, *arm.StderrBlob, checkedBlobs); err != nil {
			result.Issues = append(result.Issues, fmt.Sprintf("task %s %s stderr blob is invalid", taskID, condition))
		}
	}
}

func verifyRawArmEvidence(arm ArmResult, toolPolicy string, rawData []byte) error {
	parsed, err := parseExecutionJSONL(rawData)
	if err != nil {
		return err
	}
	if parsed.ThreadID != arm.ThreadID || !reflect.DeepEqual(parsed.Usage, arm.Usage) ||
		parsed.ToolCalls != arm.ToolCalls || sha256Hex([]byte(parsed.AgentMessage)) != arm.AgentMessageSHA256 {
		return errors.New("reported thread, usage, tool calls, or agent message differs from raw Codex events")
	}
	expectedOraclePassed := arm.CodexExitCode == 0 && arm.OracleExitCode == 0 &&
		(toolPolicy != "forbid" || parsed.ToolCalls == 0)
	if arm.OraclePassed != expectedOraclePassed {
		return errors.New("reported oracle result does not replay from exits and tool policy")
	}
	return nil
}

func conditionIndex(order []string, condition string) int {
	for index, value := range order {
		if value == condition {
			return index
		}
	}
	return -1
}
func containsParent(event ledger.Event, parent string) bool {

	if event.Causality == nil {
		return false
	}
	for _, candidate := range event.Causality.ParentEventIDs {
		if candidate == parent {
			return true
		}
	}
	return false
}

func validBoundArtifact(store *ledger.Store, artifact Artifact, checkedBlobs map[string]struct{}) bool {
	if artifact.SHA256 == "" || artifact.Bytes < 0 || artifact.Blob.SHA256 != artifact.SHA256 || artifact.Blob.Bytes != artifact.Bytes {
		return false
	}
	_, err := verifyReference(store, artifact.Blob, checkedBlobs)
	return err == nil
}

func validArtifacts(store *ledger.Store, items []Artifact, checkedBlobs map[string]struct{}) bool {
	for _, item := range items {
		if !validBoundArtifact(store, item, checkedBlobs) {
			return false
		}
	}
	return true
}

func readBlob(store *ledger.Store, reference ledger.BlobRef) ([]byte, error) {
	file, err := store.OpenBlob(reference)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != reference.Bytes || sha256Hex(data) != reference.SHA256 {
		return nil, errors.New("blob content does not match its reference")
	}
	return data, nil
}

func verifyReference(store *ledger.Store, reference ledger.BlobRef, checkedBlobs map[string]struct{}) ([]byte, error) {
	data, err := readBlob(store, reference)
	if err == nil {
		checkedBlobs[blobKey(reference)] = struct{}{}
	}
	return data, err
}

func blobKey(reference ledger.BlobRef) string {
	return fmt.Sprintf("%s:%d:%s", reference.SHA256, reference.Bytes, reference.RelativePath)
}

func finishVerification(result Verification, checkedBlobs map[string]struct{}) Verification {
	result.BlobsChecked = len(checkedBlobs)
	result.Issues = append([]string{}, result.Issues...)
	sort.Strings(result.Issues)
	return result
}
