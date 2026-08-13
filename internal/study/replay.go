package study

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
)

type indexedRecord struct {
	Index  int
	Record ledger.Record
}

type replayState struct {
	Records              map[string]indexedRecord
	Plans                map[string]Plan
	PlanRecords          map[string]indexedRecord
	Trials               map[string]TrialReservation
	TrialRecords         map[string]indexedRecord
	TrialTerminals       map[string]TrialTerminal
	TrialTerminalRecords map[string]indexedRecord
	Outcomes             map[string]OutcomeEvidence
	OutcomeRecords       map[string]indexedRecord
	Observations         map[string]map[string]Observation
	ObservationRecords   map[string]indexedRecord
	VerifiedExecutions   map[string]agentbridge.VerifiedExecution
	VerifiedContexts     map[string]loadoutcontext.ContextReceipt
	Issues               []string
}

func newReplayState() replayState {
	return replayState{
		Records:              map[string]indexedRecord{},
		Plans:                map[string]Plan{},
		PlanRecords:          map[string]indexedRecord{},
		Trials:               map[string]TrialReservation{},
		TrialRecords:         map[string]indexedRecord{},
		TrialTerminals:       map[string]TrialTerminal{},
		TrialTerminalRecords: map[string]indexedRecord{},
		Outcomes:             map[string]OutcomeEvidence{},
		OutcomeRecords:       map[string]indexedRecord{},
		Observations:         map[string]map[string]Observation{},
		ObservationRecords:   map[string]indexedRecord{},
		VerifiedExecutions:   map[string]agentbridge.VerifiedExecution{},
		VerifiedContexts:     map[string]loadoutcontext.ContextReceipt{},
		Issues:               []string{},
	}
}

func replay(store *ledger.Store) replayState {
	return replayWithExecutionLoader(store, agentbridge.BuildVerifiedIndex)
}

type verifiedExecutionLoader func(*ledger.Store) agentbridge.VerifiedIndex
type workspaceContentVerifier func(*ledger.Store, WorkspaceSnapshot) error

func replayWithExecutionLoader(store *ledger.Store, loadExecutions verifiedExecutionLoader) replayState {
	return replayWithDependencies(store, loadExecutions, verifyWorkspaceSnapshotContent)
}

type workspaceContentKey struct {
	Archive           ledger.BlobRef
	TreeSHA256        string
	Format            string
	Policy            string
	Files             int
	UncompressedBytes int64
}

