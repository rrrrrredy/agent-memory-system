package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func DecodeTaskAttemptDraft(reader io.Reader) (TaskAttemptDraft, error) {
	if reader == nil {
		return TaskAttemptDraft{}, errors.New("task attempt draft reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var draft TaskAttemptDraft
	if err := decoder.Decode(&draft); err != nil {
		return draft, fmt.Errorf("decode task attempt draft: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return draft, err
	}
	if draft.SchemaVersion != TaskAttemptDraftSchema || !safeIdentifier(draft.TaskID) ||
		!safeIdentifier(draft.AttemptID) || !validAgent(draft.Agent) || draft.Agent == ledger.AgentUnknown ||
		!validSHA256(draft.SemanticKeySHA256) ||
		draft.Privacy != "local_only" {
		return draft, errors.New("task attempt draft is invalid")
	}
	if draft.Condition != TaskConditionBaseline && draft.Condition != TaskConditionMemory {
		return draft, errors.New("task attempt draft condition is invalid")
	}
	return draft, nil
}

func DecodeTaskAttemptPreregistration(reader io.Reader) (TaskAttemptPreregistration, error) {
	if reader == nil {
		return TaskAttemptPreregistration{}, errors.New("task attempt preregistration reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var preregistration TaskAttemptPreregistration
	if err := decoder.Decode(&preregistration); err != nil {
		return TaskAttemptPreregistration{}, fmt.Errorf("decode task attempt preregistration: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return TaskAttemptPreregistration{}, err
	}
	if preregistration.SchemaVersion != TaskAttemptPreregisterSchema ||
		preregistration.EventID != preregistration.Request.WindowStartEventID ||
		!validSHA256(preregistration.RecordHash) || preregistration.Privacy != "local_only" {
		return TaskAttemptPreregistration{}, errors.New("task attempt preregistration envelope is invalid")
	}
	if err := validatePreregisteredTaskAttemptRequest(preregistration.Request); err != nil {
		return TaskAttemptPreregistration{}, err
	}
	return preregistration, nil
}

func PreregisterTaskAttempt(store *ledger.Store, draft TaskAttemptDraft,
	options TaskAttemptPreregisterOptions) (TaskAttemptPreregistration, error) {
	result := TaskAttemptPreregistration{SchemaVersion: TaskAttemptPreregisterSchema, Privacy: "local_only"}
	if store == nil || strings.TrimSpace(options.ThreadID) == "" || strings.TrimSpace(options.SessionID) == "" {
		return result, errors.New("store, thread id, and session id are required")
	}
	encodedDraft, err := json.Marshal(draft)
	if err != nil {
		return result, err
	}
	validated, err := DecodeTaskAttemptDraft(bytes.NewReader(encodedDraft))
	if err != nil {
		return result, err
	}
	for name, data := range map[string][]byte{
		"task spec": options.TaskSpec, "acceptance criteria": options.AcceptanceCriteria,
		"execution config": options.ExecutionConfig, "system-under-test manifest": options.SystemUnderTest,
	} {
		if len(data) == 0 {
			return result, fmt.Errorf("%s is empty", name)
		}
	}
	registry, err := loadOracleRegistry(options.OracleRegistryPath)
	if err != nil {
		return result, err
	}
	entry, exists := registry.Entries[oracleRegistryKey("builtin", "evidence-score", "v1")]
	if !exists {
		return result, errors.New("builtin evidence-score registry entry is required")
	}
	taskBlob, err := store.PutBlob(bytes.NewReader(options.TaskSpec))
	if err != nil {
		return result, err
	}
	criteriaBlob, err := store.PutBlob(bytes.NewReader(options.AcceptanceCriteria))
	if err != nil {
		return result, err
	}
	configBlob, err := store.PutBlob(bytes.NewReader(options.ExecutionConfig))
	if err != nil {
		return result, err
	}
	sutBlob, err := store.PutBlob(bytes.NewReader(options.SystemUnderTest))
	if err != nil {
		return result, err
	}
	request := TaskAttemptRequest{
		SchemaVersion: TaskAttemptRequestSchema, TaskID: validated.TaskID, AttemptID: validated.AttemptID,
		Agent: validated.Agent, SemanticKeySHA256: validated.SemanticKeySHA256,
		TaskSpecSHA256: taskBlob.SHA256, AcceptanceCriteriaSHA256: criteriaBlob.SHA256,
		ExecutionConfigSHA256: configBlob.SHA256, SystemUnderTestSHA256: sutBlob.SHA256,
		TaskSpecBlob: &taskBlob, AcceptanceCriteriaBlob: &criteriaBlob, ExecutionConfigBlob: &configBlob,
		SystemUnderTestBlob: &sutBlob, Condition: validated.Condition,
		MemoryReferences: []retrieval.MemoryReference{},
		Oracle: TaskOracle{Kind: "builtin", ID: "evidence-score", Version: "v1",
			RegistryEntrySHA256: oracleEntrySHA256(entry)},
		Privacy: "local_only",
	}
	if err := bindCurrentSystemArtifact(&request); err != nil {
		return result, err
	}
	request.WindowStartEventID = adapterjsonl.DeterministicID("task-attempt-contract",
		request.TaskID, request.AttemptID, string(request.Agent), request.SemanticKeySHA256,
		string(request.Condition), request.TaskSpecSHA256, request.AcceptanceCriteriaSHA256,
		request.ExecutionConfigSHA256, request.SystemUnderTestSHA256, request.SystemArtifactSHA256,
		request.Oracle.RegistryEntrySHA256)
	request.WindowEndEventID = adapterjsonl.DeterministicID("task-attempt-result", request.WindowStartEventID)
	request.Oracle.VerdictEventID = adapterjsonl.DeterministicID("task-attempt-verdict", request.WindowStartEventID)
	if request.WindowStartEventID == request.WindowEndEventID || request.WindowStartEventID == request.Oracle.VerdictEventID {
		return result, errors.New("generated task contract id collides with another task event")
	}
	if err := validatePreregisteredTaskAttemptRequest(request); err != nil {
		return result, err
	}
	if err := validateAttemptBlobs(store, request); err != nil {
		return result, err
	}
	contractData, err := marshalIndented(NewTaskAttemptContract(request))
	if err != nil {
		return result, err
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(contractData))
	if options.Now == nil {
		options.Now = time.Now
	}
	now := options.Now().UTC()
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: request.WindowStartEventID,
		Kind: ledger.KindTaskAttemptContract, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: request.Agent, Adapter: request.Oracle.ID,
			AdapterVersion: request.Oracle.Version, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: options.ThreadID, SessionID: options.SessionID,
			SourceEventID: request.AttemptID, SourceCursor: "task-attempt-contract:" + request.AttemptID},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}
	record, err := store.Append(event)
	if err != nil {
		return result, err
	}
	result.Request, result.EventID, result.RecordHash = request, event.EventID, record.RecordHash
	return result, nil
}

func ObserveTaskAttemptResult(store *ledger.Store, request TaskAttemptRequest, resultData []byte,
	mediaType string, now func() time.Time) (TaskAttemptObservationResult, error) {
	result := TaskAttemptObservationResult{SchemaVersion: TaskAttemptObservationSchema, Privacy: "local_only"}
	if store == nil || len(resultData) == 0 {
		return result, errors.New("store and non-empty result bytes are required")
	}
	if strings.TrimSpace(mediaType) == "" {
		mediaType = "application/octet-stream"
	}
	if err := bindCurrentSystemArtifact(&request); err != nil {
		return result, err
	}
	if request.Condition == TaskConditionMemory {
		if err := validatePreregisteredTaskAttemptRequest(request); err != nil {
			return result, err
		}
	} else if err := validateTaskAttemptRequest(request); err != nil {
		return result, err
	}
	if err := validateAttemptBlobs(store, request); err != nil {
		return result, err
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return result, err
	}
	start, exists := records[request.WindowStartEventID]
	if !exists || start.Record.Event.Kind != ledger.KindTaskAttemptContract {
		return result, errors.New("preregistered task contract is unavailable")
	}
	contractData, err := eventPayload(store, start.Record.Event)
	if err != nil {
		return result, err
	}
	var contract TaskAttemptContract
	if decodeStrictEvaluationJSON(contractData, &contract) != nil || !taskAttemptContractMatches(contract, request) {
		return result, errors.New("preregistered task contract does not match the result request")
	}
	parentID := request.WindowStartEventID
	if request.Condition == TaskConditionMemory {
		retrievalIDs, injectionIDs := []string{}, []string{}
		for index := start.Index + 1; index <= len(ordered); index++ {
			event := ordered[index-1].Event
			if !sameTaskContext(start.Record.Event, event, request.Agent) {
				continue
			}
			switch event.Kind {
			case ledger.KindRetrieval:
				retrievalIDs = append(retrievalIDs, event.EventID)
			case ledger.KindInjection:
				injectionIDs = append(injectionIDs, event.EventID)
			}
		}
		if len(retrievalIDs) != 1 || len(injectionIDs) != 1 {
			return result, errors.New("memory result requires exactly one post-contract retrieval and injection")
		}
		injectionData, readErr := eventPayload(store, records[injectionIDs[0]].Record.Event)
		if readErr != nil {
			return result, readErr
		}
		var injection retrieval.InjectionReceipt
		if decodeStrictEvaluationJSON(injectionData, &injection) != nil ||
			injection.InjectionID != injectionIDs[0] || injection.RetrievalReceiptID != retrievalIDs[0] ||
			len(injection.Memories) == 0 || !isCausalDescendant(records, injectionIDs[0], retrievalIDs[0]) {
			return result, errors.New("memory result injection is invalid")
		}
		parentID = injectionIDs[0]
	}
	payloadSHA := sha256Hex(resultData)
	if existing, duplicate := records[request.WindowEndEventID]; duplicate {
		data, readErr := eventPayload(store, existing.Record.Event)
		if readErr != nil || !bytes.Equal(data, resultData) || existing.Record.Event.Kind != ledger.KindToolResult ||
			!sameTaskContext(start.Record.Event, existing.Record.Event, request.Agent) ||
			existing.Record.Event.Causality == nil ||
			!sameStrings(existing.Record.Event.Causality.ParentEventIDs, []string{parentID}) {
			return result, errors.New("task result event id already exists with different evidence")
		}
		result.EventID, result.RecordHash, result.PayloadSHA256, result.Reused =
			request.WindowEndEventID, existing.Record.RecordHash, payloadSHA, true
		return result, nil
	}
	blob, err := store.PutBlob(bytes.NewReader(resultData))
	if err != nil {
		return result, err
	}
	payload := ledger.Payload{Encoding: "binary", MediaType: mediaType, Blob: &blob,
		SHA256: blob.SHA256, Bytes: blob.Bytes}
	if now == nil {
		now = time.Now
	}
	timestamp := now().UTC()
	source := start.Record.Event.Source
	source.Adapter, source.AdapterVersion = evaluationAdapterName, taskAttemptAdapterVersion
	source.SourceCursor = "task-attempt-result:" + request.AttemptID
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: request.WindowEndEventID,
		Kind: ledger.KindToolResult, ObservedAt: timestamp, RecordedAt: timestamp, Source: source,
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: []string{parentID}},
		Privacy:   ledger.Privacy{Classification: "local_only"}}
	record, err := store.Append(event)
	if err != nil {
		return result, err
	}
	result.EventID, result.RecordHash, result.PayloadSHA256 = event.EventID, record.RecordHash, payloadSHA
	return result, nil
}

func validatePreregisteredTaskAttemptRequest(request TaskAttemptRequest) error {
	if request.Condition != TaskConditionMemory {
		return validateTaskAttemptRequest(request)
	}
	if request.RetrievalReceiptID != "" || request.InjectionID != "" || request.AdoptionID != "" ||
		len(request.MemoryReferences) != 0 {
		return errors.New("preregistered memory attempt must not bind post-contract memory receipts")
	}
	validated := request
	validated.RetrievalReceiptID = "retrieval-preregistered"
	validated.InjectionID = "injection-preregistered"
	validated.AdoptionID = "adoption-preregistered"
	validated.MemoryReferences = []retrieval.MemoryReference{{MemoryID: "memory-" + strings.Repeat("a", 64),
		RevisionID: "portable-revision-" + strings.Repeat("b", 64)}}
	return validateTaskAttemptRequest(validated)
}

func hydratePreregisteredMemoryAttempt(store *ledger.Store, request TaskAttemptRequest) (TaskAttemptRequest, error) {
	if request.Condition != TaskConditionMemory {
		return request, nil
	}
	bound := request.RetrievalReceiptID != "" || request.InjectionID != "" || request.AdoptionID != "" ||
		len(request.MemoryReferences) != 0
	if bound {
		if request.RetrievalReceiptID == "" || request.InjectionID == "" || request.AdoptionID == "" ||
			len(request.MemoryReferences) == 0 {
			return request, errors.New("memory attempt has a partial receipt binding")
		}
		return request, nil
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return request, err
	}
	start, startOK := records[request.WindowStartEventID]
	end, endOK := records[request.WindowEndEventID]
	if !startOK || !endOK || start.Index >= end.Index {
		return request, errors.New("memory attempt window is unavailable")
	}
	retrievalIDs, injectionIDs := []string{}, []string{}
	for index := start.Index + 1; index <= end.Index; index++ {
		event := ordered[index-1].Event
		if !sameTaskContext(start.Record.Event, event, request.Agent) {
			continue
		}
		switch event.Kind {
		case ledger.KindRetrieval:
			retrievalIDs = append(retrievalIDs, event.EventID)
		case ledger.KindInjection:
			injectionIDs = append(injectionIDs, event.EventID)
		}
	}
	if len(retrievalIDs) != 1 || len(injectionIDs) != 1 {
		return request, errors.New("memory attempt requires exactly one post-contract retrieval and injection")
	}
	injectionData, err := eventPayload(store, records[injectionIDs[0]].Record.Event)
	if err != nil {
		return request, err
	}
	var injection retrieval.InjectionReceipt
	if decodeStrictEvaluationJSON(injectionData, &injection) != nil ||
		injection.InjectionID != injectionIDs[0] || injection.RetrievalReceiptID != retrievalIDs[0] ||
		len(injection.Memories) == 0 {
		return request, errors.New("memory attempt injection receipt is invalid")
	}
	adoptionIDs := []string{}
	firstAdoptionIndex := end.Index + 1
	lastAdoptionIndex := len(ordered)
	if verdict, exists := records[request.Oracle.VerdictEventID]; exists {
		lastAdoptionIndex = verdict.Index - 1
	}
	if firstAdoptionIndex > lastAdoptionIndex {
		return request, errors.New("memory attempt has no result-to-verdict adoption window")
	}
	for index := firstAdoptionIndex; index <= lastAdoptionIndex; index++ {
		event := ordered[index-1].Event
		if event.Kind != ledger.KindAdoption || !sameTaskContext(start.Record.Event, event, request.Agent) {
			continue
		}
		data, readErr := eventPayload(store, event)
		if readErr != nil {
			return request, readErr
		}
		var adoption retrieval.AdoptionReceipt
		if decodeStrictEvaluationJSON(data, &adoption) == nil &&
			adoption.RetrievalReceiptID == retrievalIDs[0] && adoption.InjectionID == injectionIDs[0] {
			adoptionIDs = append(adoptionIDs, adoption.AdoptionID)
		}
	}
	if len(adoptionIDs) != 1 {
		return request, errors.New("memory attempt requires exactly one matching adoption receipt")
	}
	request.RetrievalReceiptID, request.InjectionID, request.AdoptionID = retrievalIDs[0], injectionIDs[0], adoptionIDs[0]
	request.MemoryReferences = append([]retrieval.MemoryReference{}, injection.Memories...)
	return request, nil
}

func FinalizeBuiltinTaskAttempt(store *ledger.Store, request TaskAttemptRequest,
	oracleRegistryPath string, now func() time.Time) (TaskAttemptResult, error) {
	if store == nil {
		return TaskAttemptResult{}, errors.New("store is required")
	}
	if err := bindCurrentSystemArtifact(&request); err != nil {
		return TaskAttemptResult{}, err
	}
	request, err := hydratePreregisteredMemoryAttempt(store, request)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	if err := validateTaskAttemptRequest(request); err != nil {
		return TaskAttemptResult{}, err
	}
	if err := validateAttemptBlobs(store, request); err != nil {
		return TaskAttemptResult{}, err
	}
	registry, err := loadOracleRegistry(oracleRegistryPath)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	entry, exists := registry.Entries[oracleRegistryKey(request.Oracle.Kind, request.Oracle.ID, request.Oracle.Version)]
	if !exists || request.Oracle.Kind != "builtin" || request.Oracle.ID != "evidence-score" ||
		oracleEntrySHA256(entry) != request.Oracle.RegistryEntrySHA256 {
		return TaskAttemptResult{}, errors.New("task attempt is not bound to the builtin evidence-score oracle")
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	start, startOK := records[request.WindowStartEventID]
	end, endOK := records[request.WindowEndEventID]
	if !startOK || !endOK || start.Index >= end.Index ||
		!sameTaskContext(start.Record.Event, end.Record.Event, request.Agent) {
		return TaskAttemptResult{}, errors.New("task attempt window is unavailable or inconsistent")
	}
	resultRefs, userRefs := []BoundEventReference{}, []BoundEventReference{}
	for index := start.Index + 1; index <= end.Index; index++ {
		event := ordered[index-1].Event
		if !sameTaskContext(start.Record.Event, event, request.Agent) {
			continue
		}
		if event.Kind == ledger.KindGap || event.Completeness.Status != ledger.CompletenessComplete {
			return TaskAttemptResult{}, errors.New("task attempt window is incomplete")
		}
		switch event.Kind {
		case ledger.KindToolResult, ledger.KindFileChange:
			resultRefs = append(resultRefs, boundReference(ordered[index-1]))
		case ledger.KindUserMessage:
			userRefs = append(userRefs, boundReference(ordered[index-1]))
		}
	}
	if len(resultRefs) == 0 {
		return TaskAttemptResult{}, errors.New("task attempt has no result evidence")
	}
	combined := append(append([]BoundEventReference{}, resultRefs...), userRefs...)
	orderedEvents, err := replayEvents(store, combined, records)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	resultEvents, err := replayEvents(store, resultRefs, records)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	userEvents, err := replayEvents(store, userRefs, records)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	criteria, err := readAttemptBlob(store, *request.AcceptanceCriteriaBlob)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	judgment, err := runBuiltinEvidenceScoreOracle(criteria, orderedEvents, resultEvents, userEvents)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	verdict := materializeBuiltinVerdict(request, judgment, resultEvents, userEvents)
	if existing, exists := records[request.Oracle.VerdictEventID]; exists {
		existingData, readErr := eventPayload(store, existing.Record.Event)
		if readErr != nil {
			return TaskAttemptResult{}, readErr
		}
		var recorded TaskAttemptVerdict
		if decodeStrictEvaluationJSON(existingData, &recorded) != nil || !reflect.DeepEqual(recorded, verdict) {
			return TaskAttemptResult{}, errors.New("existing builtin verdict does not match canonical evidence-score replay")
		}
		return RecordTaskAttempt(store, request, now)
	}
	data, err := marshalIndented(verdict)
	if err != nil {
		return TaskAttemptResult{}, err
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	if now == nil {
		now = time.Now
	}
	source := start.Record.Event.Source
	source.SourceCursor = "task-attempt-verdict:" + request.AttemptID
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: request.Oracle.VerdictEventID,
		Kind: ledger.KindToolResult, ObservedAt: end.Record.Event.ObservedAt.UTC(), RecordedAt: now().UTC(),
		Source: source, Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: []string{request.WindowEndEventID}},
		Privacy:   ledger.Privacy{Classification: "local_only"}}
	if _, err := store.Append(event); err != nil {
		return TaskAttemptResult{}, err
	}
	return RecordTaskAttempt(store, request, now)
}
