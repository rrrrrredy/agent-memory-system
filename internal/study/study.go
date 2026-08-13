package study

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

func Create(store *ledger.Store, draft Draft, options CreateOptions) (CreateResult, error) {
	result := CreateResult{SchemaVersion: CreateResultSchema, Privacy: PrivacyLocalOnly}
	if store == nil {
		return result, errors.New("local evidence store is required")
	}
	if err := validateDraft(draft); err != nil {
		return result, err
	}
	if strings.TrimSpace(options.PortableRoot) == "" {
		return result, errors.New("portable repository root is required")
	}
	selected, _, err := portable.LoadCurrentLoadout(options.PortableRoot, draft.LoadoutID)
	if err != nil {
		return result, fmt.Errorf("load current portable loadout: %w", err)
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	plan, err := buildPlan(store, draft, selected, now().UTC())
	if err != nil {
		return result, err
	}
	appender, err := store.NewAppenderAfterVisit(func(ledger.Record) error { return nil })
	if err != nil {
		return result, err
	}
	state := replay(store)
	if len(state.Issues) != 0 {
		return result, closeAppender(appender,
			fmt.Errorf("longitudinal study replay failed: %s", strings.Join(state.Issues, "; ")))
	}
	if existing, found := state.Plans[plan.StudyID]; found {
		if !reflect.DeepEqual(existing, plan) {
			return result, closeAppender(appender, errors.New("study identity already exists with different content"))
		}
		record := state.PlanRecords[plan.StudyID].Record
		result.Plan = plan
		result.Event = BoundEvent{EventID: plan.StudyID, RecordSHA256: record.RecordHash}
		return result, closeAppender(appender, nil)
	}
	for existingStudyID, existingPlan := range state.Plans {
		for _, existingTask := range existingPlan.Tasks {
			for _, task := range plan.Tasks {
				if existingTask.TaskID == task.TaskID {
					return result, closeAppender(appender, fmt.Errorf(
						"longitudinal study task id %q is already sealed by study %s", task.TaskID, existingStudyID))
				}
			}
		}
	}
	if _, conflict := state.Records[plan.StudyID]; conflict {
		return result, closeAppender(appender, errors.New("study identity conflicts with an existing event"))
	}
	event, err := planEvent(store, plan)
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
	result.Plan = plan
	result.Event = BoundEvent{EventID: plan.StudyID, RecordSHA256: records[0].RecordHash}
	result.Written = true
	return result, nil
}

func Observe(store *ledger.Store, request ObservationRequest, now func() time.Time) (ObserveResult, error) {
	result := ObserveResult{SchemaVersion: ObserveResultSchema, Privacy: PrivacyLocalOnly}
	if store == nil {
		return result, errors.New("local evidence store is required")
	}
	if err := validateObservationRequest(request); err != nil {
		return result, err
	}
	if now == nil {
		now = time.Now
	}
	appender, err := store.NewAppenderAfterVisit(func(ledger.Record) error { return nil })
	if err != nil {
		return result, err
	}
	state := replay(store)
	if len(state.Issues) != 0 {
		return result, closeAppender(appender,
			fmt.Errorf("longitudinal study replay failed: %s", strings.Join(state.Issues, "; ")))
	}
	plan, exists := state.Plans[request.StudyID]
	if !exists {
		return result, closeAppender(appender, errors.New("prospectively sealed longitudinal study is unavailable"))
	}
	if _, exists := state.Observations[request.StudyID][request.TaskID]; exists {
		return result, closeAppender(appender, errors.New("longitudinal study task already has an observation"))
	}
	task, found := plannedTask(plan, request.TaskID)
	if !found {
		return result, closeAppender(appender, errors.New("task is not present in the sealed study population"))
	}
	executionRecord, found := state.Records[request.ExecutionReceiptID]
	if !found {
		return result, closeAppender(appender, errors.New("native Agent execution receipt is unavailable"))
	}
	execution, err := agentbridge.ResolveVerifiedExecution(store, request.ExecutionReceiptID)
	if err != nil {
		return result, closeAppender(appender, err)
	}
	if issue := executionEligibility(store, state, plan, task, execution, request.ExecutionReceiptID); issue != "" {
		return result, closeAppender(appender, errors.New(issue))
	}
	observedAt := now().UTC()
	executionBinding := BoundEvent{EventID: request.ExecutionReceiptID,
		RecordSHA256: executionRecord.Record.RecordHash}
	outcomeEvidence, recovered := state.Outcomes[outcomeKey(request.StudyID, request.TaskID)]
	var outcomeReference BoundEvent
	if recovered {
		replayed, replayErr := evaluateOutcome(store, plan, task, execution, executionBinding,
			outcomeEvidence.EvaluatedAt)
		if replayErr != nil || !reflect.DeepEqual(replayed, outcomeEvidence) {
			return result, closeAppender(appender, errors.New("recorded outcome evidence cannot be replayed"))
		}
		record := state.OutcomeRecords[outcomeEvidence.EvidenceID].Record
		outcomeReference = BoundEvent{EventID: outcomeEvidence.EvidenceID, RecordSHA256: record.RecordHash}
	} else {
		outcomeEvidence, err = evaluateOutcome(store, plan, task, execution, executionBinding, observedAt)
		if err != nil {
			return result, closeAppender(appender, err)
		}
		outcomeEvent, eventErr := outcomeEvidenceEvent(store, plan.Agent, outcomeEvidence)
		if eventErr != nil {
			return result, closeAppender(appender, eventErr)
		}
		outcomeRecords, appendErr := appender.AppendBatch([]ledger.Event{outcomeEvent})
		if appendErr != nil {
			return result, closeAppender(appender, appendErr)
		}
		outcomeReference = BoundEvent{EventID: outcomeEvidence.EvidenceID,
			RecordSHA256: outcomeRecords[0].RecordHash}
	}
	observation := Observation{
		SchemaVersion: ObservationSchema, StudyID: request.StudyID, TaskID: request.TaskID,
		Condition: task.Condition,
		Execution: executionBinding,
		Outcome:   outcomeEvidence.Outcome, OutcomeAuthority: AuthorityBuiltinAcceptance,
		OutcomeEvidence: &outcomeReference,
		Reporter:        request.Reporter, Reason: strings.TrimSpace(request.Reason), ObservedAt: observedAt,
		Privacy: PrivacyLocalOnly,
	}
	observation.ObservationID, err = observationIdentity(observation)
	if err != nil {
		return result, closeAppender(appender, fmt.Errorf("record outcome evidence without observation: %w", err))
	}
	if _, conflict := state.Records[observation.ObservationID]; conflict {
		return result, closeAppender(appender,
			errors.New("outcome evidence was recorded but observation identity conflicts with an existing event"))
	}
	event, err := observationEvent(store, plan.Agent, observation)
	if err != nil {
		return result, closeAppender(appender, fmt.Errorf("record outcome evidence without observation: %w", err))
	}
	records, err := appender.AppendBatch([]ledger.Event{event})
	if err != nil {
		return result, closeAppender(appender, fmt.Errorf("record outcome evidence without observation: %w", err))
	}
	if err := closeAppender(appender, nil); err != nil {
		return result, err
	}
	result.Observation = observation
	result.Outcome = &outcomeEvidence
	result.Event = BoundEvent{EventID: observation.ObservationID, RecordSHA256: records[0].RecordHash}
	return result, nil
}

func ResolvePlan(store *ledger.Store, studyID string) (Plan, error) {
	state := replay(store)
	if len(state.Issues) != 0 {
		return Plan{}, fmt.Errorf("longitudinal study verification failed: %s", strings.Join(state.Issues, "; "))
	}
	plan, exists := state.Plans[studyID]
	if !exists {
		return Plan{}, errors.New("verified longitudinal study is unavailable")
	}
	return plan, nil
}

func Verify(store *ledger.Store) VerificationReport {
	report := VerificationReport{
		SchemaVersion: VerificationSchema, Issues: []string{}, Privacy: PrivacyLocalOnly,
	}
	state := replay(store)
	report.StudiesChecked = len(state.Plans)
	for _, observations := range state.Observations {
		report.ObservationsChecked += len(observations)
	}
	report.Issues = append(report.Issues, state.Issues...)
	sort.Strings(report.Issues)
	return report
}

func BuildReport(store *ledger.Store, studyID string, now func() time.Time) (Report, error) {
	report := Report{SchemaVersion: ReportSchema, StudyID: studyID, Reasons: []string{},
		MissingTaskIDs: []string{}, ClaimBoundary: ClaimBoundary, Privacy: PrivacyLocalOnly}
	if now == nil {
		now = time.Now
	}
	report.GeneratedAt = now().UTC()
	state := replay(store)
	if len(state.Issues) != 0 {
		return report, fmt.Errorf("longitudinal study verification failed: %s", strings.Join(state.Issues, "; "))
	}
	plan, exists := state.Plans[studyID]
	if !exists {
		return report, errors.New("verified longitudinal study is unavailable")
	}
	observations := state.Observations[studyID]
	report.PlannedTasks = len(plan.Tasks)
	report.ObservedTasks = len(observations)
	report.MinimumElapsedDays = plan.MinimumElapsedDays
	latest := plan.CreatedAt
	for _, task := range plan.Tasks {
		summary := &report.Baseline
		if task.Condition == ConditionMemory {
			summary = &report.Memory
		}
		summary.Planned++
		observation, observed := observations[task.TaskID]
		if !observed {
			report.MissingTaskIDs = append(report.MissingTaskIDs, task.TaskID)
			continue
		}
		summary.Observed++
		if observation.Outcome == OutcomeSuccess {
			summary.Successes++
		} else {
			summary.Failures++
		}
		if observation.ObservedAt.After(latest) {
			latest = observation.ObservedAt
		}
		if observation.OutcomeAuthority == AuthorityBuiltinAcceptance {
			report.BuiltinEvaluatorOutcomes++
		}
		if observation.Reporter.Kind == "synthetic_test" {
			report.SyntheticObservations++
		}
	}
	if latest.After(plan.CreatedAt) {
		report.ElapsedDays = int(latest.Sub(plan.CreatedAt) / (24 * time.Hour))
	}
	report.Baseline.SuccessRateBasisPoints = successRate(report.Baseline)
	report.Memory.SuccessRateBasisPoints = successRate(report.Memory)
	report.SuccessDeltaBasisPoints = report.Memory.SuccessRateBasisPoints - report.Baseline.SuccessRateBasisPoints
	report.Status = "not_evaluable"
	if report.ObservedTasks != report.PlannedTasks {
		report.Reasons = append(report.Reasons, "prospective population is incomplete")
	}
	if report.ElapsedDays < report.MinimumElapsedDays {
		report.Reasons = append(report.Reasons, "minimum elapsed period has not been reached")
	}
	if report.BuiltinEvaluatorOutcomes != report.ObservedTasks {
		report.Reasons = append(report.Reasons, "one or more outcomes lack built-in acceptance replay")
	}
	if report.SyntheticObservations != 0 {
		report.Reasons = append(report.Reasons, "synthetic observations cannot support an evaluable study")
	}
	if report.Baseline.Observed < 2 || report.Memory.Observed < 2 {
		report.Reasons = append(report.Reasons, "at least two completed observations per arm are required")
	}
	if len(report.Reasons) == 0 {
		report.Status = "descriptive_signal"
	}
	return report, nil
}

func ListReports(store *ledger.Store, now func() time.Time) ([]Report, error) {
	state := replay(store)
	if len(state.Issues) != 0 {
		return nil, fmt.Errorf("longitudinal study verification failed: %s", strings.Join(state.Issues, "; "))
	}
	ids := make([]string, 0, len(state.Plans))
	for studyID := range state.Plans {
		ids = append(ids, studyID)
	}
	sort.Strings(ids)
	reports := make([]Report, 0, len(ids))
	for _, studyID := range ids {
		report, err := BuildReport(store, studyID, now)
		if err != nil {
			return nil, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func planEvent(store *ledger.Store, plan Plan) (ledger.Event, error) {
	data, err := canonicalJSON(plan)
	if err != nil {
		return ledger.Event{}, err
	}
	payload := ledger.InlinePayload("json", PlanMediaType, string(data))
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: plan.StudyID, Kind: ledger.KindEvaluationTrialPlan,
		ObservedAt: plan.CreatedAt, RecordedAt: plan.CreatedAt,
		Source: ledger.Source{Agent: plan.Agent, Adapter: "agentmem-study", AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), ThreadID: plan.StudyID, SourceEventID: plan.StudyID},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: PrivacyLocalOnly},
	}, nil
}

func observationEvent(store *ledger.Store, agent ledger.Agent, observation Observation) (ledger.Event, error) {
	data, err := canonicalJSON(observation)
	if err != nil {
		return ledger.Event{}, err
	}
	payload := ledger.InlinePayload("json", ObservationMediaType, string(data))
	parents := []string{observation.StudyID, observation.Execution.EventID}
	if observation.OutcomeEvidence != nil {
		parents = append(parents, observation.OutcomeEvidence.EventID)
	}
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: observation.ObservationID,
		Kind: ledger.KindEvaluationAttestation, ObservedAt: observation.ObservedAt,
		RecordedAt: observation.ObservedAt,
		Source: ledger.Source{Agent: agent, Adapter: "agentmem-study", AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), ThreadID: observation.StudyID,
			SourceEventID: observation.ObservationID, SourceCursor: observation.TaskID},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: parents},
		Privacy:   ledger.Privacy{Classification: PrivacyLocalOnly},
	}, nil
}

