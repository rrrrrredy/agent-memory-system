package study

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func decodeTrialReservationEvent(event ledger.Event) (TrialReservation, error) {
	var value TrialReservation
	data, err := inlineStudyJSON(event, TrialReservationMediaType)
	if err != nil {
		return value, err
	}
	if err := decodeCanonical(data, &value); err != nil {
		return value, err
	}
	if err := validateTrialReservation(value); err != nil {
		return TrialReservation{}, err
	}
	return value, nil
}

func validateTrialReservation(value TrialReservation) error {
	if value.SchemaVersion != TrialReservationSchema || value.Privacy != PrivacyLocalOnly ||
		!validPrefixedHash(value.TrialID, "study-trial-") || !validPrefixedHash(value.StudyID, "study-") ||
		!safeID(value.TaskID) || value.ReservedAt.IsZero() || value.Plan.EventID != value.StudyID ||
		!validSHA256(value.Plan.RecordSHA256) || value.Request.SchemaVersion != agentbridge.RunRequestSchema ||
		value.Request.Privacy != agentbridge.PrivacyLocalOnly || value.Request.TaskID != value.TaskID {
		return errors.New("longitudinal trial reservation envelope is invalid")
	}
	if value.Condition != ConditionBaseline && value.Condition != ConditionMemory {
		return errors.New("longitudinal trial condition is invalid")
	}
	expected, err := trialReservationIdentity(value)
	if err != nil || expected != value.TrialID {
		return errors.New("longitudinal trial reservation identity is invalid")
	}
	return nil
}

func validateTrialReservationRecord(item indexedRecord, value TrialReservation) []string {
	event := item.Record.Event
	parents := []string{value.Plan.EventID}
	if value.Request.LoadoutContextReceiptID != "" {
		parents = append(parents, value.Request.LoadoutContextReceiptID)
	}
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, value.TrialID+": "+message)
		}
	}
	add(event.SchemaVersion == ledger.SchemaVersion && event.EventID == value.TrialID &&
		event.Kind == ledger.KindTaskExecutionStarted, "trial reservation event identity is invalid")
	add(event.ObservedAt.Equal(value.ReservedAt) && event.RecordedAt.Equal(value.ReservedAt),
		"trial reservation time is invalid")
	add(event.Source.Agent == ledger.AgentCodex && event.Source.Adapter == "agentmem-study" &&
		event.Source.AdapterVersion == AdapterVersion && event.Source.ThreadID == value.StudyID &&
		event.Source.SourceEventID == value.TrialID && event.Source.SourceCursor == value.TaskID,
		"trial reservation source binding is invalid")
	add(event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly, "trial reservation trust boundary is invalid")
	add(event.Causality != nil && reflect.DeepEqual(event.Causality.ParentEventIDs, parents),
		"trial reservation causality is invalid")
	return issues
}

func decodeTrialTerminalEvent(event ledger.Event) (TrialTerminal, error) {
	var value TrialTerminal
	data, err := inlineStudyJSON(event, TrialTerminalMediaType)
	if err != nil {
		return value, err
	}
	if err := decodeCanonical(data, &value); err != nil {
		return value, err
	}
	if err := validateTrialTerminal(value); err != nil {
		return TrialTerminal{}, err
	}
	return value, nil
}

func validateTrialTerminal(value TrialTerminal) error {
	if value.SchemaVersion != TrialTerminalSchema || value.Privacy != PrivacyLocalOnly ||
		!validPrefixedHash(value.TerminalID, "study-trial-terminal-") ||
		!validPrefixedHash(value.StudyID, "study-") || !safeID(value.TaskID) ||
		!validPrefixedHash(value.Trial.EventID, "study-trial-") ||
		!validSHA256(value.Trial.RecordSHA256) || value.FinishedAt.IsZero() {
		return errors.New("longitudinal trial terminal envelope is invalid")
	}
	switch value.Outcome {
	case "completed":
		if value.Execution == nil || value.FailureKind != "" {
			return errors.New("completed longitudinal trial terminal is invalid")
		}
	case "failed":
		if strings.TrimSpace(value.FailureKind) == "" {
			return errors.New("failed longitudinal trial terminal lacks a failure kind")
		}
	default:
		return errors.New("longitudinal trial terminal outcome is invalid")
	}
	if value.Execution != nil && (!validPrefixedHash(value.Execution.EventID, "native-agent-receipt-") ||
		!validSHA256(value.Execution.RecordSHA256)) {
		return errors.New("longitudinal trial execution binding is invalid")
	}
	expected, err := trialTerminalIdentity(value)
	if err != nil || expected != value.TerminalID {
		return errors.New("longitudinal trial terminal identity is invalid")
	}
	return nil
}