func replayWithDependencies(store *ledger.Store, loadExecutions verifiedExecutionLoader,
	verifyWorkspaceContent workspaceContentVerifier) replayState {
	state := newReplayState()
	if store == nil {
		state.Issues = append(state.Issues, "local evidence store is required")
		return state
	}
	if report := store.Verify(); len(report.Issues) != 0 {
		state.Issues = append(state.Issues, "evidence ledger verification failed")
		return state
	}
	ordered := []indexedRecord{}
	index := 0
	if err := store.VisitRecords(func(record ledger.Record) error {
		index++
		item := indexedRecord{Index: index, Record: record}
		if _, duplicate := state.Records[record.Event.EventID]; duplicate {
			state.Issues = append(state.Issues, "evidence event id is duplicated: "+record.Event.EventID)
		} else {
			state.Records[record.Event.EventID] = item
		}
		ordered = append(ordered, item)
		return nil
	}); err != nil {
		state.Issues = append(state.Issues, "evidence ledger traversal failed")
		return state
	}
	for _, item := range ordered {
		event := item.Record.Event
		if event.Payload == nil {
			continue
		}
		switch event.Payload.MediaType {
		case PlanMediaType:
			plan, err := decodePlanEvent(event)
			if err != nil {
				state.Issues = append(state.Issues, event.EventID+": "+err.Error())
				continue
			}
			if _, duplicate := state.Plans[plan.StudyID]; duplicate {
				state.Issues = append(state.Issues, plan.StudyID+": study plan is duplicated")
				continue
			}
			if issues := validatePlanRecord(item, plan); len(issues) != 0 {
				state.Issues = append(state.Issues, issues...)
				continue
			}
			state.Plans[plan.StudyID] = plan
			state.PlanRecords[plan.StudyID] = item
		case TrialReservationMediaType:
			trial, err := decodeTrialReservationEvent(event)
			if err != nil {
				state.Issues = append(state.Issues, event.EventID+": "+err.Error())
				continue
			}
			if issues := validateTrialReservationRecord(item, trial); len(issues) != 0 {
				state.Issues = append(state.Issues, issues...)
				continue
			}
			key := outcomeKey(trial.StudyID, trial.TaskID)
			if _, duplicate := state.Trials[key]; duplicate {
				state.Issues = append(state.Issues, trial.StudyID+"/"+trial.TaskID+": trial reservation is duplicated")
				continue
			}
			state.Trials[key] = trial
			state.TrialRecords[trial.TrialID] = item
		case TrialTerminalMediaType:
			terminal, err := decodeTrialTerminalEvent(event)
			if err != nil {
				state.Issues = append(state.Issues, event.EventID+": "+err.Error())
				continue
			}
			if issues := validateTrialTerminalRecord(item, terminal); len(issues) != 0 {
				state.Issues = append(state.Issues, issues...)
				continue
			}
			key := outcomeKey(terminal.StudyID, terminal.TaskID)
			if _, duplicate := state.TrialTerminals[key]; duplicate {
				state.Issues = append(state.Issues, terminal.StudyID+"/"+terminal.TaskID+": trial terminal is duplicated")
				continue
			}
			state.TrialTerminals[key] = terminal
			state.TrialTerminalRecords[terminal.TerminalID] = item
		case OutcomeEvidenceMediaType:
			evidence, err := decodeOutcomeEvidenceEvent(event)
			if err != nil {
				state.Issues = append(state.Issues, event.EventID+": "+err.Error())
				continue
			}
			issues := validateOutcomeEvidenceRecord(item, evidence)
			if len(issues) != 0 {
				state.Issues = append(state.Issues, issues...)
				continue
			}
			key := outcomeKey(evidence.StudyID, evidence.TaskID)
			if _, duplicate := state.Outcomes[key]; duplicate {
				state.Issues = append(state.Issues, evidence.EvidenceID+": outcome evidence is duplicated")
				continue
			}
			state.Outcomes[key] = evidence
			state.OutcomeRecords[evidence.EvidenceID] = item
		case ObservationMediaType:
			observation, err := decodeObservationEvent(event)
			if err != nil {
				state.Issues = append(state.Issues, event.EventID+": "+err.Error())
				continue
			}
			if issues := validateObservationRecord(item, observation); len(issues) != 0 {
				state.Issues = append(state.Issues, issues...)
				continue
			}
			if _, exists := state.Observations[observation.StudyID]; !exists {
				state.Observations[observation.StudyID] = map[string]Observation{}
			}
			if _, duplicate := state.Observations[observation.StudyID][observation.TaskID]; duplicate {
				state.Issues = append(state.Issues, observation.StudyID+"/"+observation.TaskID+": observation is duplicated")
				continue
			}
			state.Observations[observation.StudyID][observation.TaskID] = observation
			state.ObservationRecords[observation.ObservationID] = item
		default:
			if event.Source.Adapter == "agentmem-study" {
				state.Issues = append(state.Issues, event.EventID+": study event media type is invalid")
			}
		}
	}
	taskOwners := map[string]string{}
	verifiedWorkspaceContent := map[workspaceContentKey]error{}
	for studyID, plan := range state.Plans {
		for _, task := range plan.Tasks {
			snapshot := task.WorkspaceSnapshot
			if err := validateWorkspaceSnapshotEnvelope(snapshot, task.WorkingDirectory); err != nil {
				state.Issues = append(state.Issues, studyID+"/"+task.TaskID+": sealed workspace snapshot is invalid")
			} else {
				key := workspaceContentKey{Archive: snapshot.Archive, TreeSHA256: snapshot.TreeSHA256,
					Format: snapshot.Format, Policy: snapshot.Policy, Files: snapshot.Files,
					UncompressedBytes: snapshot.UncompressedBytes}
				contentErr, checked := verifiedWorkspaceContent[key]
				if !checked {
					contentErr = verifyWorkspaceContent(store, snapshot)
					verifiedWorkspaceContent[key] = contentErr
				}
				if contentErr != nil {
					state.Issues = append(state.Issues, studyID+"/"+task.TaskID+": sealed workspace snapshot is invalid")
				}
			}
			if owner, exists := taskOwners[task.TaskID]; exists && owner != studyID {
				state.Issues = append(state.Issues,
					studyID+"/"+task.TaskID+": task id is already sealed by study "+owner)
				continue
			}
			taskOwners[task.TaskID] = studyID
		}
	}
	executionIndex := loadExecutions(store)
	if len(executionIndex.Report.Issues) != 0 {
		state.Issues = append(state.Issues, "native Agent executions cannot be replayed")
	} else {
		state.VerifiedExecutions = executionIndex.Executions
		state.VerifiedContexts = executionIndex.LoadoutContexts
	}
	validateTrialLinks(store, &state, state.VerifiedExecutions)
	validateStudyLinks(store, &state, state.VerifiedExecutions)
	sort.Strings(state.Issues)
	return state
}

