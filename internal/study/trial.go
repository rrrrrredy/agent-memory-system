package study

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func RunTask(ctx context.Context, store *ledger.Store, studyID, taskID string, options RunTaskOptions) (RunTaskResult, error) {
	result := RunTaskResult{SchemaVersion: RunTaskResultSchema, Privacy: PrivacyLocalOnly}
	if ctx == nil || store == nil || !validPrefixedHash(studyID, "study-") || !safeID(taskID) ||
		strings.TrimSpace(options.PortableRoot) == "" || strings.TrimSpace(options.CodexPath) == "" {
		return result, errors.New("context, evidence store, study, task, portable repository, and Codex executable are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	state := replay(store)
	if len(state.Issues) != 0 {
		return result, fmt.Errorf("longitudinal study replay failed: %s", strings.Join(state.Issues, "; "))
	}
	plan, exists := state.Plans[studyID]
	if !exists {
		return result, errors.New("prospectively sealed longitudinal study is unavailable")
	}
	task, exists := plannedTask(plan, taskID)
	if !exists {
		return result, errors.New("task is not present in the sealed study population")
	}
	key := outcomeKey(studyID, taskID)
	if _, consumed := state.Trials[key]; consumed {
		return result, errors.New("longitudinal study task already consumed its one study-bound attempt")
	}
	current, report, err := portable.LoadCurrentLoadout(options.PortableRoot, plan.Loadout.LoadoutID)
	if err != nil || !reflect.DeepEqual(current, plan.Loadout) {
		if len(report.Issues) != 0 {
			return result, fmt.Errorf("portable repository verification failed: %s", report.Issues[0].Message)
		}
		return result, errors.New("sealed study loadout no longer matches current promoted memory heads")
	}
	contextReceiptID := ""
	if task.Condition == ConditionMemory {
		contextResult, contextErr := loadoutcontext.BuildContext(store, options.PortableRoot, plan.Loadout.LoadoutID,
			studyRetrievalContext(plan, task))
		if contextErr != nil {
			return result, contextErr
		}
		contextReceiptID = contextResult.Receipt.ReceiptID
	}
	workspace, cleanupWorkspace, err := reserveIsolatedWorkspace(store)
	if err != nil {
		return result, err
	}
	defer cleanupWorkspace()
	request := expectedStudyRequest(task, contextReceiptID, workspace)
	planRecord := state.PlanRecords[studyID].Record
	reservation := TrialReservation{SchemaVersion: TrialReservationSchema, StudyID: studyID,
		TaskID: taskID, Condition: task.Condition,
		Plan: BoundEvent{EventID: studyID, RecordSHA256: planRecord.RecordHash}, Request: request,
		WorkspaceSnapshot: task.WorkspaceSnapshot, ReservedAt: options.Now().UTC(), Privacy: PrivacyLocalOnly}
	reservation.TrialID, err = trialReservationIdentity(reservation)
	if err != nil {
		return result, err
	}
	appender, err := store.NewAppenderAfterVisit(func(ledger.Record) error { return nil })
	if err != nil {
		return result, err
	}
	locked := replay(store)
	if len(locked.Issues) != 0 {
		return result, closeAppender(appender, fmt.Errorf("longitudinal study replay failed: %s", strings.Join(locked.Issues, "; ")))
	}
	if _, consumed := locked.Trials[key]; consumed {
		return result, closeAppender(appender, errors.New("longitudinal study task already consumed its one study-bound attempt"))
	}
	lockedPlan, exists := locked.Plans[studyID]
	if !exists || !reflect.DeepEqual(lockedPlan, plan) || locked.PlanRecords[studyID].Record.RecordHash != reservation.Plan.RecordSHA256 {
		return result, closeAppender(appender, errors.New("sealed longitudinal study changed before trial reservation"))
	}
	event, err := trialReservationEvent(store, plan.Agent, reservation)
	if err != nil {
		return result, closeAppender(appender, err)
	}
	records, err := appender.AppendBatch([]ledger.Event{event})
	if err != nil {
		return result, closeAppender(appender, err)
	}
	if err := closeAppender(appender, nil); err != nil {
		return result, err
	}
	result.Trial = reservation
	result.TrialEvent = BoundEvent{EventID: reservation.TrialID, RecordSHA256: records[0].RecordHash}
	if err := materializeWorkspace(store, task, workspace); err != nil {
		return finishFailedTrial(store, plan.Agent, result, "workspace_materialization", err, options.Now)
	}
	binding := workspaceBinding(task.WorkspaceSnapshot)
	agentOptions := agentbridge.Options{CodexPath: options.CodexPath,
		ParentEventIDs: []string{reservation.TrialID}, WorkspaceBinding: &binding,
		WorkspacePreflight: func() error { return verifyMaterializedWorkspace(workspace, task.WorkspaceSnapshot) },
		Now:                options.Now}
	if contextReceiptID != "" {
		agentOptions.PortableRoot = options.PortableRoot
	}
	execution, runErr := agentbridge.Run(ctx, store, request, agentOptions)
	result.Execution = &execution
	terminal := TrialTerminal{SchemaVersion: TrialTerminalSchema, StudyID: studyID, TaskID: taskID,
		Trial: result.TrialEvent, Outcome: "failed", FailureKind: "native_execution_setup",
		FinishedAt: options.Now().UTC(), Privacy: PrivacyLocalOnly}
	if execution.Receipt.ReceiptID != "" {
		binding, bindErr := lookupBoundEvent(store, execution.Receipt.ReceiptID)
		if bindErr != nil {
			return result, bindErr
		}
		terminal.Execution = &binding
		terminal.FailureKind = execution.Receipt.FailureKind
		if runErr == nil && execution.Receipt.Outcome == agentbridge.OutcomeCompleted {
			terminal.Outcome = "completed"
			terminal.FailureKind = ""
		}
	}
	if terminal.Outcome == "failed" && terminal.FailureKind == "" {
		terminal.FailureKind = "native_execution_failed"
	}
	if err := appendTrialTerminal(store, plan.Agent, &result, terminal); err != nil {
		if runErr != nil {
			return result, errors.Join(runErr, err)
		}
		return result, err
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

func finishFailedTrial(store *ledger.Store, agent ledger.Agent, result RunTaskResult, kind string, cause error,
	now func() time.Time) (RunTaskResult, error) {
	terminal := TrialTerminal{SchemaVersion: TrialTerminalSchema, StudyID: result.Trial.StudyID,
		TaskID: result.Trial.TaskID, Trial: result.TrialEvent, Outcome: "failed", FailureKind: kind,
		FinishedAt: now().UTC(), Privacy: PrivacyLocalOnly}
	if err := appendTrialTerminal(store, agent, &result, terminal); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, cause
}

func appendTrialTerminal(store *ledger.Store, agent ledger.Agent, result *RunTaskResult, terminal TrialTerminal) error {
	var err error
	terminal.TerminalID, err = trialTerminalIdentity(terminal)
	if err != nil {
		return err
	}
	event, err := trialTerminalEvent(store, agent, terminal)
	if err != nil {
		return err
	}
	record, err := store.Append(event)
	if err != nil {
		return err
	}
	result.Terminal = terminal
	result.TerminalEvent = BoundEvent{EventID: terminal.TerminalID, RecordSHA256: record.RecordHash}
	return nil
}

func studyRetrievalContext(plan Plan, task PlannedTask) retrieval.Context {
	result := retrieval.Context{Agent: plan.Agent, ThreadID: plan.StudyID, Task: task.TaskID,
		Channel: retrieval.ChannelHarness}
	switch plan.Loadout.ScopeKind {
	case "project":
		result.Project = plan.Loadout.ScopeValue
	case "repository":
		result.Repository = plan.Loadout.ScopeValue
	}
	return result
}

func trialReservationIdentity(value TrialReservation) (string, error) {
	copyValue := value
	copyValue.TrialID = ""
	data, err := canonicalJSON(copyValue)
	if err != nil {
		return "", err
	}
	return "study-trial-" + digest(data), nil
}

func trialTerminalIdentity(value TrialTerminal) (string, error) {
	copyValue := value
	copyValue.TerminalID = ""
	data, err := canonicalJSON(copyValue)
	if err != nil {
		return "", err
	}
	return "study-trial-terminal-" + digest(data), nil
}

func trialReservationEvent(store *ledger.Store, agent ledger.Agent, value TrialReservation) (ledger.Event, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return ledger.Event{}, err
	}
	payload := ledger.InlinePayload("json", TrialReservationMediaType, string(data))
	parents := []string{value.Plan.EventID}
	if value.Request.LoadoutContextReceiptID != "" {
		parents = append(parents, value.Request.LoadoutContextReceiptID)
	}
	return studyEvent(store, agent, value.TrialID, value.StudyID, value.TaskID,
		ledger.KindTaskExecutionStarted, payload, parents, value.ReservedAt), nil
}

func trialTerminalEvent(store *ledger.Store, agent ledger.Agent, value TrialTerminal) (ledger.Event, error) {
	data, err := canonicalJSON(value)
	if err != nil {
		return ledger.Event{}, err
	}
	payload := ledger.InlinePayload("json", TrialTerminalMediaType, string(data))
	parents := []string{value.Trial.EventID}
	if value.Execution != nil {
		parents = append(parents, value.Execution.EventID)
	}
	return studyEvent(store, agent, value.TerminalID, value.StudyID, value.TaskID,
		ledger.KindTaskExecutionReceipt, payload, parents, value.FinishedAt), nil
}

func studyEvent(store *ledger.Store, agent ledger.Agent, eventID, studyID, taskID string, kind ledger.EventKind,
	payload ledger.Payload, parents []string, at time.Time) ledger.Event {
	return ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: kind,
		ObservedAt: at, RecordedAt: at, Source: ledger.Source{Agent: agent, Adapter: "agentmem-study",
			AdapterVersion: AdapterVersion, DeviceID: store.DeviceID(), ThreadID: studyID,
			SourceEventID: eventID, SourceCursor: taskID}, Payload: &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: parents}, Privacy: ledger.Privacy{Classification: PrivacyLocalOnly}}
}

func lookupBoundEvent(store *ledger.Store, eventID string) (BoundEvent, error) {
	var result BoundEvent
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID == eventID {
			if result.EventID != "" {
				return errors.New("evidence event id is duplicated")
			}
			result = BoundEvent{EventID: eventID, RecordSHA256: record.RecordHash}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	if result.EventID == "" {
		return result, errors.New("evidence event is unavailable")
	}
	return result, nil
}

func expectedStudyRequest(task PlannedTask, contextReceiptID, workingDirectory string) agentbridge.RunRequest {
	return agentbridge.RunRequest{SchemaVersion: agentbridge.RunRequestSchema, TaskID: task.TaskID,
		Prompt: task.Prompt, Model: task.Model, Sandbox: task.Sandbox,
		WorkingDirectory: workingDirectory,
		TimeoutSeconds:   task.TimeoutSeconds, SkipGitRepositoryCheck: true,
		LoadoutContextReceiptID: contextReceiptID, Privacy: agentbridge.PrivacyLocalOnly}
}
