package study

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestMain(m *testing.M) {
	if os.Getenv("STUDY_TEST_HELPER") == "1" {
		runStudyTestHelper()
		return
	}
	os.Exit(m.Run())
}

func runStudyTestHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-study-test 1.0")
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "exec" || os.Args[len(os.Args)-1] != "-" {
		fmt.Fprintln(os.Stderr, "unexpected Codex invocation")
		os.Exit(4)
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil || len(strings.TrimSpace(string(prompt))) == 0 {
		fmt.Fprintln(os.Stderr, "missing prompt")
		os.Exit(5)
	}
	if target := os.Getenv("STUDY_WORKSPACE_CAPTURE"); target != "" {
		data, err := os.ReadFile("task-input.txt")
		if err != nil || os.WriteFile(target, data, 0o600) != nil {
			os.Exit(6)
		}
	}
	fmt.Println(`{"type":"thread.started","thread_id":"study-thread"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"study answer"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":8,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":0}}`)
}

func TestPlanCounterbalancesAStablePopulationWithoutUsingCreationTimeAsSeed(t *testing.T) {
	first := newStudyFixture(t)
	secondStore, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	draft := studyDraft(first.Loadout.LoadoutID, first.Workspace, 7)
	firstResult, err := Create(first.Store, draft, CreateOptions{PortableRoot: first.Repository,
		Now: fixedStudyClock(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := Create(secondStore, draft, CreateOptions{PortableRoot: first.Repository,
		Now: fixedStudyClock(time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.Plan.StudyID == secondResult.Plan.StudyID ||
		firstResult.Plan.AssignmentSeedSHA256 != secondResult.Plan.AssignmentSeedSHA256 ||
		!reflect.DeepEqual(planConditions(firstResult.Plan), planConditions(secondResult.Plan)) {
		t.Fatalf("assignment changed with creation time: first=%+v second=%+v", firstResult.Plan, secondResult.Plan)
	}
	baseline, memory := 0, 0
	for _, task := range firstResult.Plan.Tasks {
		if task.Condition == ConditionBaseline {
			baseline++
		} else {
			memory++
		}
	}
	if baseline != 2 || memory != 2 {
		t.Fatalf("population is not exactly counterbalanced: baseline=%d memory=%d", baseline, memory)
	}
}

func TestObservationRejectsAnExecutionThatPredatesTheSealedPlan(t *testing.T) {
	fixture := newStudyFixture(t)
	draft := studyDraft(fixture.Loadout.LoadoutID, fixture.Workspace, 7)
	preview, err := buildPlan(fixture.Store, draft, fixture.Loadout, time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseline := taskWithCondition(t, preview, ConditionBaseline)
	receipt := runUnboundStudyTask(t, fixture, baseline, "", time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC))
	created, err := Create(fixture.Store, draft, CreateOptions{PortableRoot: fixture.Repository,
		Now: fixedStudyClock(time.Date(2026, 8, 3, 2, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Observe(fixture.Store, observationRequest(created.Plan.StudyID, baseline.TaskID,
		receipt.ReceiptID, "caller_attestation"),
		fixedStudyClock(time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC)))
	if err == nil || !strings.Contains(err.Error(), "one-shot reservation") {
		t.Fatalf("pre-plan execution was accepted: %v", err)
	}
}

func TestObservationRejectsBaselineForAMemoryAssignment(t *testing.T) {
	fixture := newStudyFixture(t)
	created := createStudyPlan(t, fixture, time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC), 7)
	memoryTask := taskWithCondition(t, created.Plan, ConditionMemory)
	receipt := runUnboundStudyTask(t, fixture, memoryTask, "", time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC))
	_, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, memoryTask.TaskID,
		receipt.ReceiptID, "caller_attestation"),
		fixedStudyClock(time.Date(2026, 8, 4, 2, 0, 0, 0, time.UTC)))
	if err == nil || !strings.Contains(err.Error(), "one-shot reservation") {
		t.Fatalf("memory assignment accepted an execution without memory: %v", err)
	}
}

func TestCompleteBoundPopulationProducesOnlyADescriptiveSignal(t *testing.T) {
	fixture := newStudyFixture(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	created := createStudyPlan(t, fixture, start, 3)
	for index, task := range created.Plan.Tasks {
		at := start.Add(time.Duration(index+1) * 24 * time.Hour)
		receipt := runStudyTask(t, fixture, task, at)
		result, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
			receipt.ReceiptID, "caller_attestation"),
			fixedStudyClock(at.Add(time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			_, duplicateErr := Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
				receipt.ReceiptID, "caller_attestation"),
				fixedStudyClock(at.Add(2*time.Hour)))
			if duplicateErr == nil || !strings.Contains(duplicateErr.Error(), "already has") {
				t.Fatalf("duplicate observation was accepted: %v", duplicateErr)
			}
		}
		if result.Observation.OutcomeAuthority != AuthorityBuiltinAcceptance || result.Outcome == nil {
			t.Fatalf("tool-bound outcome lost its authority boundary: %+v", result)
		}
	}
	report, err := BuildReport(fixture.Store, created.Plan.StudyID,
		fixedStudyClock(start.Add(10*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "descriptive_signal" || len(report.Reasons) != 0 ||
		report.ObservedTasks != 4 || report.BuiltinEvaluatorOutcomes != 4 || report.SyntheticObservations != 0 ||
		report.Baseline.Observed != 2 || report.Memory.Observed != 2 || report.ElapsedDays != 4 ||
		report.ClaimBoundary != ClaimBoundary {
		t.Fatalf("unexpected longitudinal report: %+v", report)
	}
	if verification := Verify(fixture.Store); len(verification.Issues) != 0 ||
		verification.StudiesChecked != 1 || verification.ObservationsChecked != 4 {
		t.Fatalf("study replay failed: %+v", verification)
	}
	loaderCalls := 0
	replayed := replayWithExecutionLoader(fixture.Store,
		func(store *ledger.Store) ([]agentbridge.VerifiedExecution, error) {
			loaderCalls++
			return agentbridge.ListVerifiedExecutions(store)
		})
	if loaderCalls != 1 || len(replayed.Issues) != 0 {
		t.Fatalf("study replay did not reuse one verified execution index: calls=%d issues=%v", loaderCalls, replayed.Issues)
	}
}

func TestCallerAndSyntheticObservationsRemainNotEvaluable(t *testing.T) {
	fixture := newStudyFixture(t)
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	created := createStudyPlan(t, fixture, start, 1)
	baseline := taskWithCondition(t, created.Plan, ConditionBaseline)
	receipt := runStudyTask(t, fixture, baseline, start.Add(24*time.Hour))
	if _, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, baseline.TaskID,
		receipt.ReceiptID, "synthetic_test"), fixedStudyClock(start.Add(48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	report, err := BuildReport(fixture.Store, created.Plan.StudyID, fixedStudyClock(start.Add(72*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "not_evaluable" || report.BuiltinEvaluatorOutcomes != 1 ||
		report.SyntheticObservations != 1 || !containsReason(report.Reasons, "synthetic observations") {
		t.Fatalf("self-reported synthetic observation became evaluable: %+v", report)
	}
}

func TestOnlyTheReservedStudyBoundAttemptCanBeObserved(t *testing.T) {
	fixture := newStudyFixture(t)
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	created := createStudyPlan(t, fixture, start, 1)
	task := taskWithCondition(t, created.Plan, ConditionBaseline)
	rewritten := task
	rewritten.Prompt = "Complete a different task under the same identifier."
	first := runUnboundStudyTask(t, fixture, rewritten, "", start.Add(time.Hour))
	_, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
		first.ReceiptID, "caller_attestation"), fixedStudyClock(start.Add(2*time.Hour)))
	if err == nil || !strings.Contains(err.Error(), "one-shot reservation") {
		t.Fatalf("rewritten task was accepted: %v", err)
	}
	second := runStudyTask(t, fixture, task, start.Add(3*time.Hour))
	if _, err = Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
		second.ReceiptID, "caller_attestation"), fixedStudyClock(start.Add(4*time.Hour))); err != nil {
		t.Fatalf("reserved study-bound execution was rejected: %v", err)
	}
	if _, err := RunTask(context.Background(), fixture.Store, created.Plan.StudyID, task.TaskID,
		RunTaskOptions{PortableRoot: fixture.Repository, CodexPath: fixture.Executable}); err == nil ||
		!strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second study-bound attempt was accepted: %v", err)
	}
}

func TestStudyRunUsesTheSealedWorkspaceSnapshotAfterTheSourceChanges(t *testing.T) {
	fixture := newStudyFixture(t)
	sealedPath := filepath.Join(fixture.Workspace, "task-input.txt")
	if err := os.WriteFile(sealedPath, []byte("sealed before assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	created := createStudyPlan(t, fixture, start, 1)
	task := taskWithCondition(t, created.Plan, ConditionBaseline)
	if err := os.WriteFile(sealedPath, []byte("changed after assignment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "executed-workspace.txt")
	t.Setenv("STUDY_WORKSPACE_CAPTURE", capture)
	result, err := RunTask(context.Background(), fixture.Store, created.Plan.StudyID, task.TaskID,
		RunTaskOptions{PortableRoot: fixture.Repository, CodexPath: fixture.Executable,
			Now: fixedStudyClock(start.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trial.Request.WorkingDirectory == fixture.Workspace {
		t.Fatal("study executed in the mutable source workspace")
	}
	materialized, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(materialized) != "sealed before assignment\n" {
		t.Fatalf("study did not execute the sealed workspace bytes: %q", materialized)
	}
	if _, err := os.Stat(result.Trial.Request.WorkingDirectory); !os.IsNotExist(err) {
		t.Fatalf("isolated study workspace was not removed after its terminal result: %v", err)
	}
}

func TestStudyRejectsAWorkspaceArchiveThatNoLongerMatchesItsContentAddress(t *testing.T) {
	fixture := newStudyFixture(t)
	created := createStudyPlan(t, fixture, time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC), 1)
	task := taskWithCondition(t, created.Plan, ConditionBaseline)
	archivePath := filepath.Join(fixture.Store.Root(), filepath.FromSlash(task.WorkspaceSnapshot.Archive.RelativePath))
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if report := Verify(fixture.Store); len(report.Issues) == 0 {
		t.Fatal("study replay accepted a replaced content-addressed workspace archive")
	}
	if _, err := RunTask(context.Background(), fixture.Store, created.Plan.StudyID, task.TaskID,
		RunTaskOptions{PortableRoot: fixture.Repository, CodexPath: fixture.Executable}); err == nil {
		t.Fatal("study execution accepted a replaced content-addressed workspace archive")
	}
}

func TestOutcomeHashesTheCompleteAgentMessageBeyondFourMiB(t *testing.T) {
	fixture := newStudyFixture(t)
	message := strings.Repeat("x", (4<<20)+17) + "complete-tail"
	reference, err := fixture.Store.PutBlob(strings.NewReader(message))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(message))
	expected := hex.EncodeToString(digest[:])
	plan := Plan{StudyID: "study-" + strings.Repeat("a", 64)}
	task := PlannedTask{TaskID: "task-large-message", Acceptance: AcceptanceContract{
		SchemaVersion: AcceptanceSchema, Mode: "all", Assertions: []AcceptanceAssertion{{
			Kind: "agent_message_sha256", ExpectedSHA256: expected,
		}},
	}}
	execution := agentbridge.VerifiedExecution{Receipt: agentbridge.Receipt{AgentMessageBlob: &reference}}
	evidence, err := evaluateOutcome(fixture.Store, plan, task, execution,
		BoundEvent{EventID: "native-agent-receipt-" + strings.Repeat("b", 64),
			RecordSHA256: strings.Repeat("c", 64)}, time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Outcome != OutcomeSuccess || len(evidence.AssertionResults) != 1 ||
		evidence.AssertionResults[0].ActualSHA256 != expected ||
		evidence.AssertionResults[0].CapturedBlob == nil ||
		!reflect.DeepEqual(*evidence.AssertionResults[0].CapturedBlob, reference) {
		t.Fatalf("complete Agent message was not evaluated exactly: %+v", evidence)
	}
}

func TestOutcomeIsDerivedFromTheSealedAcceptanceContract(t *testing.T) {
	fixture := newStudyFixture(t)
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	draft := studyDraft(fixture.Loadout.LoadoutID, fixture.Workspace, 1)
	badDigest := sha256.Sum256([]byte("a different expected answer"))
	for index := range draft.Tasks {
		draft.Tasks[index].Acceptance.Assertions[0].ExpectedSHA256 = hex.EncodeToString(badDigest[:])
	}
	created, err := Create(fixture.Store, draft, CreateOptions{PortableRoot: fixture.Repository,
		Now: fixedStudyClock(start)})
	if err != nil {
		t.Fatal(err)
	}
	task := taskWithCondition(t, created.Plan, ConditionBaseline)
	receipt := runStudyTask(t, fixture, task, start.Add(time.Hour))
	result, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
		receipt.ReceiptID, "caller_attestation"), fixedStudyClock(start.Add(2*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome == nil || result.Outcome.Outcome != OutcomeFailure ||
		result.Observation.Outcome != OutcomeFailure {
		t.Fatalf("caller could override the failed built-in acceptance result: %+v", result)
	}
}

func TestObservationRequestRejectsCallerSuppliedOutcome(t *testing.T) {
	data := fmt.Sprintf(`{"schema_version":%q,"study_id":%q,"task_id":"task-a",`+
		`"execution_receipt_id":%q,"outcome":"success","reporter":{"kind":"caller_attestation",`+
		`"id":"reviewer"},"reason":"This caller attempted to self-report the outcome.","privacy":"local_only"}`,
		ObserveRequestSchema, "study-"+strings.Repeat("a", 64),
		"native-agent-receipt-"+strings.Repeat("b", 64))
	_, err := DecodeObservationRequest(strings.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("caller-supplied outcome was accepted: %v", err)
	}
}

func TestObservationRecoversAnOutcomeRecordedBeforeAnInterruptedAppend(t *testing.T) {
	fixture := newStudyFixture(t)
	start := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	created := createStudyPlan(t, fixture, start, 1)
	task := taskWithCondition(t, created.Plan, ConditionBaseline)
	receipt := runStudyTask(t, fixture, task, start.Add(time.Hour))
	execution, err := agentbridge.ResolveVerifiedExecution(fixture.Store, receipt.ReceiptID)
	if err != nil {
		t.Fatal(err)
	}
	binding := recordBinding(t, fixture.Store, receipt.ReceiptID)
	evidence, err := evaluateOutcome(fixture.Store, created.Plan, task, execution,
		binding, start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	event, err := outcomeEvidenceEvent(fixture.Store, created.Plan.Agent, evidence)
	if err != nil {
		t.Fatal(err)
	}
	record, err := fixture.Store.Append(event)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Observe(fixture.Store, observationRequest(created.Plan.StudyID, task.TaskID,
		receipt.ReceiptID, "caller_attestation"), fixedStudyClock(start.Add(3*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome == nil || result.Outcome.EvidenceID != evidence.EvidenceID ||
		result.Observation.OutcomeEvidence.RecordSHA256 != record.RecordHash {
		t.Fatalf("interrupted outcome was not recovered exactly: %+v", result)
	}
	if verification := Verify(fixture.Store); len(verification.Issues) != 0 {
		t.Fatalf("recovered study does not replay: %+v", verification)
	}
}

type studyFixture struct {
	Store      *ledger.Store
	Repository string
	Workspace  string
	Executable string
	Loadout    portable.Loadout
}

func newStudyFixture(t *testing.T) studyFixture {
	t.Helper()
	t.Setenv("STUDY_TEST_HELPER", "1")
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(root, "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	revision := writeStudyRevision(t, repository, "Verify the exact evidence before reporting completion.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name: "Study memory", Agents: []ledger.Agent{ledger.AgentCodex},
		Scope: review.Scope{Kind: review.ScopeGlobal, Value: "*"}, MemoryIDs: []string{revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return studyFixture{Store: store, Repository: repository, Workspace: workspace,
		Executable: executable, Loadout: created.Loadout}
}

func studyDraft(loadoutID, workspace string, minimumDays int) Draft {
	answerDigest := sha256.Sum256([]byte("study answer"))
	acceptance := AcceptanceContract{SchemaVersion: AcceptanceSchema, Mode: "all",
		Assertions: []AcceptanceAssertion{{Kind: "agent_message_sha256",
			ExpectedSHA256: hex.EncodeToString(answerDigest[:])}}}
	task := func(id string) TaskDraft {
		return TaskDraft{TaskID: "task-" + id, ClusterID: "cluster-" + id,
			Prompt: "Complete the sealed study task.", Model: "default", Sandbox: "read-only",
			WorkingDirectory: workspace, TimeoutSeconds: 60, SkipGitRepositoryCheck: true,
			Acceptance: acceptance}
	}
	return Draft{SchemaVersion: DraftSchema, Name: "Prospective memory study",
		Hypothesis: "Reviewed memory may reduce repeated mistakes over a prospective task population.",
		Agent:      ledger.AgentCodex, LoadoutID: loadoutID, MinimumElapsedDays: minimumDays,
		Tasks: []TaskDraft{task("a"), task("b"), task("c"), task("d")}, Privacy: PrivacyLocalOnly}
}

func createStudyPlan(t *testing.T, fixture studyFixture, at time.Time, minimumDays int) CreateResult {
	t.Helper()
	result, err := Create(fixture.Store, studyDraft(fixture.Loadout.LoadoutID, fixture.Workspace, minimumDays),
		CreateOptions{PortableRoot: fixture.Repository, Now: fixedStudyClock(at)})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func taskWithCondition(t *testing.T, plan Plan, condition Condition) PlannedTask {
	t.Helper()
	for _, task := range plan.Tasks {
		if task.Condition == condition {
			return task
		}
	}
	t.Fatalf("plan has no %s task", condition)
	return PlannedTask{}
}

func runStudyTask(t *testing.T, fixture studyFixture, task PlannedTask, at time.Time) agentbridge.Receipt {
	t.Helper()
	state := replay(fixture.Store)
	studyID := ""
	for candidate, plan := range state.Plans {
		if _, found := plannedTask(plan, task.TaskID); found {
			studyID = candidate
			break
		}
	}
	if studyID == "" {
		t.Fatal("study task has no sealed plan")
	}
	result, err := RunTask(context.Background(), fixture.Store, studyID, task.TaskID,
		RunTaskOptions{PortableRoot: fixture.Repository, CodexPath: fixture.Executable,
			Now: fixedStudyClock(at)})
	if err != nil {
		t.Fatal(err)
	}
	return result.Execution.Receipt
}

func runUnboundStudyTask(t *testing.T, fixture studyFixture, task PlannedTask, contextID string, at time.Time) agentbridge.Receipt {
	t.Helper()
	request := agentbridge.RunRequest{SchemaVersion: agentbridge.RunRequestSchema, TaskID: task.TaskID,
		Prompt: task.Prompt, Model: task.Model, Sandbox: task.Sandbox,
		WorkingDirectory: task.WorkingDirectory, TimeoutSeconds: task.TimeoutSeconds,
		SkipGitRepositoryCheck:  task.SkipGitRepositoryCheck,
		LoadoutContextReceiptID: contextID, Privacy: agentbridge.PrivacyLocalOnly}
	options := agentbridge.Options{CodexPath: fixture.Executable, Now: fixedStudyClock(at)}
	if contextID != "" {
		options.PortableRoot = fixture.Repository
	}
	result, err := agentbridge.Run(context.Background(), fixture.Store, request, options)
	if err != nil {
		t.Fatal(err)
	}
	return result.Receipt
}

func buildStudyContext(t *testing.T, fixture studyFixture, taskID string) string {
	t.Helper()
	result, err := loadoutcontext.BuildContext(fixture.Store, fixture.Repository, fixture.Loadout.LoadoutID,
		retrieval.Context{Agent: ledger.AgentCodex, ThreadID: "study-" + taskID, Task: taskID,
			Channel: retrieval.ChannelHarness})
	if err != nil {
		t.Fatal(err)
	}
	return result.Receipt.ReceiptID
}

func observationRequest(studyID, taskID, receiptID, reporterKind string) ObservationRequest {
	return ObservationRequest{SchemaVersion: ObserveRequestSchema, StudyID: studyID, TaskID: taskID,
		ExecutionReceiptID: receiptID, Reporter: Reporter{Kind: reporterKind, ID: "study-reviewer"},
		Reason: "The task outcome was checked against its explicit completion criterion.", Privacy: PrivacyLocalOnly}
}

func recordBinding(t *testing.T, store *ledger.Store, eventID string) BoundEvent {
	t.Helper()
	var binding BoundEvent
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID == eventID {
			binding = BoundEvent{EventID: eventID, RecordSHA256: record.RecordHash}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.EventID == "" {
		t.Fatalf("event %s was not found", eventID)
	}
	return binding
}

func fixedStudyClock(at time.Time) func() time.Time { return func() time.Time { return at } }

func planConditions(plan Plan) []Condition {
	result := make([]Condition, len(plan.Tasks))
	for index, task := range plan.Tasks {
		result[index] = task.Condition
	}
	return result
}

func containsReason(reasons []string, fragment string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, fragment) {
			return true
		}
	}
	return false
}

func writeStudyRevision(t *testing.T, root, text string) portable.Revision {
	t.Helper()
	textDigest := sha256.Sum256([]byte(text))
	textSHA := hex.EncodeToString(textDigest[:])
	memoryEnvelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA, review.Scope{Kind: review.ScopeGlobal, Value: "*"}}
	memoryData, _ := json.Marshal(memoryEnvelope)
	memoryDigest := sha256.Sum256(memoryData)
	revision := portable.Revision{SchemaVersion: portable.RevisionSchemaVersion,
		MemoryID: "memory-" + hex.EncodeToString(memoryDigest[:]), Action: portable.ActionPromote,
		Status: portable.StatusActive, Kind: candidates.KindDirective, ScopeKind: review.ScopeGlobal,
		ScopeValue: "*", EvidenceBasis: []review.Basis{review.BasisExplicitRemember}, Text: text,
		TextSHA256: textSHA, RuleChangeAuthorization: "not_granted", Privacy: portable.PortablePrivacy}
	identity := revision
	identity.Text = ""
	identity.RevisionID = ""
	revisionData, _ := json.Marshal(identity)
	revisionDigest := sha256.Sum256(revisionData)
	revision.RevisionID = "portable-revision-" + hex.EncodeToString(revisionDigest[:])
	data, err := portable.RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash := strings.TrimPrefix(revision.MemoryID, "memory-")
	revisionHash := strings.TrimPrefix(revision.RevisionID, "portable-revision-")
	path := filepath.Join(root, "memories", memoryHash[:2], memoryHash, revisionHash+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestReservedWorkspaceIsOutsideEvidenceAndRemoved(t *testing.T) {
	fixture := newStudyFixture(t)
	workspace, cleanup, err := reserveIsolatedWorkspace(fixture.Store, fixture.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if !validIsolatedWorkspacePath(fixture.Store, workspace) || pathsOverlap(workspace, fixture.Store.Root()) {
		t.Fatalf("reserved workspace is not isolated from evidence: %q", workspace)
	}
	root := filepath.Dir(workspace)
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("reserved workspace root was not removed: %v", err)
	}
}
func TestReservedWorkspaceRejectsPortableGitWorktree(t *testing.T) {
	fixture := newStudyFixture(t)
	t.Setenv("TMPDIR", fixture.Repository)
	t.Setenv("TMP", fixture.Repository)
	t.Setenv("TEMP", fixture.Repository)
	workspace, cleanup, err := reserveIsolatedWorkspace(fixture.Store, fixture.Repository)
	if cleanup != nil {
		_ = cleanup()
	}
	if err == nil {
		t.Fatalf("portable Git worktree accepted as an isolated study workspace: %q", workspace)
	}
	if matches, globErr := filepath.Glob(filepath.Join(fixture.Repository, "agentmem-study-*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("rejected portable workspace left temporary data: matches=%v err=%v", matches, globErr)
	}
}