func decodePlanEvent(event ledger.Event) (Plan, error) {
	var plan Plan
	data, err := inlineStudyJSON(event, PlanMediaType)
	if err != nil {
		return plan, err
	}
	if err := decodeCanonical(data, &plan); err != nil {
		return plan, fmt.Errorf("decode study plan: %w", err)
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func decodeObservationEvent(event ledger.Event) (Observation, error) {
	var observation Observation
	data, err := inlineStudyJSON(event, ObservationMediaType)
	if err != nil {
		return observation, err
	}
	if err := decodeCanonical(data, &observation); err != nil {
		return observation, fmt.Errorf("decode study observation: %w", err)
	}
	if err := validateObservation(observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func decodeCanonical(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("payload contains trailing JSON")
	}
	expected, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return errors.New("payload is not canonical JSON")
	}
	return nil
}

func inlineStudyJSON(event ledger.Event, mediaType string) ([]byte, error) {
	if event.Payload == nil || event.Payload.Content == nil || event.Payload.Blob != nil ||
		event.Payload.Encoding != "json" || event.Payload.MediaType != mediaType {
		return nil, errors.New("study payload binding is invalid")
	}
	expected := ledger.InlinePayload("json", mediaType, *event.Payload.Content)
	if !reflect.DeepEqual(*event.Payload, expected) {
		return nil, errors.New("study payload measurements are invalid")
	}
	return []byte(*event.Payload.Content), nil
}

func validatePlanRecord(item indexedRecord, plan Plan) []string {
	event := item.Record.Event
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, plan.StudyID+": "+message)
		}
	}
	add(event.SchemaVersion == ledger.SchemaVersion && event.EventID == plan.StudyID &&
		event.Kind == ledger.KindEvaluationTrialPlan, "plan event identity is invalid")
	add(event.ObservedAt.Equal(plan.CreatedAt) && event.RecordedAt.Equal(plan.CreatedAt),
		"plan event time is invalid")
	add(event.Source.Agent == plan.Agent && event.Source.Adapter == "agentmem-study" &&
		event.Source.AdapterVersion == AdapterVersion && event.Source.ThreadID == plan.StudyID &&
		event.Source.SourceEventID == plan.StudyID, "plan source binding is invalid")
	add(event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly, "plan trust boundary is invalid")
	add(event.Causality == nil || len(event.Causality.ParentEventIDs) == 0,
		"plan must not depend on post-plan evidence")
	return issues
}

func validateObservation(observation Observation) error {
	if observation.SchemaVersion != ObservationSchema || observation.Privacy != PrivacyLocalOnly ||
		!validPrefixedHash(observation.ObservationID, "study-observation-") ||
		!validPrefixedHash(observation.StudyID, "study-") || !safeID(observation.TaskID) ||
		!validPrefixedHash(observation.Execution.EventID, "native-agent-receipt-") ||
		!validSHA256(observation.Execution.RecordSHA256) || observation.ObservedAt.IsZero() ||
		strings.TrimSpace(observation.Reporter.ID) == "" || len(observation.Reporter.ID) > 128 ||
		len(strings.TrimSpace(observation.Reason)) < 10 || len([]byte(observation.Reason)) > 2048 {
		return errors.New("longitudinal study observation envelope is invalid")
	}
	if observation.Condition != ConditionBaseline && observation.Condition != ConditionMemory {
		return errors.New("longitudinal study observation condition is invalid")
	}
	if observation.Outcome != OutcomeSuccess && observation.Outcome != OutcomeFailure {
		return errors.New("longitudinal study observation outcome is invalid")
	}
	if observation.Reporter.Kind != "caller_attestation" && observation.Reporter.Kind != "synthetic_test" {
		return errors.New("longitudinal study observation reporter is invalid")
	}
	if observation.OutcomeEvidence == nil ||
		observation.OutcomeAuthority != AuthorityBuiltinAcceptance ||
		!validPrefixedHash(observation.OutcomeEvidence.EventID, "study-outcome-") ||
		!validSHA256(observation.OutcomeEvidence.RecordSHA256) {
		return errors.New("longitudinal outcome is not bound to the built-in acceptance replay")
	}
	expected, err := observationIdentity(observation)
	if err != nil || expected != observation.ObservationID {
		return errors.New("longitudinal study observation identity is invalid")
	}
	return nil
}