func validateTrialTerminalRecord(item indexedRecord, value TrialTerminal) []string {
	event := item.Record.Event
	parents := []string{value.Trial.EventID}
	if value.Execution != nil {
		parents = append(parents, value.Execution.EventID)
	}
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, value.TerminalID+": "+message)
		}
	}
	add(event.SchemaVersion == ledger.SchemaVersion && event.EventID == value.TerminalID &&
		event.Kind == ledger.KindTaskExecutionReceipt, "trial terminal event identity is invalid")
	add(event.ObservedAt.Equal(value.FinishedAt) && event.RecordedAt.Equal(value.FinishedAt),
		"trial terminal time is invalid")
	add(event.Source.Agent == ledger.AgentCodex && event.Source.Adapter == "agentmem-study" &&
		event.Source.AdapterVersion == AdapterVersion && event.Source.ThreadID == value.StudyID &&
		event.Source.SourceEventID == value.TerminalID && event.Source.SourceCursor == value.TaskID,
		"trial terminal source binding is invalid")
	add(event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly, "trial terminal trust boundary is invalid")
	add(event.Causality != nil && reflect.DeepEqual(event.Causality.ParentEventIDs, parents),
		"trial terminal causality is invalid")
	return issues
}

func validateTrialLinks(store *ledger.Store, state *replayState,
	verifiedExecutions map[string]agentbridge.VerifiedExecution) {
	for key, trial := range state.Trials {
		prefix := trial.StudyID + "/" + trial.TaskID + ": "
		plan, exists := state.Plans[trial.StudyID]
		task, planned := plannedTask(plan, trial.TaskID)
		planRecord, planRecorded := state.PlanRecords[trial.StudyID]
		trialRecord, trialRecorded := state.TrialRecords[trial.TrialID]
		if !exists || !planned || !planRecorded || !trialRecorded ||
			trial.Plan.RecordSHA256 != planRecord.Record.RecordHash || planRecord.Index >= trialRecord.Index {
			state.Issues = append(state.Issues, prefix+"trial does not bind the sealed prospective plan")
			continue
		}
		contextID := trial.Request.LoadoutContextReceiptID
		expectedRequest := expectedStudyRequest(task, contextID, trial.Request.WorkingDirectory)
		if trial.Condition != task.Condition || !reflect.DeepEqual(trial.WorkspaceSnapshot, task.WorkspaceSnapshot) ||
			!reflect.DeepEqual(trial.Request, expectedRequest) ||
			!validIsolatedWorkspacePath(store, trial.Request.WorkingDirectory) {
			state.Issues = append(state.Issues, prefix+"trial request differs from the sealed task contract")
			continue
		}
		if task.Condition == ConditionBaseline && contextID != "" {
			state.Issues = append(state.Issues, prefix+"baseline trial received memory")
			continue
		}
		if task.Condition == ConditionMemory {
			contextReceipt, verified := state.VerifiedContexts[contextID]
			if !verified || !reflect.DeepEqual(contextReceipt.Loadout, plan.Loadout) ||
				contextReceipt.Context.Agent != plan.Agent || contextReceipt.Context.Task != task.TaskID {
				state.Issues = append(state.Issues, prefix+"memory trial does not bind the sealed loadout context")
				continue
			}
		}
		terminal, finished := state.TrialTerminals[key]
		if !finished {
			state.Issues = append(state.Issues, prefix+"trial reservation has no terminal result")
			continue
		}
		terminalRecord := state.TrialTerminalRecords[terminal.TerminalID]
		if terminal.Trial.EventID != trial.TrialID || terminal.Trial.RecordSHA256 != trialRecord.Record.RecordHash ||
			trialRecord.Index >= terminalRecord.Index {
			state.Issues = append(state.Issues, prefix+"trial terminal does not bind its reservation")
			continue
		}
		if terminal.Execution == nil {
			if terminal.Outcome != "failed" {
				state.Issues = append(state.Issues, prefix+"terminal result lacks its execution receipt")
			}
			continue
		}
		executionRecord, found := state.Records[terminal.Execution.EventID]
		if !found || executionRecord.Record.RecordHash != terminal.Execution.RecordSHA256 ||
			executionRecord.Index >= terminalRecord.Index {
			state.Issues = append(state.Issues, prefix+"terminal execution record binding is invalid")
			continue
		}
		execution, verified := verifiedExecutions[terminal.Execution.EventID]
		startedRecord, startedFound := state.Records[execution.Started.StartedEventID]
		if !verified || !startedFound || trialRecord.Index >= startedRecord.Index ||
			startedRecord.Index >= executionRecord.Index || !hasParent(startedRecord.Record.Event, trial.TrialID) ||
			!reflect.DeepEqual(execution.Request, trial.Request) || execution.Started.WorkspaceBinding == nil ||
			!reflect.DeepEqual(*execution.Started.WorkspaceBinding, workspaceBinding(task.WorkspaceSnapshot)) {
			state.Issues = append(state.Issues, prefix+"study-bound native execution does not replay")
			continue
		}
		if terminal.Outcome == "completed" && execution.Receipt.Outcome != agentbridge.OutcomeCompleted ||
			terminal.Outcome == "failed" && execution.Receipt.Outcome != agentbridge.OutcomeFailed {
			state.Issues = append(state.Issues, prefix+"trial terminal outcome differs from the native receipt")
		}
	}
	for key, terminal := range state.TrialTerminals {
		if _, exists := state.Trials[key]; !exists {
			state.Issues = append(state.Issues, fmt.Sprintf("%s/%s: terminal has no trial reservation", terminal.StudyID, terminal.TaskID))
		}
	}
}