func plannedTask(plan Plan, taskID string) (PlannedTask, bool) {
	for _, task := range plan.Tasks {
		if task.TaskID == taskID {
			return task, true
		}
	}
	return PlannedTask{}, false
}

func successRate(summary ArmSummary) int {
	if summary.Observed == 0 {
		return 0
	}
	return summary.Successes * 10000 / summary.Observed
}

func closeAppender(appender *ledger.Appender, cause error) error {
	closeErr := appender.Close()
	if cause != nil && closeErr != nil {
		return errors.Join(cause, closeErr)
	}
	if cause != nil {
		return cause
	}
	return closeErr
}

func evaluateOutcome(store *ledger.Store, plan Plan, task PlannedTask, execution agentbridge.VerifiedExecution,
	binding BoundEvent, at time.Time) (OutcomeEvidence, error) {
	evidence := OutcomeEvidence{SchemaVersion: OutcomeEvidenceSchema, StudyID: plan.StudyID,
		TaskID: task.TaskID, Execution: binding, Evaluator: "builtin-exact-sha256/v1",
		EvaluatedAt: at, Privacy: PrivacyLocalOnly, AssertionResults: []AssertionResult{}}
	acceptanceSHA, err := acceptanceSHA256(task.Acceptance)
	if err != nil {
		return evidence, err
	}
	evidence.AcceptanceSHA256 = acceptanceSHA
	allPassed := true
	for _, assertion := range task.Acceptance.Assertions {
		item := AssertionResult{Kind: assertion.Kind, Path: assertion.Path,
			ExpectedSHA256: assertion.ExpectedSHA256, Status: "failed"}
		switch assertion.Kind {
		case "agent_message_sha256":
			if execution.Receipt.AgentMessageBlob != nil {
				reference := *execution.Receipt.AgentMessageBlob
				// ResolveVerifiedExecution already replayed the complete raw JSONL
				// and exact message blob. Reuse that whole-blob binding instead of
				// reading or hashing an arbitrary prefix.
				item.CapturedBlob = &reference
				item.ActualSHA256 = reference.SHA256
				if item.ActualSHA256 == item.ExpectedSHA256 {
					item.Status = "passed"
				}
			}
		}
		if item.Status != "passed" {
			allPassed = false
		}
		evidence.AssertionResults = append(evidence.AssertionResults, item)
	}
	evidence.Outcome = OutcomeFailure
	if allPassed {
		evidence.Outcome = OutcomeSuccess
	}
	evidence.EvidenceID, err = outcomeEvidenceIdentity(evidence)
	return evidence, err
}

func outcomeEvidenceIdentity(evidence OutcomeEvidence) (string, error) {
	copyEvidence := evidence
	copyEvidence.EvidenceID = ""
	data, err := canonicalJSON(copyEvidence)
	if err != nil {
		return "", err
	}
	return "study-outcome-" + digest(data), nil
}

func outcomeEvidenceEvent(store *ledger.Store, agent ledger.Agent, evidence OutcomeEvidence) (ledger.Event, error) {
	data, err := canonicalJSON(evidence)
	if err != nil {
		return ledger.Event{}, err
	}
	payload := ledger.InlinePayload("json", OutcomeEvidenceMediaType, string(data))
	return ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: evidence.EvidenceID,
		Kind: ledger.KindToolResult, ObservedAt: evidence.EvaluatedAt, RecordedAt: evidence.EvaluatedAt,
		Source: ledger.Source{Agent: agent, Adapter: "agentmem-study", AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), ThreadID: evidence.StudyID, SourceEventID: evidence.EvidenceID,
			SourceCursor: evidence.TaskID}, Payload: &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: []string{evidence.StudyID, evidence.Execution.EventID}},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly}}, nil
}