func validateObservationRecord(item indexedRecord, observation Observation) []string {
	event := item.Record.Event
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, observation.ObservationID+": "+message)
		}
	}
	add(event.SchemaVersion == ledger.SchemaVersion && event.EventID == observation.ObservationID &&
		event.Kind == ledger.KindEvaluationAttestation, "observation event identity is invalid")
	add(event.ObservedAt.Equal(observation.ObservedAt) && event.RecordedAt.Equal(observation.ObservedAt),
		"observation event time is invalid")
	add(event.Source.Adapter == "agentmem-study" && event.Source.AdapterVersion == AdapterVersion &&
		event.Source.ThreadID == observation.StudyID && event.Source.SourceEventID == observation.ObservationID &&
		event.Source.SourceCursor == observation.TaskID, "observation source binding is invalid")
	add(event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly, "observation trust boundary is invalid")
	parents := []string{observation.StudyID, observation.Execution.EventID}
	if observation.OutcomeEvidence != nil {
		parents = append(parents, observation.OutcomeEvidence.EventID)
	}
	add(event.Causality != nil && reflect.DeepEqual(event.Causality.ParentEventIDs, parents),
		"observation causality is invalid")
	return issues
}

func validateStudyLinks(store *ledger.Store, state *replayState,
	verifiedExecutions map[string]agentbridge.VerifiedExecution) {
	validateOutcomeLinks(store, state, verifiedExecutions)
	for studyID, observations := range state.Observations {
		plan, exists := state.Plans[studyID]
		if !exists {
			state.Issues = append(state.Issues, studyID+": observations do not have a verified prospective plan")
			continue
		}
		tasks := map[string]PlannedTask{}
		for _, task := range plan.Tasks {
			tasks[task.TaskID] = task
		}
		for taskID, observation := range observations {
			prefix := studyID + "/" + taskID + ": "
			task, planned := tasks[taskID]
			if !planned || task.Condition != observation.Condition {
				state.Issues = append(state.Issues, prefix+"observation does not match the sealed assignment")
				continue
			}
			executionRecord, exists := state.Records[observation.Execution.EventID]
			if !exists || executionRecord.Record.RecordHash != observation.Execution.RecordSHA256 {
				state.Issues = append(state.Issues, prefix+"execution record binding is invalid")
				continue
			}
			execution, verified := verifiedExecutions[observation.Execution.EventID]
			if !verified {
				state.Issues = append(state.Issues, prefix+"native Agent execution cannot be replayed")
				continue
			}
			if issue := executionEligibility(store, *state, plan, task, execution,
				observation.Execution.EventID); issue != "" {
				state.Issues = append(state.Issues, issue)
				continue
			}
			observationRecord := state.ObservationRecords[observation.ObservationID]
			if executionRecord.Index >= observationRecord.Index {
				state.Issues = append(state.Issues, prefix+"observation precedes its execution receipt")
				continue
			}
			if observation.OutcomeEvidence == nil {
				state.Issues = append(state.Issues, prefix+"built-in outcome evidence is unavailable")
				continue
			}
			evidenceRecord, exists := state.Records[observation.OutcomeEvidence.EventID]
			if !exists || evidenceRecord.Record.RecordHash != observation.OutcomeEvidence.RecordSHA256 ||
				executionRecord.Index >= evidenceRecord.Index || evidenceRecord.Index >= observationRecord.Index {
				state.Issues = append(state.Issues, prefix+"built-in outcome evidence binding is invalid")
				continue
			}
			evidence, err := decodeOutcomeEvidenceEvent(evidenceRecord.Record.Event)
			if err != nil || evidence.StudyID != studyID || evidence.TaskID != taskID ||
				evidence.Execution != observation.Execution || evidence.Outcome != observation.Outcome {
				state.Issues = append(state.Issues, prefix+"built-in outcome evidence does not replay")
				continue
			}
			replayed, err := evaluateOutcome(store, plan, task, execution, observation.Execution, evidence.EvaluatedAt)
			if err != nil || !reflect.DeepEqual(replayed, evidence) {
				state.Issues = append(state.Issues, prefix+"built-in acceptance result changed during replay")
			}
		}
	}
}

