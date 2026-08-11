package evaluation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	ExecutionHarnessManifestSchema = "execution-harness-manifest/v1alpha1"
	AgentExecutionInputSchema      = "agent-execution-input/v1alpha1"
	TaskExecutionStartedSchema     = "task-execution-started/v1alpha1"
	TaskExecutionReceiptSchema     = "task-execution-receipt/v1alpha1"
	TaskExecutionResultSchema      = "task-execution-result/v1alpha1"
	executionSupervisorAdapter     = "evaluation-execution-supervisor"
	executionSupervisorVersion     = "evaluation-execution-supervisor/v1alpha1"
	TaskExecutionFailureSchema     = "task-execution-failure/v1alpha1"
	TaskExecutionFailureMediaType  = "application/vnd.agentmem.task-execution-failure+json"
)

type TaskExecutionOutcome string

const (
	TaskExecutionCompleted TaskExecutionOutcome = "completed"
	TaskExecutionFailed    TaskExecutionOutcome = "failed"
)

type ExecutionHarnessManifest struct {
	SchemaVersion        string   `json:"schema_version"`
	Arguments            []string `json:"arguments"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	WorkingDirectory     string   `json:"working_directory,omitempty"`
	InheritEnvironment   bool     `json:"inherit_environment"`
	RetrievalLimit       int      `json:"retrieval_limit"`
	RetrievalTokenBudget int      `json:"retrieval_token_budget"`
	RetrievalByteBudget  int      `json:"retrieval_byte_budget"`
	ResultMediaType      string   `json:"result_media_type"`
	ExecutableSuffix     string   `json:"executable_suffix,omitempty"`
	Privacy              string   `json:"privacy"`
}

type AgentExecutionInput struct {
	SchemaVersion    string                      `json:"schema_version"`
	Challenge        string                      `json:"challenge"`
	Agent            ledger.Agent                `json:"agent"`
	Provider         string                      `json:"provider"`
	Model            string                      `json:"model"`
	SystemPrompt     []byte                      `json:"system_prompt"`
	ToolRegistry     []byte                      `json:"tool_registry"`
	ExecutionConfig  []byte                      `json:"execution_config"`
	TaskSpec         []byte                      `json:"task_spec"`
	MemoryContext    string                      `json:"memory_context,omitempty"`
	MemoryReferences []retrieval.MemoryReference `json:"memory_references"`
	Privacy          string                      `json:"privacy"`
}

type TaskExecutionStarted struct {
	SchemaVersion            string              `json:"schema_version"`
	ExecutionID              string              `json:"execution_id"`
	TrialPlanID              string              `json:"trial_plan_id"`
	TrialPairID              string              `json:"trial_pair_id"`
	AttemptID                string              `json:"attempt_id"`
	Condition                TaskCondition       `json:"condition"`
	ChallengeSHA256          string              `json:"challenge_sha256"`
	Contract                 BoundEventReference `json:"contract"`
	SystemUnderTestSHA256    string              `json:"system_under_test_sha256"`
	SupervisorArtifactSHA256 string              `json:"supervisor_artifact_sha256"`
	ExecutableSHA256         string              `json:"executable_sha256"`
	ArgumentsSHA256          string              `json:"arguments_sha256"`
	EnvironmentSHA256        string              `json:"environment_sha256"`
	InputBlob                ledger.BlobRef      `json:"input_blob"`
	StartedAt                time.Time           `json:"started_at"`
	Privacy                  string              `json:"privacy"`
}

type TaskExecutionReceipt struct {
	SchemaVersion            string                      `json:"schema_version"`
	ReceiptID                string                      `json:"receipt_id"`
	ExecutionID              string                      `json:"execution_id"`
	TrialPlanID              string                      `json:"trial_plan_id"`
	TrialPairID              string                      `json:"trial_pair_id"`
	AttemptID                string                      `json:"attempt_id"`
	Condition                TaskCondition               `json:"condition"`
	Agent                    ledger.Agent                `json:"agent"`
	Provider                 string                      `json:"provider"`
	Model                    string                      `json:"model"`
	Contract                 BoundEventReference         `json:"contract"`
	Started                  BoundEventReference         `json:"started"`
	Result                   BoundEventReference         `json:"result"`
	SystemUnderTestSHA256    string                      `json:"system_under_test_sha256"`
	SupervisorArtifactSHA256 string                      `json:"supervisor_artifact_sha256"`
	ExecutableSHA256         string                      `json:"executable_sha256"`
	ArgumentsSHA256          string                      `json:"arguments_sha256"`
	EnvironmentSHA256        string                      `json:"environment_sha256"`
	InputBlob                ledger.BlobRef              `json:"input_blob"`
	ResultBlob               ledger.BlobRef              `json:"result_blob"`
	StdoutBlob               *ledger.BlobRef             `json:"stdout_blob,omitempty"`
	StderrBlob               *ledger.BlobRef             `json:"stderr_blob,omitempty"`
	Outcome                  TaskExecutionOutcome        `json:"outcome"`
	ResultMediaType          string                      `json:"result_media_type"`
	FailureKind              string                      `json:"failure_kind,omitempty"`
	ExitCode                 int                         `json:"exit_code"`
	StartedAt                time.Time                   `json:"started_at"`
	FinishedAt               time.Time                   `json:"finished_at"`
	RetrievalReceiptID       string                      `json:"retrieval_receipt_id,omitempty"`
	InjectionID              string                      `json:"injection_id,omitempty"`
	MemoryReferences         []retrieval.MemoryReference `json:"memory_references"`
	Privacy                  string                      `json:"privacy"`
}

type TaskExecutionFailure struct {
	SchemaVersion string          `json:"schema_version"`
	Kind          string          `json:"kind"`
	Message       string          `json:"message"`
	StdoutBlob    *ledger.BlobRef `json:"stdout_blob,omitempty"`
	StderrBlob    *ledger.BlobRef `json:"stderr_blob,omitempty"`
}

type ExecutePlannedAttemptOptions struct {
	PortableRoot string
	Now          func() time.Time
}

type TaskExecutionResult struct {
	SchemaVersion string               `json:"schema_version"`
	Request       TaskAttemptRequest   `json:"request"`
	Execution     TaskExecutionReceipt `json:"execution"`
	Privacy       string               `json:"privacy"`
}

func ExecutePlannedTaskAttempt(store *ledger.Store, request TaskAttemptRequest,
	options ExecutePlannedAttemptOptions) (TaskExecutionResult, error) {
	result := TaskExecutionResult{SchemaVersion: TaskExecutionResultSchema, Privacy: "local_only"}
	if store == nil || request.TrialPlanID == "" || request.TrialPairID == "" {
		return result, errors.New("store and a sealed trial-plan request are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := bindCurrentSystemArtifact(&request); err != nil {
		return result, err
	}
	if err := validatePreregisteredTaskAttemptRequest(request); err != nil {
		return result, err
	}
	if err := validateAttemptBlobs(store, request); err != nil {
		return result, err
	}
	records, _, err := loadIndexedRecords(store)
	if err != nil {
		return result, err
	}
	pair, err := loadExecutionTrialPair(store, records, request)
	if err != nil {
		return result, err
	}
	if err := enforceExecutionOrder(store, records, pair, request); err != nil {
		return result, err
	}
	contractRecord, exists := records[request.WindowStartEventID]
	if !exists || contractRecord.Record.Event.Kind != ledger.KindTaskAttemptContract {
		return result, errors.New("sealed task contract is unavailable")
	}
	contractData, err := eventPayload(store, contractRecord.Record.Event)
	var contract TaskAttemptContract
	if err != nil || decodeStrictEvaluationJSON(contractData, &contract) != nil ||
		!taskAttemptContractMatches(contract, request) || contractRecord.Record.Event.Causality == nil ||
		!sameStrings(contractRecord.Record.Event.Causality.ParentEventIDs, []string{request.TrialPlanID}) {
		return result, errors.New("task contract is not bound to its sealed trial plan")
	}
	if _, duplicate := records[request.WindowEndEventID]; duplicate {
		return result, errors.New("planned task result already exists")
	}

	manifestData, err := readAttemptBlob(store, *request.SystemUnderTestBlob)
	if err != nil {
		return result, err
	}
	var manifest SystemUnderTestManifest
	if decodeStrictEvaluationJSON(manifestData, &manifest) != nil ||
		validateSystemUnderTestManifest(store, manifest, request.Agent) != nil {
		return result, errors.New("planned execution system-under-test manifest is invalid")
	}
	systemPrompt, err := readAttemptBlob(store, manifest.SystemPromptBlob)
	if err != nil {
		return result, err
	}
	toolRegistry, err := readAttemptBlob(store, manifest.ToolRegistryBlob)
	if err != nil {
		return result, err
	}
	harnessData, err := readAttemptBlob(store, manifest.HarnessBlob)
	if err != nil {
		return result, err
	}
	adapterData, err := readAttemptBlob(store, manifest.AdapterBlob)
	if err != nil {
		return result, err
	}
	var harness ExecutionHarnessManifest
	if decodeStrictEvaluationJSON(harnessData, &harness) != nil || validateExecutionHarness(harness) != nil {
		return result, errors.New("planned execution harness manifest is invalid")
	}
	taskSpec, err := readAttemptBlob(store, *request.TaskSpecBlob)
	if err != nil {
		return result, err
	}
	config, err := readAttemptBlob(store, *request.ExecutionConfigBlob)
	if err != nil {
		return result, err
	}
	if !utf8.Valid(taskSpec) {
		return result, errors.New("planned task spec must be UTF-8 for retrieval")
	}

	contextValue := retrieval.Context{Agent: request.Agent, ThreadID: contractRecord.Record.Event.Source.ThreadID,
		SessionID: contractRecord.Record.Event.Source.SessionID, Channel: retrieval.ChannelHarness}
	memoryContext := retrieval.ContextResult{Memories: []retrieval.MemoryReference{}}
	parentIDs := []string{request.WindowStartEventID}
	if request.Condition == TaskConditionMemory {
		memoryContext, err = retrieval.BuildContext(store, options.PortableRoot, retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion, Query: string(taskSpec), Context: contextValue,
			Limit: harness.RetrievalLimit, TokenBudget: harness.RetrievalTokenBudget,
			ByteBudget: harness.RetrievalByteBudget})
		if err != nil || memoryContext.InjectionID == "" || len(memoryContext.Memories) == 0 {
			return result, errors.New("planned memory execution did not produce one injected memory context")
		}
		parentIDs = append(parentIDs, memoryContext.InjectionID)
	}
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		return result, err
	}
	challenge := hex.EncodeToString(challengeBytes)
	input := AgentExecutionInput{SchemaVersion: AgentExecutionInputSchema, Challenge: challenge,
		Agent: request.Agent, Provider: manifest.Provider, Model: manifest.Model,
		SystemPrompt: systemPrompt, ToolRegistry: toolRegistry, ExecutionConfig: config, TaskSpec: taskSpec,
		MemoryContext:    memoryContext.Content,
		MemoryReferences: append([]retrieval.MemoryReference{}, memoryContext.Memories...), Privacy: "local_only"}
	inputData, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	inputBlob, err := store.PutBlob(bytes.NewReader(inputData))
	if err != nil {
		return result, err
	}
	argumentData, _ := json.Marshal(harness.Arguments)
	environment := []string{}
	if harness.InheritEnvironment {
		environment = os.Environ()
	}
	sort.Strings(environment)
	environmentSHA := sha256Hex([]byte(strings.Join(environment, "\x00")))
	startedAt := options.Now().UTC()
	executionID := adapterjsonl.DeterministicID("task-execution", request.TrialPlanID,
		request.TrialPairID, request.AttemptID, sha256Hex(challengeBytes))
	started := TaskExecutionStarted{SchemaVersion: TaskExecutionStartedSchema, ExecutionID: executionID,
		TrialPlanID: request.TrialPlanID, TrialPairID: request.TrialPairID, AttemptID: request.AttemptID,
		Condition: request.Condition, ChallengeSHA256: sha256Hex(challengeBytes),
		Contract: boundReference(contractRecord.Record), SystemUnderTestSHA256: request.SystemUnderTestSHA256,
		SupervisorArtifactSHA256: request.SystemArtifactSHA256, ExecutableSHA256: manifest.AdapterSHA256,
		ArgumentsSHA256: sha256Hex(argumentData), EnvironmentSHA256: environmentSHA,
		InputBlob: inputBlob, StartedAt: startedAt, Privacy: "local_only"}
	startedData, _ := json.Marshal(started)
	startedPayload := ledger.InlinePayload("utf-8", "application/json", string(startedData))
	startedEvent := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: executionID,
		Kind: ledger.KindTaskExecutionStarted, ObservedAt: startedAt, RecordedAt: startedAt,
		Source: executionSource(store, contractRecord.Record.Event.Source, request.AttemptID), Payload: &startedPayload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: sortedUniqueStrings(parentIDs)},
		Privacy:      ledger.Privacy{Classification: "local_only"}}
	tempRoot := filepath.Join(store.Root(), "evidence", "tmp")
	executionDir, err := os.MkdirTemp(tempRoot, ".task-execution-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(executionDir)
	suffix := harness.ExecutableSuffix
	if runtime.GOOS == "windows" && suffix == "" {
		suffix = ".exe"
	}
	executablePath := filepath.Join(executionDir, "adapter"+suffix)
	if err := os.WriteFile(executablePath, adapterData, 0o700); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(harness.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executablePath, harness.Arguments...)
	command.Stdin = bytes.NewReader(inputData)
	command.Env = environment
	command.Dir = harness.WorkingDirectory
	if command.Dir == "" {
		command.Dir = executionDir
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	startedRecord, err := store.Append(startedEvent)
	if err != nil {
		return result, err
	}
	runErr := command.Run()
	finishedAt := options.Now().UTC()
	var stdoutBlob, stderrBlob *ledger.BlobRef
	if stdout.Len() != 0 {
		stored, putErr := store.PutBlob(bytes.NewReader(stdout.Bytes()))
		if putErr != nil {
			return result, putErr
		}
		stdoutBlob = &stored
	}
	if stderr.Len() != 0 {
		stored, putErr := store.PutBlob(bytes.NewReader(stderr.Bytes()))
		if putErr != nil {
			return result, putErr
		}
		stderrBlob = &stored
	}
	exitCode := 0
	failureKind, failureMessage := "", ""
	if ctx.Err() != nil {
		exitCode, failureKind, failureMessage = 124, "timeout", ctx.Err().Error()
	} else if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			exitCode, failureKind, failureMessage = 126, "start_error", runErr.Error()
		} else {
			exitCode = exitErr.ExitCode()
			failureKind, failureMessage = "process_exit", runErr.Error()
		}
	}
	if stdout.Len() == 0 && failureKind == "" {
		exitCode, failureKind, failureMessage = 125, "empty_output", "planned execution produced no result bytes"
	}
	resultData, resultMediaType := stdout.Bytes(), harness.ResultMediaType
	outcome := TaskExecutionCompleted
	if failureKind != "" {
		outcome = TaskExecutionFailed
		failureData, marshalErr := json.Marshal(TaskExecutionFailure{SchemaVersion: TaskExecutionFailureSchema,
			Kind: failureKind, Message: failureMessage, StdoutBlob: stdoutBlob, StderrBlob: stderrBlob})
		if marshalErr != nil {
			return result, marshalErr
		}
		resultData, resultMediaType = failureData, TaskExecutionFailureMediaType
	}
	resultBlob, err := store.PutBlob(bytes.NewReader(resultData))
	if err != nil {
		return result, err
	}
	resultPayload := ledger.Payload{Encoding: "binary", MediaType: resultMediaType,
		Blob: &resultBlob, SHA256: resultBlob.SHA256, Bytes: resultBlob.Bytes}
	resultEvent := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: request.WindowEndEventID,
		Kind: ledger.KindToolResult, ObservedAt: finishedAt, RecordedAt: finishedAt,
		Source: executionSource(store, contractRecord.Record.Event.Source, request.AttemptID), Payload: &resultPayload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: []string{executionID}},
		Privacy:      ledger.Privacy{Classification: "local_only"}}
	resultRecord, err := store.Append(resultEvent)
	if err != nil {
		return result, err
	}
	receipt := TaskExecutionReceipt{SchemaVersion: TaskExecutionReceiptSchema, ExecutionID: executionID,
		TrialPlanID: request.TrialPlanID, TrialPairID: request.TrialPairID, AttemptID: request.AttemptID,
		Condition: request.Condition, Agent: request.Agent, Provider: manifest.Provider, Model: manifest.Model,
		Contract: boundReference(contractRecord.Record), Started: boundReference(startedRecord),
		Result: boundReference(resultRecord), SystemUnderTestSHA256: request.SystemUnderTestSHA256,
		SupervisorArtifactSHA256: request.SystemArtifactSHA256, ExecutableSHA256: manifest.AdapterSHA256,
		ArgumentsSHA256: sha256Hex(argumentData), EnvironmentSHA256: environmentSHA, InputBlob: inputBlob,
		ResultBlob: resultBlob, StderrBlob: stderrBlob, Outcome: outcome, ResultMediaType: resultMediaType,
		FailureKind: failureKind, ExitCode: exitCode, StartedAt: startedAt,
		FinishedAt: finishedAt, RetrievalReceiptID: memoryContext.Retrieval.ReceiptID,
		InjectionID: memoryContext.InjectionID, MemoryReferences: append([]retrieval.MemoryReference{}, memoryContext.Memories...),
		Privacy: "local_only"}
	if outcome == TaskExecutionFailed {
		receipt.StdoutBlob = stdoutBlob
	}
	receiptDataWithoutID, _ := json.Marshal(receipt)
	receipt.ReceiptID = adapterjsonl.DeterministicID("task-execution-receipt", request.AttemptID,
		startedRecord.RecordHash, resultRecord.RecordHash, sha256Hex(receiptDataWithoutID))
	receiptData, _ := json.Marshal(receipt)
	receiptPayload := ledger.InlinePayload("utf-8", "application/json", string(receiptData))
	receiptEvent := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: receipt.ReceiptID,
		Kind: ledger.KindTaskExecutionReceipt, ObservedAt: finishedAt, RecordedAt: finishedAt,
		Source: executionSource(store, contractRecord.Record.Event.Source, request.AttemptID), Payload: &receiptPayload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: []string{executionID, request.WindowEndEventID}},
		Privacy:      ledger.Privacy{Classification: "local_only"}}
	if _, err := store.Append(receiptEvent); err != nil {
		return result, err
	}
	request.RetrievalReceiptID, request.InjectionID = memoryContext.Retrieval.ReceiptID, memoryContext.InjectionID
	request.MemoryReferences = append([]retrieval.MemoryReference{}, memoryContext.Memories...)
	if request.Condition == TaskConditionMemory {
		items := make([]retrieval.AdoptionItem, 0, len(request.MemoryReferences))
		for _, memory := range request.MemoryReferences {
			items = append(items, retrieval.AdoptionItem{MemoryReference: memory, Adoption: retrieval.AdoptionUnknown,
				Outcome: retrieval.OutcomeUnknown, Reason: "The exact memory context was delivered; downstream use was not independently observed."})
		}
		adoption, adoptionErr := retrieval.RecordAdoption(store, contextValue, retrieval.AdoptionRequest{
			SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
			Reporter:           retrieval.Reporter{Kind: "harness", ID: executionSupervisorAdapter},
			RetrievalReceiptID: request.RetrievalReceiptID, InjectionID: request.InjectionID,
			Items: items, OutcomeEvidenceEventIDs: []string{request.WindowEndEventID}})
		if adoptionErr != nil {
			return result, adoptionErr
		}
		request.AdoptionID = adoption.AdoptionID
	}
	result.Request, result.Execution = request, receipt
	return result, nil
}

func DecodeTaskExecutionResult(store *ledger.Store, reader io.Reader) (TaskExecutionResult, error) {
	result := TaskExecutionResult{SchemaVersion: TaskExecutionResultSchema, Privacy: "local_only"}
	if store == nil || reader == nil {
		return result, errors.New("store and task execution result reader are required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode task execution result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return result, err
	}
	if err := ValidateTaskExecutionResult(store, result); err != nil {
		return result, err
	}
	return result, nil
}

func ValidateTaskExecutionResult(store *ledger.Store, result TaskExecutionResult) error {
	if store == nil || result.SchemaVersion != TaskExecutionResultSchema || result.Privacy != "local_only" ||
		validateTaskAttemptRequest(result.Request) != nil || validateAttemptBlobs(store, result.Request) != nil {
		return errors.New("task execution result envelope is invalid")
	}
	receipt := result.Execution
	if receipt.SchemaVersion != TaskExecutionReceiptSchema || receipt.Privacy != "local_only" ||
		receipt.TrialPlanID != result.Request.TrialPlanID || receipt.TrialPairID != result.Request.TrialPairID ||
		receipt.AttemptID != result.Request.AttemptID || receipt.Condition != result.Request.Condition ||
		receipt.Agent != result.Request.Agent || receipt.SystemUnderTestSHA256 != result.Request.SystemUnderTestSHA256 ||
		receipt.SupervisorArtifactSHA256 != result.Request.SystemArtifactSHA256 ||
		receipt.Contract.EventID != result.Request.WindowStartEventID ||
		receipt.Result.EventID != result.Request.WindowEndEventID ||
		receipt.RetrievalReceiptID != result.Request.RetrievalReceiptID || receipt.InjectionID != result.Request.InjectionID ||
		!sameMemoryReferences(receipt.MemoryReferences, result.Request.MemoryReferences) {
		return errors.New("task execution result request and receipt differ")
	}
	records, _, err := loadIndexedRecords(store)
	if err != nil {
		return err
	}
	contract, contractOK := records[result.Request.WindowStartEventID]
	receiptRecord, receiptOK := records[receipt.ReceiptID]
	if !contractOK || !receiptOK || receiptRecord.Record.Event.Kind != ledger.KindTaskExecutionReceipt ||
		!reflect.DeepEqual(receipt.Contract, boundReference(contract.Record)) {
		return errors.New("task execution result ledger bindings are unavailable")
	}
	contractData, err := eventPayload(store, contract.Record.Event)
	var declared TaskAttemptContract
	if err != nil || decodeStrictEvaluationJSON(contractData, &declared) != nil ||
		!taskAttemptContractMatches(declared, result.Request) {
		return errors.New("task execution result differs from its sealed contract")
	}
	receiptData, err := eventPayload(store, receiptRecord.Record.Event)
	var recorded TaskExecutionReceipt
	if err != nil || decodeStrictEvaluationJSON(receiptData, &recorded) != nil || !reflect.DeepEqual(recorded, receipt) {
		return errors.New("task execution result receipt differs from the evidence ledger")
	}
	attempt := TaskAttemptReceipt{AttemptID: result.Request.AttemptID,
		TrialPlanID: result.Request.TrialPlanID, TrialPairID: result.Request.TrialPairID,
		Agent: result.Request.Agent, Condition: result.Request.Condition,
		SystemArtifactSHA256:   result.Request.SystemArtifactSHA256,
		SystemUnderTestSHA256:  result.Request.SystemUnderTestSHA256,
		SystemUnderTestBlob:    result.Request.SystemUnderTestBlob,
		TaskSpecBlob:           result.Request.TaskSpecBlob,
		AcceptanceCriteriaBlob: result.Request.AcceptanceCriteriaBlob,
		ExecutionConfigBlob:    result.Request.ExecutionConfigBlob,
		WindowStart:            receipt.Contract, WindowEnd: receipt.Result,
		RetrievalReceiptID: result.Request.RetrievalReceiptID,
		InjectionID:        result.Request.InjectionID,
		MemoryReferences:   result.Request.MemoryReferences}
	return validatePopulationExecutionReceipt(store, records, receiptRecord.Record, receipt, attempt, false, false)
}

func loadExecutionTrialPair(store *ledger.Store, records map[string]indexedRecord,
	request TaskAttemptRequest) (TrialPlanPair, error) {
	indexed, exists := records[request.TrialPlanID]
	if !exists || indexed.Record.Event.Kind != ledger.KindEvaluationTrialPlan {
		return TrialPlanPair{}, errors.New("sealed evaluation trial plan is unavailable")
	}
	data, err := eventPayload(store, indexed.Record.Event)
	var plan EvaluationTrialPlan
	if err != nil || decodeStrictEvaluationJSON(data, &plan) != nil ||
		validateEvaluationTrialPlan(plan) != nil || plan.PlanID != request.TrialPlanID {
		return TrialPlanPair{}, errors.New("sealed evaluation trial plan is invalid")
	}
	for _, pair := range plan.Pairs {
		if pair.PairID == request.TrialPairID {
			return pair, nil
		}
	}
	return TrialPlanPair{}, errors.New("task attempt is outside its sealed trial plan")
}

func enforceExecutionOrder(store *ledger.Store, records map[string]indexedRecord, pair TrialPlanPair,
	request TaskAttemptRequest) error {
	current := pair.Baseline
	if request.Condition == TaskConditionMemory {
		current = pair.Treatment
	}
	if !taskAttemptContractMatches(current.Contract, request) {
		return errors.New("task attempt differs from its sealed trial arm")
	}
	if countExecutionStarts(store, records, request.AttemptID) != 0 {
		return errors.New("planned task arm already has an execution start and cannot be retried")
	}
	position := -1
	for index, condition := range pair.ExecutionOrder {
		if condition == request.Condition {
			position = index
			break
		}
	}
	if position < 0 {
		return errors.New("task condition is absent from the sealed execution order")
	}
	if position == 0 {
		return nil
	}
	previous := pair.Baseline
	if pair.ExecutionOrder[position-1] == TaskConditionMemory {
		previous = pair.Treatment
	}
	result, resultExists := records[previous.Contract.WindowEndEventID]
	if !resultExists || result.Record.Event.Kind != ledger.KindToolResult ||
		!hasExecutionReceipt(store, records, previous.Contract.AttemptID) {
		return errors.New("the preceding sealed trial arm has not completed supervised execution")
	}
	return nil
}

func countExecutionStarts(store *ledger.Store, records map[string]indexedRecord, attemptID string) int {
	count := 0
	for _, indexed := range records {
		if indexed.Record.Event.Kind != ledger.KindTaskExecutionStarted {
			continue
		}
		data, err := eventPayload(store, indexed.Record.Event)
		var started TaskExecutionStarted
		if err == nil && decodeStrictEvaluationJSON(data, &started) == nil && started.AttemptID == attemptID {
			count++
		}
	}
	return count
}

func hasExecutionReceipt(store *ledger.Store, records map[string]indexedRecord, attemptID string) bool {
	for _, indexed := range records {
		if indexed.Record.Event.Kind != ledger.KindTaskExecutionReceipt {
			continue
		}
		data, err := eventPayload(store, indexed.Record.Event)
		var receipt TaskExecutionReceipt
		if err == nil && decodeStrictEvaluationJSON(data, &receipt) == nil && receipt.AttemptID == attemptID {
			return true
		}
	}
	return false
}

func validateExecutionHarness(harness ExecutionHarnessManifest) error {
	if harness.SchemaVersion != ExecutionHarnessManifestSchema || harness.Arguments == nil ||
		harness.TimeoutSeconds < 1 || harness.TimeoutSeconds > 3600 || harness.RetrievalLimit < 1 ||
		harness.RetrievalTokenBudget < 1 || harness.RetrievalByteBudget < 1 ||
		strings.TrimSpace(harness.ResultMediaType) == "" || harness.Privacy != "local_only" ||
		(harness.ExecutableSuffix != "" && harness.ExecutableSuffix != ".exe") {
		return errors.New("execution harness manifest is invalid")
	}
	for _, argument := range harness.Arguments {
		if strings.ContainsRune(argument, 0) {
			return errors.New("execution harness argument contains NUL")
		}
	}
	if harness.WorkingDirectory != "" {
		info, err := os.Stat(harness.WorkingDirectory)
		if err != nil || !info.IsDir() {
			return errors.New("execution harness working directory is unavailable")
		}
	}
	return nil
}

func executionSource(store *ledger.Store, contract ledger.Source, attemptID string) ledger.Source {
	return ledger.Source{Agent: contract.Agent, Adapter: executionSupervisorAdapter,
		AdapterVersion: executionSupervisorVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
		ThreadID: contract.ThreadID, SessionID: contract.SessionID, SourceEventID: attemptID,
		SourceCursor: "task-execution:" + attemptID}
}

func validatePopulationExecutionReceipts(store *ledger.Store, records map[string]indexedRecord,
	ordered []ledger.Record, attempts []TaskAttemptReceipt, population *EvaluationPopulation) bool {
	expected := map[string]TaskAttemptReceipt{}
	for _, attempt := range attempts {
		expected[attempt.AttemptID] = attempt
	}
	validated := map[string]struct{}{}
	issuesBefore := len(population.PopulationIssues)
	planned := map[string]struct{}{}
	starts := map[string]int{}
	for _, record := range ordered {
		if record.Event.Kind == ledger.KindTaskAttemptContract {
			data, err := eventPayload(store, record.Event)
			var contract TaskAttemptContract
			if err == nil && decodeStrictEvaluationJSON(data, &contract) == nil &&
				contract.TrialPlanID != "" && contract.TrialPairID != "" {
				planned[contract.AttemptID] = struct{}{}
			}
		}
	}
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindTaskExecutionStarted {
			continue
		}
		data, err := eventPayload(store, record.Event)
		var started TaskExecutionStarted
		if err != nil || decodeStrictEvaluationJSON(data, &started) != nil ||
			started.SchemaVersion != TaskExecutionStartedSchema || started.Privacy != "local_only" {
			population.PopulationIssues = append(population.PopulationIssues,
				"task execution start is invalid: "+record.Event.EventID)
			continue
		}
		if _, exists := planned[started.AttemptID]; exists {
			starts[started.AttemptID]++
		}
	}
	for attemptID := range planned {
		if starts[attemptID] != 1 {
			population.PopulationIssues = append(population.PopulationIssues,
				fmt.Sprintf("planned task attempt has %d execution starts: %s", starts[attemptID], attemptID))
		}
	}
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindTaskExecutionReceipt {
			continue
		}
		data, err := eventPayload(store, record.Event)
		var receipt TaskExecutionReceipt
		if err != nil || decodeStrictEvaluationJSON(data, &receipt) != nil ||
			receipt.SchemaVersion != TaskExecutionReceiptSchema || receipt.ReceiptID != record.Event.EventID ||
			receipt.Privacy != "local_only" {
			population.PopulationIssues = append(population.PopulationIssues,
				"task execution receipt is invalid: "+record.Event.EventID)
			continue
		}
		attempt, exists := expected[receipt.AttemptID]
		if !exists {
			continue
		}
		if _, duplicate := validated[receipt.AttemptID]; duplicate {
			population.PopulationIssues = append(population.PopulationIssues,
				"task attempt has multiple execution receipts: "+receipt.AttemptID)
			continue
		}
		if err := validatePopulationExecutionReceipt(store, records, record, receipt, attempt, true, true); err != nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"task execution receipt failed replay: "+receipt.AttemptID+": "+err.Error())
			continue
		}
		validated[receipt.AttemptID] = struct{}{}
	}
	for attemptID := range expected {
		if _, exists := validated[attemptID]; !exists {
			population.PopulationIssues = append(population.PopulationIssues,
				"planned task attempt has no verified execution receipt: "+attemptID)
		}
	}
	return len(expected) != 0 && len(validated) == len(expected) &&
		len(population.PopulationIssues) == issuesBefore
}

func validatePopulationExecutionReceipt(store *ledger.Store, records map[string]indexedRecord,
	receiptRecord ledger.Record, receipt TaskExecutionReceipt, attempt TaskAttemptReceipt,
	requireCompleted, requireVerdict bool) error {
	if receipt.AttemptID != attempt.AttemptID || receipt.TrialPlanID != attempt.TrialPlanID || receipt.TrialPairID != attempt.TrialPairID ||
		receipt.Condition != attempt.Condition || receipt.Agent != attempt.Agent ||
		receipt.SystemUnderTestSHA256 != attempt.SystemUnderTestSHA256 ||
		receipt.SupervisorArtifactSHA256 != attempt.SystemArtifactSHA256 ||
		!validSHA256(receipt.ExecutableSHA256) || !validSHA256(receipt.ArgumentsSHA256) ||
		!validSHA256(receipt.EnvironmentSHA256) || receipt.StartedAt.IsZero() || receipt.FinishedAt.IsZero() ||
		receipt.FinishedAt.Before(receipt.StartedAt) || !validBlobRef(receipt.InputBlob) ||
		!validBlobRef(receipt.ResultBlob) || verifyBlob(store, receipt.InputBlob) != nil ||
		verifyBlob(store, receipt.ResultBlob) != nil ||
		(receipt.StderrBlob != nil && (!validBlobRef(*receipt.StderrBlob) || verifyBlob(store, *receipt.StderrBlob) != nil)) {
		return errors.New("execution receipt identity or blobs are invalid")
	}
	if err := validateExecutionOutcome(store, records, receipt, requireCompleted); err != nil {
		return err
	}
	contract, contractOK := records[attempt.WindowStart.EventID]
	started, startedOK := records[receipt.ExecutionID]
	result, resultOK := records[attempt.WindowEnd.EventID]
	receiptIndexed, receiptOK := records[receipt.ReceiptID]
	verdictOrderOK := true
	if requireVerdict {
		verdict, verdictOK := records[attempt.Verdict.EventID]
		verdictOrderOK = verdictOK && receiptIndexed.Index < verdict.Index
	}
	if !contractOK || !startedOK || !resultOK || !receiptOK || !verdictOrderOK ||
		contract.Index >= started.Index || started.Index >= result.Index || result.Index >= receiptIndexed.Index ||
		!reflect.DeepEqual(receiptIndexed.Record, receiptRecord) ||
		!reflect.DeepEqual(receipt.Contract, boundReference(contract.Record)) ||
		!reflect.DeepEqual(receipt.Started, boundReference(started.Record)) ||
		!reflect.DeepEqual(receipt.Result, boundReference(result.Record)) ||
		!reflect.DeepEqual(attempt.WindowEnd, boundReference(result.Record)) {
		return errors.New("execution receipt event order or bound references are invalid")
	}
	if started.Record.Event.Kind != ledger.KindTaskExecutionStarted ||
		result.Record.Event.Kind != ledger.KindToolResult ||
		started.Record.Event.Source != executionSource(store, contract.Record.Event.Source, attempt.AttemptID) ||
		result.Record.Event.Source != executionSource(store, contract.Record.Event.Source, attempt.AttemptID) ||
		receiptRecord.Event.Source != executionSource(store, contract.Record.Event.Source, attempt.AttemptID) {
		return errors.New("execution supervisor event source is invalid")
	}
	expectedStartParents := []string{attempt.WindowStart.EventID}
	if attempt.Condition == TaskConditionMemory {
		expectedStartParents = append(expectedStartParents, receipt.InjectionID)
	}
	if started.Record.Event.Causality == nil ||
		!sameStrings(started.Record.Event.Causality.ParentEventIDs, sortedUniqueStrings(expectedStartParents)) ||
		result.Record.Event.Causality == nil ||
		!sameStrings(result.Record.Event.Causality.ParentEventIDs, []string{receipt.ExecutionID}) ||
		receiptRecord.Event.Causality == nil ||
		!sameStrings(receiptRecord.Event.Causality.ParentEventIDs,
			[]string{receipt.ExecutionID, attempt.WindowEnd.EventID}) {
		return errors.New("execution supervisor causality is invalid")
	}
	startedData, err := eventPayload(store, started.Record.Event)
	var declaration TaskExecutionStarted
	if err != nil || decodeStrictEvaluationJSON(startedData, &declaration) != nil ||
		declaration.SchemaVersion != TaskExecutionStartedSchema || declaration.Privacy != "local_only" ||
		!validSHA256(declaration.ChallengeSHA256) || declaration.ExecutionID != receipt.ExecutionID ||
		declaration.TrialPlanID != receipt.TrialPlanID ||
		declaration.TrialPairID != receipt.TrialPairID || declaration.AttemptID != receipt.AttemptID ||
		declaration.Condition != receipt.Condition || !reflect.DeepEqual(declaration.Contract, receipt.Contract) ||
		declaration.SystemUnderTestSHA256 != receipt.SystemUnderTestSHA256 ||
		declaration.SupervisorArtifactSHA256 != receipt.SupervisorArtifactSHA256 ||
		declaration.ExecutableSHA256 != receipt.ExecutableSHA256 ||
		declaration.ArgumentsSHA256 != receipt.ArgumentsSHA256 ||
		declaration.EnvironmentSHA256 != receipt.EnvironmentSHA256 ||
		!reflect.DeepEqual(declaration.InputBlob, receipt.InputBlob) || declaration.StartedAt != receipt.StartedAt {
		return errors.New("execution start declaration differs from its receipt")
	}
	copy := receipt
	copy.ReceiptID = ""
	identityData, _ := json.Marshal(copy)
	expectedID := adapterjsonl.DeterministicID("task-execution-receipt", receipt.AttemptID,
		started.Record.RecordHash, result.Record.RecordHash, sha256Hex(identityData))
	if expectedID != receipt.ReceiptID {
		return errors.New("execution receipt id does not match its complete contents")
	}
	if result.Record.Event.Payload == nil || result.Record.Event.Payload.Blob == nil ||
		!reflect.DeepEqual(*result.Record.Event.Payload.Blob, receipt.ResultBlob) {
		return errors.New("execution result blob differs from the observed task result")
	}
	manifestData, err := readAttemptBlob(store, *attempt.SystemUnderTestBlob)
	if err != nil {
		return err
	}
	var manifest SystemUnderTestManifest
	if decodeStrictEvaluationJSON(manifestData, &manifest) != nil ||
		validateSystemUnderTestManifest(store, manifest, attempt.Agent) != nil ||
		manifest.Provider != receipt.Provider || manifest.Model != receipt.Model ||
		manifest.AdapterSHA256 != receipt.ExecutableSHA256 {
		return errors.New("execution receipt differs from its system-under-test manifest")
	}
	harnessData, err := readAttemptBlob(store, manifest.HarnessBlob)
	if err != nil {
		return err
	}
	var harness ExecutionHarnessManifest
	if decodeStrictEvaluationJSON(harnessData, &harness) != nil || validateExecutionHarness(harness) != nil {
		return errors.New("execution receipt harness manifest is invalid")
	}
	argumentData, err := json.Marshal(harness.Arguments)
	if err != nil || sha256Hex(argumentData) != receipt.ArgumentsSHA256 {
		return errors.New("execution receipt argument hash differs from its harness manifest")
	}
	inputData, err := readAttemptBlob(store, receipt.InputBlob)
	if err != nil {
		return err
	}
	var input AgentExecutionInput
	if decodeStrictEvaluationJSON(inputData, &input) != nil || input.SchemaVersion != AgentExecutionInputSchema ||
		input.Agent != attempt.Agent || input.Provider != manifest.Provider || input.Model != manifest.Model || input.Privacy != "local_only" ||
		!sameMemoryReferences(input.MemoryReferences, receipt.MemoryReferences) {
		return errors.New("execution input envelope is invalid")
	}
	for _, item := range []struct {
		observed []byte
		blob     ledger.BlobRef
	}{
		{input.SystemPrompt, manifest.SystemPromptBlob}, {input.ToolRegistry, manifest.ToolRegistryBlob},
		{input.ExecutionConfig, *attempt.ExecutionConfigBlob}, {input.TaskSpec, *attempt.TaskSpecBlob},
	} {
		expectedBytes, readErr := readAttemptBlob(store, item.blob)
		challengeBytes, challengeErr := hex.DecodeString(input.Challenge)
		if challengeErr != nil || len(challengeBytes) != 32 ||
			sha256Hex(challengeBytes) != declaration.ChallengeSHA256 {
			return errors.New("execution input challenge differs from the start declaration")
		}

		if readErr != nil || !bytes.Equal(item.observed, expectedBytes) {
			return errors.New("execution input does not contain the exact bound artifacts")
		}
	}
	if attempt.Condition == TaskConditionBaseline {
		if receipt.RetrievalReceiptID != "" || receipt.InjectionID != "" || len(receipt.MemoryReferences) != 0 ||
			input.MemoryContext != "" {
			return errors.New("baseline execution contains memory exposure")
		}
	} else {
		if receipt.RetrievalReceiptID != attempt.RetrievalReceiptID ||
			receipt.InjectionID != attempt.InjectionID ||
			!sameMemoryReferences(receipt.MemoryReferences, attempt.MemoryReferences) {
			return errors.New("memory execution differs from its exact retrieval and injection")
		}
		retrievalRecord, retrievalOK := records[receipt.RetrievalReceiptID]
		injectionRecord, injectionOK := records[receipt.InjectionID]
		if !retrievalOK || !injectionOK || retrievalRecord.Record.Event.Kind != ledger.KindRetrieval ||
			injectionRecord.Record.Event.Kind != ledger.KindInjection ||
			retrievalRecord.Index >= injectionRecord.Index || injectionRecord.Index >= started.Index ||
			!sameTaskContext(contract.Record.Event, retrievalRecord.Record.Event, attempt.Agent) ||
			!sameTaskContext(contract.Record.Event, injectionRecord.Record.Event, attempt.Agent) ||
			injectionRecord.Record.Event.Causality == nil ||
			!sameStrings(injectionRecord.Record.Event.Causality.ParentEventIDs, []string{receipt.RetrievalReceiptID}) {
			return errors.New("memory execution retrieval and injection chain is invalid")
		}
		retrievalData, retrievalErr := eventPayload(store, retrievalRecord.Record.Event)
		var retrievalReceipt retrieval.Receipt
		if retrievalErr != nil || decodeStrictEvaluationJSON(retrievalData, &retrievalReceipt) != nil ||
			retrievalReceipt.SchemaVersion != retrieval.ReceiptSchemaVersion ||
			retrievalReceipt.ReceiptID != receipt.RetrievalReceiptID || retrievalReceipt.Result.Status != "completed" {
			return errors.New("memory execution retrieval receipt is invalid")
		}
		injectionData, injectionErr := eventPayload(store, injectionRecord.Record.Event)
		var injection retrieval.InjectionReceipt
		if injectionErr != nil || decodeStrictEvaluationJSON(injectionData, &injection) != nil ||
			injection.SchemaVersion != retrieval.InjectionReceiptSchemaVersion ||
			injection.InjectionID != receipt.InjectionID ||
			injection.RetrievalReceiptID != receipt.RetrievalReceiptID || injection.Privacy != retrieval.PrivacyLocalOnly ||
			injection.Content == "" || injection.Content != input.MemoryContext ||
			injection.ContentSHA256 != sha256Hex([]byte(injection.Content)) ||
			injection.ContentBytes != len([]byte(injection.Content)) ||
			!injection.RecordedAt.Equal(injectionRecord.Record.Event.RecordedAt) ||
			!injectionRecord.Record.Event.ObservedAt.Equal(injectionRecord.Record.Event.RecordedAt) ||
			!sameMemoryReferences(injection.Memories, receipt.MemoryReferences) {
			return errors.New("execution input memory context differs from its exact injection")
		}
	}
	return nil
}

func validateExecutionOutcome(store *ledger.Store, records map[string]indexedRecord,
	receipt TaskExecutionReceipt, requireCompleted bool) error {
	result, exists := records[receipt.Result.EventID]
	if !exists || result.Record.Event.Payload == nil || result.Record.Event.Payload.Blob == nil ||
		!reflect.DeepEqual(*result.Record.Event.Payload.Blob, receipt.ResultBlob) ||
		result.Record.Event.Payload.MediaType != receipt.ResultMediaType || strings.TrimSpace(receipt.ResultMediaType) == "" ||
		(receipt.StdoutBlob != nil && (!validBlobRef(*receipt.StdoutBlob) || verifyBlob(store, *receipt.StdoutBlob) != nil)) {
		return errors.New("execution outcome payload is invalid")
	}
	switch receipt.Outcome {
	case TaskExecutionCompleted:
		if receipt.ExitCode != 0 || receipt.FailureKind != "" || receipt.StdoutBlob != nil ||
			receipt.ResultMediaType == TaskExecutionFailureMediaType {
			return errors.New("completed execution has failure state")
		}
	case TaskExecutionFailed:
		if receipt.ExitCode == 0 || receipt.FailureKind == "" || receipt.ResultMediaType != TaskExecutionFailureMediaType {
			return errors.New("failed execution terminal is incomplete")
		}
		data, err := readAttemptBlob(store, receipt.ResultBlob)
		var failure TaskExecutionFailure
		if err != nil || decodeStrictEvaluationJSON(data, &failure) != nil ||
			failure.SchemaVersion != TaskExecutionFailureSchema || failure.Kind != receipt.FailureKind ||
			strings.TrimSpace(failure.Message) == "" || !reflect.DeepEqual(failure.StdoutBlob, receipt.StdoutBlob) ||
			!reflect.DeepEqual(failure.StderrBlob, receipt.StderrBlob) {
			return errors.New("failed execution terminal does not match its receipt")
		}
		if failure.StdoutBlob != nil && verifyBlob(store, *failure.StdoutBlob) != nil {
			return errors.New("failed execution stdout blob is unavailable")
		}
		if failure.StderrBlob != nil && verifyBlob(store, *failure.StderrBlob) != nil {
			return errors.New("failed execution stderr blob is unavailable")
		}
	default:
		return errors.New("execution outcome is invalid")
	}
	if requireCompleted && receipt.Outcome != TaskExecutionCompleted {
		return errors.New("failed execution is ineligible for the continuous population")
	}
	return nil
}
