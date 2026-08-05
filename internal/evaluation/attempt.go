package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const taskAttemptAdapterVersion = "task-attempt/v1alpha1"

type indexedRecord struct {
	Record ledger.Record
	Index  int
}

func DecodeTaskAttemptRequest(reader io.Reader) (TaskAttemptRequest, error) {
	if reader == nil {
		return TaskAttemptRequest{}, errors.New("task attempt request reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request TaskAttemptRequest
	if err := decoder.Decode(&request); err != nil {
		return TaskAttemptRequest{}, fmt.Errorf("decode task attempt request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return TaskAttemptRequest{}, err
	}
	if err := validateTaskAttemptRequest(request); err != nil {
		return TaskAttemptRequest{}, err
	}
	return request, nil
}

func RecordTaskAttempt(store *ledger.Store, request TaskAttemptRequest,
	now func() time.Time) (TaskAttemptResult, error) {
	result := TaskAttemptResult{SchemaVersion: TaskAttemptResultSchema, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	request.MemoryReferences = append([]retrieval.MemoryReference{}, request.MemoryReferences...)
	if err := validateTaskAttemptRequest(request); err != nil {
		return result, err
	}
	if request.Condition == TaskConditionMemory {
		if verification := retrieval.Verify(store); len(verification.Issues) != 0 {
			return result, fmt.Errorf("memory receipts do not verify: %s",
				strings.Join(verification.Issues, "; "))
		}
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return result, err
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("encode task attempt request: %w", err)
	}
	requestSHA := sha256Hex(requestData)
	verdictRecord, ok := records[request.Oracle.VerdictEventID]
	if !ok {
		return result, errors.New("task attempt verdict event is unavailable")
	}
	receiptID := taskAttemptReceiptID(request, requestSHA, verdictRecord.Record.RecordHash)
	if existing, exists := records[receiptID]; exists {
		verification := verifyTaskAttemptRecord(store, existing.Record, records, ordered)
		if len(verification.Issues) != 0 {
			return result, errors.New("task attempt receipt id collision")
		}
		receipt, decodeErr := decodeTaskAttemptReceipt(store, existing.Record.Event)
		if decodeErr != nil || receipt.RequestSHA256 != requestSHA {
			return result, errors.New("task attempt receipt id collision")
		}
		return TaskAttemptResult{SchemaVersion: TaskAttemptResultSchema, Receipt: receipt,
			EventID: receiptID, RecordHash: existing.Record.RecordHash, Reused: true,
			Privacy: "local_only"}, nil
	}
	if now == nil {
		now = time.Now
	}
	receipt, parents, err := deriveTaskAttempt(store, request, requestSHA, receiptID,
		now().UTC(), records, ordered)
	if err != nil {
		return result, err
	}
	data, err := marshalIndented(receipt)
	if err != nil {
		return result, fmt.Errorf("encode task attempt receipt: %w", err)
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	start := records[request.WindowStartEventID].Record.Event
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       receipt.ReceiptID,
		Kind:          ledger.KindTaskAttempt,
		ObservedAt:    records[request.WindowEndEventID].Record.Event.ObservedAt.UTC(),
		RecordedAt:    receipt.RecordedAt,
		Source: ledger.Source{
			Agent: request.Agent, Adapter: evaluationAdapterName,
			AdapterVersion: taskAttemptAdapterVersion, DeviceID: start.Source.DeviceID, OS: start.Source.OS,
			ThreadID: start.Source.ThreadID, SessionID: start.Source.SessionID,
			SourceEventID: request.AttemptID, SourceCursor: "task-attempt:" + request.AttemptID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: parents},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	record, err := store.Append(event)
	if err != nil {
		return result, fmt.Errorf("append task attempt receipt: %w", err)
	}
	return TaskAttemptResult{SchemaVersion: TaskAttemptResultSchema, Receipt: receipt,
		EventID: receipt.ReceiptID, RecordHash: record.RecordHash, Privacy: "local_only"}, nil
}

func VerifyTaskAttempt(store *ledger.Store, receiptID string) TaskAttemptVerification {
	report := TaskAttemptVerification{SchemaVersion: TaskAttemptVerificationSchema,
		ReceiptID: receiptID, Issues: []string{}, Privacy: "local_only"}
	if store == nil || !strings.HasPrefix(receiptID, "task-attempt-") {
		report.Issues = append(report.Issues, "store and task attempt receipt id are required")
		return report
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	record, exists := records[receiptID]
	if !exists {
		report.Issues = append(report.Issues, "task attempt receipt is unavailable")
		return report
	}
	receipt, decodeErr := decodeTaskAttemptReceipt(store, record.Record.Event)
	if decodeErr != nil {
		report.Issues = append(report.Issues, decodeErr.Error())
		return report
	}
	if receipt.Condition == TaskConditionMemory {
		if verification := retrieval.Verify(store); len(verification.Issues) != 0 {
			report.Issues = append(report.Issues, "memory receipts do not verify: "+strings.Join(verification.Issues, "; "))
			return report
		}
	}
	return verifyTaskAttemptRecord(store, record.Record, records, ordered)
}

func verifyTaskAttemptRecord(store *ledger.Store, record ledger.Record,
	records map[string]indexedRecord, ordered []ledger.Record) TaskAttemptVerification {
	report := TaskAttemptVerification{SchemaVersion: TaskAttemptVerificationSchema,
		ReceiptID: record.Event.EventID, Issues: []string{}, Privacy: "local_only"}
	receipt, err := decodeTaskAttemptReceipt(store, record.Event)
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	request := taskAttemptRequestFromReceipt(receipt)
	requestData, err := json.Marshal(request)
	if err != nil || sha256Hex(requestData) != receipt.RequestSHA256 {
		report.Issues = append(report.Issues, "task attempt request binding is invalid")
		return report
	}
	verdict, exists := records[request.Oracle.VerdictEventID]
	if !exists || taskAttemptReceiptID(request, receipt.RequestSHA256,
		verdict.Record.RecordHash) != receipt.ReceiptID {
		report.Issues = append(report.Issues, "task attempt identity is invalid")
		return report
	}
	expected, parents, deriveErr := deriveTaskAttempt(store, request, receipt.RequestSHA256,
		receipt.ReceiptID, receipt.RecordedAt, records, ordered)
	if deriveErr != nil {
		report.Issues = append(report.Issues, deriveErr.Error())
		return report
	}
	expectedData, _ := json.Marshal(expected)
	actualData, _ := json.Marshal(receipt)
	if !bytes.Equal(expectedData, actualData) {
		report.Issues = append(report.Issues, "task attempt receipt does not match replayed evidence")
	}
	if record.Event.Kind != ledger.KindTaskAttempt || record.Event.Source.Agent != receipt.Agent ||
		!sameTaskAttemptEventSource(record.Event.Source,
			records[receipt.WindowStart.EventID].Record.Event.Source, receipt.AttemptID) ||
		!record.Event.ObservedAt.UTC().Equal(records[receipt.WindowEnd.EventID].Record.Event.ObservedAt.UTC()) ||
		record.Event.Completeness.Status != ledger.CompletenessComplete ||
		record.Event.Privacy.Classification != "local_only" ||
		!record.Event.RecordedAt.UTC().Equal(receipt.RecordedAt) ||
		record.Event.Causality == nil ||
		!sameStrings(record.Event.Causality.ParentEventIDs, parents) {
		report.Issues = append(report.Issues, "task attempt event envelope is invalid")
	}
	report.EventsChecked = len(parents)
	report.Issues = uniqueSorted(report.Issues)
	return report
}

func deriveTaskAttempt(store *ledger.Store, request TaskAttemptRequest, requestSHA,
	receiptID string, recordedAt time.Time, records map[string]indexedRecord,
	ordered []ledger.Record) (TaskAttemptReceipt, []string, error) {
	if err := validateTaskAttemptRequest(request); err != nil {
		return TaskAttemptReceipt{}, nil, err
	}
	required := []string{request.WindowStartEventID, request.WindowEndEventID,
		request.Oracle.VerdictEventID}
	if request.Condition == TaskConditionMemory {
		required = append(required, request.RetrievalReceiptID, request.InjectionID, request.AdoptionID)
	}
	for _, id := range required {
		if _, exists := records[id]; !exists {
			return TaskAttemptReceipt{}, nil, fmt.Errorf("task attempt evidence %q is unavailable", id)
		}
	}
	start := records[request.WindowStartEventID]
	end := records[request.WindowEndEventID]
	verdictRecord := records[request.Oracle.VerdictEventID]
	if start.Index >= end.Index || end.Index >= verdictRecord.Index {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt window and verdict order is invalid")
	}
	if !sameTaskContext(start.Record.Event, end.Record.Event, request.Agent) ||
		!sameTaskContext(start.Record.Event, verdictRecord.Record.Event, request.Agent) {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt boundaries or verdict belong to another context")
	}
	if err := completeEvidenceEvent(start.Record.Event); err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("window start: %w", err)
	}
	if err := completeEvidenceEvent(end.Record.Event); err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("window end: %w", err)
	}
	if verdictRecord.Record.Event.Kind != ledger.KindToolResult {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt verdict must be a tool-result event")
	}
	if err := completeEvidenceEvent(verdictRecord.Record.Event); err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("task attempt verdict: %w", err)
	}
	contractData, err := eventPayload(store, start.Record.Event)
	if err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("read task attempt contract: %w", err)
	}
	var contract TaskAttemptContract
	if start.Record.Event.Kind != ledger.KindSystemEvent ||
		decodeStrictEvaluationJSON(contractData, &contract) != nil ||
		!taskAttemptContractMatches(contract, request) {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt start does not contain the declared task contract")
	}
	if start.Record.Event.Source.Adapter != request.Oracle.ID ||
		start.Record.Event.Source.AdapterVersion != request.Oracle.Version ||
		verdictRecord.Record.Event.Source.Adapter != request.Oracle.ID ||
		verdictRecord.Record.Event.Source.AdapterVersion != request.Oracle.Version {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt oracle does not match contract and verdict sources")
	}
	verdictData, err := eventPayload(store, verdictRecord.Record.Event)
	if err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("read task attempt verdict: %w", err)
	}
	var verdict TaskAttemptVerdict
	if err := decodeStrictEvaluationJSON(verdictData, &verdict); err != nil {
		return TaskAttemptReceipt{}, nil, fmt.Errorf("decode task attempt verdict: %w", err)
	}
	if err := validateTaskAttemptVerdict(verdict, request); err != nil {
		return TaskAttemptReceipt{}, nil, err
	}
	if !isCausalDescendant(records, request.Oracle.VerdictEventID, request.WindowEndEventID) {
		return TaskAttemptReceipt{}, nil, errors.New("task attempt verdict is not causally bound to the window end")
	}

	resultRecords := map[string]indexedRecord{}
	userRecords := map[string]indexedRecord{}
	retrievals := []string{}
	injections := []string{}
	for index := start.Index; index <= end.Index; index++ {
		record := ordered[index-1]
		event := record.Event
		if !sameTaskContext(start.Record.Event, event, request.Agent) {
			continue
		}
		if event.Kind == ledger.KindGap || event.Completeness.Status != ledger.CompletenessComplete {
			return TaskAttemptReceipt{}, nil, errors.New("task attempt window is incomplete")
		}
		switch event.Kind {
		case ledger.KindToolResult, ledger.KindFileChange:
			if err := completeEvidenceEvent(event); err != nil {
				return TaskAttemptReceipt{}, nil, errors.New("task attempt result evidence is incomplete")
			}
			resultRecords[event.EventID] = indexedRecord{Record: record, Index: index}
		case ledger.KindUserMessage:
			if err := completeEvidenceEvent(event); err != nil {
				return TaskAttemptReceipt{}, nil, errors.New("task attempt user-message evidence is incomplete")
			}
			userRecords[event.EventID] = indexedRecord{Record: record, Index: index}
		case ledger.KindRetrieval:
			retrievals = append(retrievals, event.EventID)
		case ledger.KindInjection:
			injections = append(injections, event.EventID)
		}
	}
	if err := validateVerdictCoverage(verdict, resultRecords, userRecords); err != nil {
		return TaskAttemptReceipt{}, nil, err
	}

	causalParent := request.WindowStartEventID
	causalEvidenceIDs := []string{request.WindowStartEventID}
	for id := range resultRecords {
		causalEvidenceIDs = append(causalEvidenceIDs, id)
	}
	for id := range userRecords {
		causalEvidenceIDs = append(causalEvidenceIDs, id)
	}
	memoryAncestors := causalAncestorKinds(records, causalEvidenceIDs,
		ledger.KindRetrieval, ledger.KindInjection)
	if request.Condition == TaskConditionBaseline {
		if len(retrievals) != 0 || len(injections) != 0 {
			return TaskAttemptReceipt{}, nil, errors.New("baseline window contains memory retrieval or injection")
		}
		if len(memoryAncestors) != 0 {
			return TaskAttemptReceipt{}, nil, errors.New("baseline task evidence descends from prior memory exposure")
		}
	} else {
		if len(retrievals) != 1 || retrievals[0] != request.RetrievalReceiptID ||
			len(injections) != 1 || injections[0] != request.InjectionID {
			return TaskAttemptReceipt{}, nil, errors.New("memory window does not contain exactly the declared retrieval and injection")
		}
		if err := validateMemoryAttempt(store, request, records, start.Index, end.Index); err != nil {
			return TaskAttemptReceipt{}, nil, err
		}
		for id, kind := range memoryAncestors {
			if (kind == ledger.KindRetrieval && id != request.RetrievalReceiptID) ||
				(kind == ledger.KindInjection && id != request.InjectionID) {
				return TaskAttemptReceipt{}, nil, errors.New("memory task evidence contains undeclared memory exposure")
			}
		}
		causalParent = request.InjectionID
	}
	for id := range resultRecords {
		if !isCausalDescendant(records, id, causalParent) {
			return TaskAttemptReceipt{}, nil, fmt.Errorf("result event %q is not causally bound to the task condition", id)
		}
	}
	for id := range userRecords {
		if !isCausalDescendant(records, id, causalParent) {
			return TaskAttemptReceipt{}, nil, fmt.Errorf("user message %q is not causally bound to the task condition", id)
		}
	}

	measurement := TrialMeasurement{Success: verdict.Verdict == TaskVerdictPass,
		Score: verdict.Score, TotalTokens: verdict.TotalTokens}
	for _, item := range verdict.ResultEvents {
		if item.Label == ResultLabelError {
			measurement.Errors++
		}
	}
	for _, item := range verdict.UserMessages {
		if item.Label == UserMessageCorrection {
			measurement.UserCorrections++
		}
	}
	resultRefs := boundReferences(resultRecords)
	userRefs := boundReferences(userRecords)
	parents := []string{request.WindowStartEventID, request.WindowEndEventID,
		request.Oracle.VerdictEventID}
	for _, ref := range resultRefs {
		parents = append(parents, ref.EventID)
	}
	for _, ref := range userRefs {
		parents = append(parents, ref.EventID)
	}
	if request.Condition == TaskConditionMemory {
		parents = append(parents, request.RetrievalReceiptID, request.InjectionID, request.AdoptionID)
	}
	parents = sortedUniqueStrings(parents)
	receipt := TaskAttemptReceipt{
		SchemaVersion: TaskAttemptReceiptSchema, ReceiptID: receiptID, RecordedAt: recordedAt.UTC(),
		TaskID: request.TaskID, AttemptID: request.AttemptID, Agent: request.Agent,
		SemanticKeySHA256: request.SemanticKeySHA256, TaskSpecSHA256: request.TaskSpecSHA256,
		AcceptanceCriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ExecutionConfigSHA256:    request.ExecutionConfigSHA256, Condition: request.Condition,
		WindowStart: boundReference(start.Record), WindowEnd: boundReference(end.Record),
		RetrievalReceiptID: request.RetrievalReceiptID, InjectionID: request.InjectionID,
		AdoptionID: request.AdoptionID, MemoryReferences: append([]retrieval.MemoryReference{}, request.MemoryReferences...),
		Oracle: request.Oracle, Verdict: boundReference(verdictRecord.Record),
		ResultEvents: resultRefs, UserMessages: userRefs, Measurement: measurement,
		BindingStatus: "causal_complete", Authority: "measurement_only",
		RequestSHA256: requestSHA, Privacy: "local_only",
	}
	return receipt, parents, nil
}

func validateTaskAttemptRequest(request TaskAttemptRequest) error {
	if request.SchemaVersion != TaskAttemptRequestSchema {
		return fmt.Errorf("unsupported task attempt request schema %q", request.SchemaVersion)
	}
	if !safeIdentifier(request.TaskID) || !safeIdentifier(request.AttemptID) ||
		!validAgent(request.Agent) || request.Agent == ledger.AgentUnknown ||
		!validSHA256(request.SemanticKeySHA256) || !validSHA256(request.TaskSpecSHA256) ||
		!validSHA256(request.AcceptanceCriteriaSHA256) || !validSHA256(request.ExecutionConfigSHA256) {
		return errors.New("task attempt identity and content hashes are required")
	}
	if strings.TrimSpace(request.WindowStartEventID) == "" ||
		strings.TrimSpace(request.WindowEndEventID) == "" || request.WindowStartEventID == request.WindowEndEventID {
		return errors.New("task attempt requires distinct window boundaries")
	}
	if request.Oracle.Kind != "harness" && request.Oracle.Kind != "human" {
		return errors.New("task attempt oracle must be a harness or human claim")
	}
	if strings.TrimSpace(request.Oracle.ID) == "" || strings.TrimSpace(request.Oracle.Version) == "" ||
		strings.TrimSpace(request.Oracle.VerdictEventID) == "" {
		return errors.New("task attempt oracle identity, version, and verdict event are required")
	}
	if request.Privacy != "local_only" {
		return errors.New("task attempt privacy must be local_only")
	}
	if request.MemoryReferences == nil || !sortedUniqueMemoryReferences(request.MemoryReferences) {
		return errors.New("task attempt memory references must be sorted and unique")
	}
	switch request.Condition {
	case TaskConditionBaseline:
		if request.RetrievalReceiptID != "" || request.InjectionID != "" || request.AdoptionID != "" ||
			len(request.MemoryReferences) != 0 {
			return errors.New("baseline task attempt cannot declare memory exposure")
		}
	case TaskConditionMemory:
		if !strings.HasPrefix(request.RetrievalReceiptID, "retrieval-") ||
			!strings.HasPrefix(request.InjectionID, "injection-") ||
			!strings.HasPrefix(request.AdoptionID, "adoption-") || len(request.MemoryReferences) == 0 {
			return errors.New("memory task attempt requires retrieval, injection, adoption, and memory references")
		}
	default:
		return errors.New("task attempt condition is invalid")
	}
	return nil
}

func validateTaskAttemptVerdict(verdict TaskAttemptVerdict, request TaskAttemptRequest) error {
	if verdict.SchemaVersion != TaskAttemptVerdictSchema || verdict.TaskID != request.TaskID ||
		verdict.AttemptID != request.AttemptID || verdict.TaskSpecSHA256 != request.TaskSpecSHA256 ||
		verdict.CriteriaSHA256 != request.AcceptanceCriteriaSHA256 || verdict.ConfigSHA256 != request.ExecutionConfigSHA256 ||
		(verdict.Verdict != TaskVerdictPass && verdict.Verdict != TaskVerdictFail) ||
		verdict.Score < 0 || verdict.Score > 1 || verdict.TotalTokens < 0 || verdict.Privacy != "local_only" {
		return errors.New("task attempt verdict does not match the declared task contract")
	}
	if len(verdict.ResultEvents) == 0 || verdict.UserMessages == nil ||
		!sortedUniqueLabeledResults(verdict.ResultEvents) ||
		!sortedUniqueLabeledUserMessages(verdict.UserMessages) {
		return errors.New("task attempt verdict event labels must provide sorted exact coverage")
	}
	return nil
}

func validateVerdictCoverage(verdict TaskAttemptVerdict, results, users map[string]indexedRecord) error {
	if len(verdict.ResultEvents) != len(results) || len(verdict.UserMessages) != len(users) {
		return errors.New("task attempt verdict does not cover the complete observed window")
	}
	for _, item := range verdict.ResultEvents {
		if _, exists := results[item.EventID]; !exists {
			return fmt.Errorf("task attempt verdict labels unrelated result %q", item.EventID)
		}
	}
	for _, item := range verdict.UserMessages {
		if _, exists := users[item.EventID]; !exists {
			return fmt.Errorf("task attempt verdict labels unrelated user message %q", item.EventID)
		}
	}
	return nil
}

func validateMemoryAttempt(store *ledger.Store, request TaskAttemptRequest,
	records map[string]indexedRecord, startIndex, endIndex int) error {
	retrievalRecord := records[request.RetrievalReceiptID]
	injectionRecord := records[request.InjectionID]
	adoptionRecord := records[request.AdoptionID]
	if retrievalRecord.Index <= startIndex || retrievalRecord.Index >= injectionRecord.Index ||
		injectionRecord.Index > endIndex || adoptionRecord.Index <= injectionRecord.Index {
		return errors.New("memory receipt order is invalid for the task window")
	}
	if !sameTaskContext(records[request.WindowStartEventID].Record.Event,
		retrievalRecord.Record.Event, request.Agent) ||
		!sameTaskContext(records[request.WindowStartEventID].Record.Event,
			injectionRecord.Record.Event, request.Agent) ||
		!sameTaskContext(records[request.WindowStartEventID].Record.Event,
			adoptionRecord.Record.Event, request.Agent) {
		return errors.New("memory receipts belong to another task context")
	}
	if !isCausalDescendant(records, request.InjectionID, request.RetrievalReceiptID) ||
		!isCausalDescendant(records, request.AdoptionID, request.InjectionID) {
		return errors.New("memory receipt causality is incomplete")
	}
	retrievalData, err := eventPayload(store, retrievalRecord.Record.Event)
	if err != nil {
		return errors.New("task attempt retrieval receipt is invalid")
	}
	var retrievalReceipt retrieval.Receipt
	if decodeStrictEvaluationJSON(retrievalData, &retrievalReceipt) != nil ||
		retrievalReceipt.ReceiptID != request.RetrievalReceiptID || retrievalReceipt.Result.Status != "completed" {
		return errors.New("task attempt retrieval receipt is invalid")
	}
	injectionData, err := eventPayload(store, injectionRecord.Record.Event)
	if err != nil {
		return errors.New("task attempt injection receipt is invalid")
	}
	var injection retrieval.InjectionReceipt
	if decodeStrictEvaluationJSON(injectionData, &injection) != nil || injection.InjectionID != request.InjectionID ||
		injection.RetrievalReceiptID != request.RetrievalReceiptID ||
		!sameMemoryReferences(injection.Memories, request.MemoryReferences) {
		return errors.New("task attempt injection does not contain the declared memories")
	}
	adoptionData, err := eventPayload(store, adoptionRecord.Record.Event)
	if err != nil {
		return errors.New("task attempt adoption receipt is invalid")
	}
	var adoption retrieval.AdoptionReceipt
	if decodeStrictEvaluationJSON(adoptionData, &adoption) != nil || adoption.AdoptionID != request.AdoptionID ||
		adoption.RetrievalReceiptID != request.RetrievalReceiptID || adoption.InjectionID != request.InjectionID {
		return errors.New("task attempt adoption receipt is invalid")
	}
	selected := map[string]struct{}{}
	for _, match := range retrievalReceipt.Result.Selected {
		selected[memoryReferenceKey(match.MemoryReference)] = struct{}{}
	}
	adopted := map[string]struct{}{}
	for _, item := range adoption.Items {
		if item.Adoption == retrieval.AdoptionAdopted {
			adopted[memoryReferenceKey(item.MemoryReference)] = struct{}{}
		}
	}
	for _, reference := range request.MemoryReferences {
		key := memoryReferenceKey(reference)
		if _, ok := selected[key]; !ok {
			return errors.New("task attempt memory was not selected by the retrieval")
		}
		if _, ok := adopted[key]; !ok {
			return errors.New("task attempt memory lacks an adopted-action claim")
		}
		semanticKey, err := portable.ResolveSemanticKey(store, reference.MemoryID, reference.RevisionID)
		if err != nil || semanticKey != request.SemanticKeySHA256 {
			return errors.New("task attempt memory does not match the declared semantic key")
		}
	}
	return nil
}

func loadIndexedRecords(store *ledger.Store) (map[string]indexedRecord, []ledger.Record, error) {
	records := map[string]indexedRecord{}
	ordered := []ledger.Record{}
	if verification := store.Verify(); len(verification.Issues) != 0 {
		return nil, nil, errors.New("evidence ledger does not verify")
	}
	err := store.VisitRecords(func(record ledger.Record) error {
		if _, duplicate := records[record.Event.EventID]; duplicate {
			return fmt.Errorf("duplicate evidence event id %q", record.Event.EventID)
		}
		ordered = append(ordered, record)
		records[record.Event.EventID] = indexedRecord{Record: record, Index: len(ordered)}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read task attempt evidence: %w", err)
	}
	return records, ordered, nil
}

func NewTaskAttemptContract(request TaskAttemptRequest) TaskAttemptContract {
	return TaskAttemptContract{
		SchemaVersion: TaskAttemptContractSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Agent: request.Agent, SemanticKeySHA256: request.SemanticKeySHA256,
		TaskSpecSHA256: request.TaskSpecSHA256, AcceptanceCriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ExecutionConfigSHA256: request.ExecutionConfigSHA256, Condition: request.Condition,
		WindowEndEventID: request.WindowEndEventID, Oracle: request.Oracle, Privacy: request.Privacy,
	}
}

func taskAttemptContractMatches(contract TaskAttemptContract, request TaskAttemptRequest) bool {
	return contract == NewTaskAttemptContract(request) && contract.SchemaVersion == TaskAttemptContractSchema &&
		contract.Privacy == "local_only"
}

func sameTaskAttemptEventSource(observed, start ledger.Source, attemptID string) bool {
	expected := ledger.Source{
		Agent: start.Agent, Adapter: evaluationAdapterName, AdapterVersion: taskAttemptAdapterVersion,
		DeviceID: start.DeviceID, OS: start.OS, ThreadID: start.ThreadID, SessionID: start.SessionID,
		SourceEventID: attemptID, SourceCursor: "task-attempt:" + attemptID,
	}
	return observed == expected
}

func decodeTaskAttemptReceipt(store *ledger.Store, event ledger.Event) (TaskAttemptReceipt, error) {
	data, err := eventPayload(store, event)
	if err != nil {
		return TaskAttemptReceipt{}, fmt.Errorf("read task attempt receipt: %w", err)
	}
	var receipt TaskAttemptReceipt
	if err := decodeStrictEvaluationJSON(data, &receipt); err != nil {
		return TaskAttemptReceipt{}, fmt.Errorf("decode task attempt receipt: %w", err)
	}
	if receipt.SchemaVersion != TaskAttemptReceiptSchema || receipt.ReceiptID != event.EventID ||
		receipt.BindingStatus != "causal_complete" || receipt.Authority != "measurement_only" ||
		receipt.Privacy != "local_only" {
		return TaskAttemptReceipt{}, errors.New("task attempt receipt envelope is invalid")
	}
	return receipt, nil
}

func taskAttemptRequestFromReceipt(receipt TaskAttemptReceipt) TaskAttemptRequest {
	return TaskAttemptRequest{
		SchemaVersion: TaskAttemptRequestSchema, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID,
		Agent: receipt.Agent, SemanticKeySHA256: receipt.SemanticKeySHA256,
		TaskSpecSHA256: receipt.TaskSpecSHA256, AcceptanceCriteriaSHA256: receipt.AcceptanceCriteriaSHA256,
		ExecutionConfigSHA256: receipt.ExecutionConfigSHA256, Condition: receipt.Condition,
		WindowStartEventID: receipt.WindowStart.EventID, WindowEndEventID: receipt.WindowEnd.EventID,
		RetrievalReceiptID: receipt.RetrievalReceiptID, InjectionID: receipt.InjectionID,
		AdoptionID: receipt.AdoptionID, MemoryReferences: append([]retrieval.MemoryReference{}, receipt.MemoryReferences...),
		Oracle: receipt.Oracle, Privacy: receipt.Privacy,
	}
}

func taskAttemptReceiptID(request TaskAttemptRequest, requestSHA, verdictRecordHash string) string {
	return adapterjsonl.DeterministicID("task-attempt", request.AttemptID, requestSHA, verdictRecordHash)
}

func boundReference(record ledger.Record) BoundEventReference {
	ref := BoundEventReference{EventID: record.Event.EventID, RecordHash: record.RecordHash}
	if record.Event.Payload != nil {
		ref.PayloadSHA256 = record.Event.Payload.SHA256
	}
	return ref
}

func boundReferences(records map[string]indexedRecord) []BoundEventReference {
	result := make([]BoundEventReference, 0, len(records))
	for _, record := range records {
		result = append(result, boundReference(record.Record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EventID < result[j].EventID })
	return result
}

func sameTaskContext(anchor, event ledger.Event, agent ledger.Agent) bool {
	return anchor.Source.Agent == agent && event.Source.Agent == agent &&
		anchor.Source.DeviceID == event.Source.DeviceID &&
		anchor.Source.ThreadID == event.Source.ThreadID && anchor.Source.SessionID == event.Source.SessionID
}

func completeEvidenceEvent(event ledger.Event) error {
	if event.Completeness.Status != ledger.CompletenessComplete || event.Payload == nil {
		return errors.New("evidence event is incomplete")
	}
	return nil
}

func isCausalDescendant(records map[string]indexedRecord, eventID, ancestorID string) bool {
	if eventID == ancestorID {
		_, exists := records[eventID]
		return exists
	}
	if _, exists := records[ancestorID]; !exists {
		return false
	}
	seen := map[string]struct{}{}
	queue := []string{eventID}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[current]; duplicate {
			continue
		}
		seen[current] = struct{}{}
		record, exists := records[current]
		if !exists || record.Record.Event.Causality == nil {
			continue
		}
		for _, parent := range record.Record.Event.Causality.ParentEventIDs {
			parentRecord, parentExists := records[parent]
			if !parentExists || parentRecord.Index >= record.Index {
				continue
			}
			if parent == ancestorID {
				return true
			}
			queue = append(queue, parent)
		}
	}
	return false
}

func causalAncestorKinds(records map[string]indexedRecord, eventIDs []string,
	kinds ...ledger.EventKind) map[string]ledger.EventKind {
	wanted := map[ledger.EventKind]struct{}{}
	for _, kind := range kinds {
		wanted[kind] = struct{}{}
	}
	result := map[string]ledger.EventKind{}
	seen := map[string]struct{}{}
	queue := append([]string{}, eventIDs...)
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		if _, duplicate := seen[current]; duplicate {
			continue
		}
		seen[current] = struct{}{}
		record, exists := records[current]
		if !exists {
			continue
		}
		if _, selected := wanted[record.Record.Event.Kind]; selected {
			result[current] = record.Record.Event.Kind
		}
		if record.Record.Event.Causality == nil {
			continue
		}
		for _, parent := range record.Record.Event.Causality.ParentEventIDs {
			parentRecord, parentExists := records[parent]
			if parentExists && parentRecord.Index < record.Index {
				queue = append(queue, parent)
			}
		}
	}
	return result
}

func sortedUniqueMemoryReferences(references []retrieval.MemoryReference) bool {
	for index, reference := range references {
		if !validPrefixedHex(reference.MemoryID, "memory-") ||
			!validPrefixedHex(reference.RevisionID, "portable-revision-") {
			return false
		}
		if index > 0 && memoryReferenceKey(references[index-1]) >= memoryReferenceKey(reference) {
			return false
		}
	}
	return true
}

func sortedUniqueLabeledResults(items []LabeledResultEvent) bool {
	for index, item := range items {
		if strings.TrimSpace(item.EventID) == "" ||
			(item.Label != ResultLabelSuccess && item.Label != ResultLabelError && item.Label != ResultLabelNeutral) ||
			(index > 0 && items[index-1].EventID >= item.EventID) {
			return false
		}
	}
	return true
}

func sortedUniqueLabeledUserMessages(items []LabeledUserMessage) bool {
	for index, item := range items {
		if strings.TrimSpace(item.EventID) == "" ||
			(item.Label != UserMessageCorrection && item.Label != UserMessageNotCorrection) ||
			(index > 0 && items[index-1].EventID >= item.EventID) {
			return false
		}
	}
	return true
}

func sameMemoryReferences(left, right []retrieval.MemoryReference) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func memoryReferenceKey(reference retrieval.MemoryReference) string {
	return reference.MemoryID + "\x00" + reference.RevisionID
}

func validPrefixedHex(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	value = strings.TrimPrefix(value, prefix)
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decodeStrictEvaluationJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedUniqueStrings(values []string) []string {
	set := map[string]struct{}{}
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