func hasParent(event ledger.Event, parent string) bool {
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

func validateOutcomeLinks(store *ledger.Store, state *replayState,
	verifiedExecutions map[string]agentbridge.VerifiedExecution) {
	for _, evidence := range state.Outcomes {
		prefix := evidence.StudyID + "/" + evidence.TaskID + ": "
		plan, exists := state.Plans[evidence.StudyID]
		if !exists {
			state.Issues = append(state.Issues, prefix+"outcome evidence has no prospective plan")
			continue
		}
		task, planned := plannedTask(plan, evidence.TaskID)
		if !planned {
			state.Issues = append(state.Issues, prefix+"outcome evidence task is not sealed")
			continue
		}
		executionRecord, exists := state.Records[evidence.Execution.EventID]
		if !exists || executionRecord.Record.RecordHash != evidence.Execution.RecordSHA256 {
			state.Issues = append(state.Issues, prefix+"outcome execution binding is invalid")
			continue
		}
		outcomeRecord, exists := state.OutcomeRecords[evidence.EvidenceID]
		if !exists || executionRecord.Index >= outcomeRecord.Index ||
			outcomeRecord.Record.Event.Source.Agent != plan.Agent {
			state.Issues = append(state.Issues, prefix+"outcome evidence order or Agent is invalid")
			continue
		}
		execution, verified := verifiedExecutions[evidence.Execution.EventID]
		if !verified {
			state.Issues = append(state.Issues, prefix+"outcome execution cannot be replayed")
			continue
		}
		if issue := executionEligibility(store, *state, plan, task, execution,
			evidence.Execution.EventID); issue != "" {
			state.Issues = append(state.Issues, issue)
			continue
		}
		replayed, err := evaluateOutcome(store, plan, task, execution,
			evidence.Execution, evidence.EvaluatedAt)
		if err != nil || !reflect.DeepEqual(replayed, evidence) {
			state.Issues = append(state.Issues, prefix+"built-in acceptance result does not replay")
		}
	}
}
func executionEligibility(store *ledger.Store, state replayState, plan Plan, task PlannedTask,
	execution agentbridge.VerifiedExecution, receiptID string) string {
	prefix := plan.StudyID + "/" + task.TaskID + ": "
	planRecord, planExists := state.PlanRecords[plan.StudyID]
	receiptRecord, receiptExists := state.Records[receiptID]
	startedRecord, startedExists := state.Records[execution.Started.StartedEventID]
	key := outcomeKey(plan.StudyID, task.TaskID)
	trial, trialExists := state.Trials[key]
	terminal, terminalExists := state.TrialTerminals[key]
	trialRecord, trialRecorded := state.TrialRecords[trial.TrialID]
	terminalRecord, terminalRecorded := state.TrialTerminalRecords[terminal.TerminalID]
	if !planExists || !receiptExists || !startedExists || !trialExists || !terminalExists ||
		!trialRecorded || !terminalRecorded || planRecord.Index >= trialRecord.Index ||
		trialRecord.Index >= startedRecord.Index || startedRecord.Index >= receiptRecord.Index ||
		receiptRecord.Index >= terminalRecord.Index {
		return prefix + "execution is not the terminal result of its prospective one-shot reservation"
	}
	if execution.Receipt.Outcome != agentbridge.OutcomeCompleted ||
		execution.Receipt.TaskID != task.TaskID || execution.Request.TaskID != task.TaskID {
		return prefix + "native Agent execution is not verified and completed"
	}
	if terminal.Outcome != "completed" || terminal.Execution == nil || terminal.Execution.EventID != receiptID ||
		terminal.Execution.RecordSHA256 != receiptRecord.Record.RecordHash ||
		trial.Plan.EventID != plan.StudyID || trial.Plan.RecordSHA256 != planRecord.Record.RecordHash ||
		trial.Condition != task.Condition || !reflect.DeepEqual(trial.WorkspaceSnapshot, task.WorkspaceSnapshot) ||
		!reflect.DeepEqual(trial.Request, execution.Request) || !hasParent(startedRecord.Record.Event, trial.TrialID) ||
		execution.Started.WorkspaceBinding == nil ||
		!reflect.DeepEqual(*execution.Started.WorkspaceBinding, workspaceBinding(task.WorkspaceSnapshot)) {
		return prefix + "execution does not replay from the sealed one-shot trial"
	}
	expectedRequest := expectedStudyRequest(task, execution.Request.LoadoutContextReceiptID,
		execution.Request.WorkingDirectory)
	if !reflect.DeepEqual(execution.Request, expectedRequest) ||
		!validIsolatedWorkspacePath(store, execution.Request.WorkingDirectory) {
		return prefix + "execution request differs from the sealed snapshot task contract"
	}
	for otherStudyID, otherObservations := range state.Observations {
		for otherTaskID, other := range otherObservations {
			if other.Execution.EventID == receiptID &&
				(otherStudyID != plan.StudyID || otherTaskID != task.TaskID) {
				return prefix + "execution receipt is already consumed by another study task"
			}
		}
	}
	if task.Condition == ConditionBaseline {
		if execution.Started.LoadoutContextReceiptID != "" || len(execution.Started.MemoryReferences) != 0 {
			return prefix + "baseline execution received memory"
		}
		return ""
	}
	contextReceipt := execution.LoadoutContext
	if contextReceipt == nil || !reflect.DeepEqual(contextReceipt.Loadout, plan.Loadout) ||
		contextReceipt.Context.Agent != plan.Agent ||
		contextReceipt.Context.Task != "" && contextReceipt.Context.Task != task.TaskID {
		return prefix + "memory execution does not bind the sealed loadout"
	}
	return ""
}

func validateOutcomeEvidenceRecord(item indexedRecord, evidence OutcomeEvidence) []string {
	event := item.Record.Event
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, evidence.EvidenceID+": "+message)
		}
	}
	add(event.SchemaVersion == ledger.SchemaVersion && event.EventID == evidence.EvidenceID &&
		event.Kind == ledger.KindToolResult, "outcome evidence event identity is invalid")
	add(event.ObservedAt.Equal(evidence.EvaluatedAt) && event.RecordedAt.Equal(evidence.EvaluatedAt),
		"outcome evidence time is invalid")
	add(event.Source.Adapter == "agentmem-study" && event.Source.AdapterVersion == AdapterVersion &&
		event.Source.ThreadID == evidence.StudyID && event.Source.SourceEventID == evidence.EvidenceID &&
		event.Source.SourceCursor == evidence.TaskID, "outcome evidence source binding is invalid")
	add(event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly, "outcome evidence trust boundary is invalid")
	add(event.Causality != nil && reflect.DeepEqual(event.Causality.ParentEventIDs,
		[]string{evidence.StudyID, evidence.Execution.EventID}), "outcome evidence causality is invalid")
	return issues
}
func decodeOutcomeEvidenceEvent(event ledger.Event) (OutcomeEvidence, error) {
	var evidence OutcomeEvidence
	data, err := inlineStudyJSON(event, OutcomeEvidenceMediaType)
	if err != nil {
		return evidence, err
	}
	if err := decodeCanonical(data, &evidence); err != nil {
		return evidence, err
	}
	if evidence.SchemaVersion != OutcomeEvidenceSchema || evidence.Privacy != PrivacyLocalOnly ||
		!validPrefixedHash(evidence.EvidenceID, "study-outcome-") ||
		!validPrefixedHash(evidence.StudyID, "study-") || !safeID(evidence.TaskID) ||
		evidence.Evaluator != "builtin-exact-sha256/v1" || evidence.EvaluatedAt.IsZero() ||
		!validSHA256(evidence.AcceptanceSHA256) || len(evidence.AssertionResults) == 0 {
		return OutcomeEvidence{}, errors.New("built-in outcome evidence is invalid")
	}
	expected, err := outcomeEvidenceIdentity(evidence)
	if err != nil || expected != evidence.EvidenceID {
		return OutcomeEvidence{}, errors.New("built-in outcome evidence identity is invalid")
	}
	return evidence, nil
}

func outcomeKey(studyID, taskID string) string {
	return studyID + "\x00" + taskID
}
