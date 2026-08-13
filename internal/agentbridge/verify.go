package agentbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

type indexedRecord struct {
	Index  int
	Record ledger.Record
}

func Verify(store *ledger.Store) VerificationReport {
	report := VerificationReport{
		SchemaVersion: VerificationSchema,
		Issues:        []string{},
		Privacy:       PrivacyLocalOnly,
	}
	if store == nil {
		report.Issues = append(report.Issues, "local evidence store is required")
		return report
	}
	ledgerReport := store.Verify()
	report.RecordsChecked = ledgerReport.RecordsChecked
	if len(ledgerReport.Issues) != 0 {
		report.Issues = append(report.Issues, "evidence ledger verification failed")
		return report
	}
	checkedBlobs := map[string]struct{}{}
	starts := map[string]indexedRecord{}
	receipts := map[string]indexedRecord{}
	var visitIndex int
	err := store.VisitRecords(func(record ledger.Record) error {
		visitIndex++
		event := record.Event
		if event.Payload == nil || event.Payload.Blob == nil {
			return nil
		}
		switch event.Payload.MediaType {
		case StartedMediaType:
			if _, duplicate := starts[event.EventID]; duplicate {
				report.Issues = append(report.Issues, "native Agent start event is duplicated: "+event.EventID)
				return nil
			}
			starts[event.EventID] = indexedRecord{Index: visitIndex, Record: record}
		case ReceiptMediaType:
			if _, duplicate := receipts[event.EventID]; duplicate {
				report.Issues = append(report.Issues, "native Agent receipt event is duplicated: "+event.EventID)
				return nil
			}
			receipts[event.EventID] = indexedRecord{Index: visitIndex, Record: record}
		}
		return nil
	})
	if err != nil {
		report.Issues = append(report.Issues, "native Agent evidence traversal failed")
		return report
	}
	receiptIDs := make([]string, 0, len(receipts))
	for receiptID := range receipts {
		receiptIDs = append(receiptIDs, receiptID)
	}
	sort.Strings(receiptIDs)
	referencedStarts := map[string]struct{}{}
	for _, receiptID := range receiptIDs {
		indexed := receipts[receiptID]
		receipt, data, decodeErr := decodeReceiptEvent(store, indexed.Record.Event, checkedBlobs)
		if decodeErr != nil {
			report.Issues = append(report.Issues, receiptID+": "+decodeErr.Error())
			continue
		}
		startedIndexed, exists := starts[receipt.Started.EventID]
		if !exists {
			report.Issues = append(report.Issues, receiptID+": referenced start event is unavailable")
			continue
		}
		referencedStarts[receipt.Started.EventID] = struct{}{}
		started, _, startErr := decodeStartedEvent(store, startedIndexed.Record.Event, checkedBlobs)
		if startErr != nil {
			report.Issues = append(report.Issues, receiptID+": "+startErr.Error())
			continue
		}
		issues := validateExecution(store, indexed, receipt, data, startedIndexed, started, checkedBlobs)
		if len(issues) != 0 {
			for _, issue := range issues {
				report.Issues = append(report.Issues, receiptID+": "+issue)
			}
			continue
		}
		report.ReceiptsChecked++
	}
	for startID := range starts {
		if _, referenced := referencedStarts[startID]; !referenced {
			report.Issues = append(report.Issues, "native Agent start has no terminal receipt: "+startID)
		}
	}
	report.BlobsChecked = len(checkedBlobs)
	sort.Strings(report.Issues)
	return report
}

func ResolveVerifiedReceipt(store *ledger.Store, receiptID string) (Receipt, error) {
	if strings.TrimSpace(receiptID) == "" {
		return Receipt{}, errors.New("native Agent receipt id is required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return Receipt{}, fmt.Errorf("native Agent receipt verification failed: %s", strings.Join(report.Issues, "; "))
	}
	var found *Receipt
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID != receiptID {
			return nil
		}
		receipt, _, err := decodeReceiptEvent(store, record.Event, map[string]struct{}{})
		if err != nil {
			return err
		}
		if found != nil {
			return errors.New("native Agent receipt id is duplicated")
		}
		found = &receipt
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	if found == nil {
		return Receipt{}, errors.New("verified native Agent receipt is unavailable")
	}
	return *found, nil
}

func ListVerifiedExecutions(store *ledger.Store) ([]VerifiedExecution, error) {
	if store == nil {
		return nil, errors.New("local evidence store is required")
	}
	report := Verify(store)
	if len(report.Issues) != 0 {
		return nil, fmt.Errorf("native Agent receipt verification failed: %s", strings.Join(report.Issues, "; "))
	}
	starts := map[string]Started{}
	receipts := []Receipt{}
	err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Payload == nil {
			return nil
		}
		switch record.Event.Payload.MediaType {
		case StartedMediaType:
			started, _, decodeErr := decodeStartedEvent(store, record.Event, map[string]struct{}{})
			if decodeErr != nil {
				return decodeErr
			}
			starts[started.StartedEventID] = started
		case ReceiptMediaType:
			receipt, _, decodeErr := decodeReceiptEvent(store, record.Event, map[string]struct{}{})
			if decodeErr != nil {
				return decodeErr
			}
			receipts = append(receipts, receipt)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]VerifiedExecution, 0, len(receipts))
	for _, receipt := range receipts {
		started, exists := starts[receipt.Started.EventID]
		if !exists {
			return nil, errors.New("verified native Agent start is unavailable")
		}
		request, err := verifiedRequest(store, started)
		if err != nil {
			return nil, err
		}
		result = append(result, VerifiedExecution{Request: request, Started: started, Receipt: receipt})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Started.StartedAt.Equal(result[j].Started.StartedAt) {
			return result[i].Started.StartedEventID < result[j].Started.StartedEventID
		}
		return result[i].Started.StartedAt.Before(result[j].Started.StartedAt)
	})
	return result, nil
}

func ResolveVerifiedExecution(store *ledger.Store, receiptID string) (VerifiedExecution, error) {
	receipt, err := ResolveVerifiedReceipt(store, receiptID)
	if err != nil {
		return VerifiedExecution{}, err
	}
	var started *Started
	err = store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID != receipt.Started.EventID {
			return nil
		}
		value, _, decodeErr := decodeStartedEvent(store, record.Event, map[string]struct{}{})
		if decodeErr != nil {
			return decodeErr
		}
		if started != nil {
			return errors.New("native Agent start id is duplicated")
		}
		started = &value
		return nil
	})
	if err != nil {
		return VerifiedExecution{}, err
	}
	if started == nil {
		return VerifiedExecution{}, errors.New("verified native Agent start is unavailable")
	}
	request, err := verifiedRequest(store, *started)
	if err != nil {
		return VerifiedExecution{}, err
	}
	return VerifiedExecution{Request: request, Started: *started, Receipt: receipt}, nil
}

func verifiedRequest(store *ledger.Store, started Started) (RunRequest, error) {
	requestData, err := readBlob(store, started.RequestBlob)
	if err != nil {
		return RunRequest{}, fmt.Errorf("read verified native Agent request: %w", err)
	}
	var request RunRequest
	if err := decodeCanonicalJSON(requestData, &request); err != nil {
		return RunRequest{}, fmt.Errorf("decode verified native Agent request: %w", err)
	}
	if err := validateRequest(request); err != nil {
		return RunRequest{}, fmt.Errorf("validate verified native Agent request: %w", err)
	}
	return request, nil
}

func decodeStartedEvent(store *ledger.Store, event ledger.Event,
	checked map[string]struct{}) (Started, []byte, error) {
	var started Started
	data, err := readEventJSON(store, event, StartedMediaType, checked)
	if err != nil {
		return started, nil, err
	}
	if err := decodeCanonicalJSON(data, &started); err != nil {
		return started, nil, fmt.Errorf("decode native Agent start: %w", err)
	}
	return started, data, nil
}

func decodeReceiptEvent(store *ledger.Store, event ledger.Event,
	checked map[string]struct{}) (Receipt, []byte, error) {
	var receipt Receipt
	data, err := readEventJSON(store, event, ReceiptMediaType, checked)
	if err != nil {
		return receipt, nil, err
	}
	if err := decodeCanonicalJSON(data, &receipt); err != nil {
		return receipt, nil, fmt.Errorf("decode native Agent receipt: %w", err)
	}
	return receipt, data, nil
}

func readEventJSON(store *ledger.Store, event ledger.Event, mediaType string,
	checked map[string]struct{}) ([]byte, error) {
	if event.Payload == nil || event.Payload.Blob == nil ||
		event.Payload.Encoding != "binary" || event.Payload.MediaType != mediaType ||
		event.Payload.SHA256 != event.Payload.Blob.SHA256 ||
		event.Payload.Bytes != event.Payload.Blob.Bytes {
		return nil, errors.New("native Agent event payload binding is invalid")
	}
	data, err := checkedBlob(store, *event.Payload.Blob, checked)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func validateExecution(store *ledger.Store, receiptIndexed indexedRecord, receipt Receipt, receiptData []byte,
	startedIndexed indexedRecord, started Started, checked map[string]struct{}) []string {
	issues := []string{}
	add := func(ok bool, message string) {
		if !ok {
			issues = append(issues, message)
		}
	}
	startedEvent := startedIndexed.Record.Event
	receiptEvent := receiptIndexed.Record.Event
	expectedStartedID, startIDErr := startedIdentity(started)
	expectedReceiptID, receiptIDErr := receiptIdentity(receipt)
	add(startIDErr == nil && started.StartedEventID == expectedStartedID &&
		started.StartedEventID == startedEvent.EventID, "start content hash identity is invalid")
	add(receiptIDErr == nil && receipt.ReceiptID == expectedReceiptID &&
		receipt.ReceiptID == receiptEvent.EventID && canonicalEqual(receiptData, receipt),
		"receipt content hash identity is invalid")
	add(started.SchemaVersion == StartedSchema && started.Privacy == PrivacyLocalOnly &&
		receipt.SchemaVersion == ReceiptSchema && receipt.Privacy == PrivacyLocalOnly,
		"receipt or start envelope is invalid")
	add(receipt.ExecutionID == started.ExecutionID && receipt.TaskID == started.TaskID &&
		receipt.Started.EventID == started.StartedEventID &&
		receipt.Started.RecordSHA256 == startedIndexed.Record.RecordHash,
		"receipt does not bind the exact start record")
	add(startedIndexed.Index < receiptIndexed.Index &&
		!started.StartedAt.IsZero() && receipt.StartedAt.Equal(started.StartedAt) &&
		!receipt.FinishedAt.Before(receipt.StartedAt),
		"execution order or timestamps are invalid")
	add(validStartedEvent(startedEvent, started), "start ledger event is invalid")
	add(validReceiptEvent(receiptEvent, receipt), "receipt ledger event is invalid")
	add(receipt.ProviderAuthority == ProviderAuthority, "provider authority boundary is invalid")

	requestData, err := checkedBlob(store, started.RequestBlob, checked)
	var request RunRequest
	if err != nil || decodeCanonicalJSON(requestData, &request) != nil || validateRequest(request) != nil ||
		started.RequestSHA256 != sha256Hex(requestData) || request.TaskID != started.TaskID {
		issues = append(issues, "run request blob is invalid")
		return issues
	}
	promptData, err := checkedBlob(store, started.PromptBlob, checked)
	if err != nil || started.PromptSHA256 != started.PromptBlob.SHA256 ||
		started.PromptSHA256 != sha256Hex(promptData) {
		issues = append(issues, "rendered prompt blob is invalid")
		return issues
	}
	expectedPrompt := request.Prompt
	expectedMemories := []retrieval.MemoryReference{}
	if started.LoadoutContextReceiptID != "" {
		contextReceipt, resolveErr := loadoutcontext.ResolveVerifiedContext(store, started.LoadoutContextReceiptID)
		if resolveErr != nil || request.LoadoutContextReceiptID != started.LoadoutContextReceiptID ||
			contextReceipt.Context.Agent != ledger.AgentCodex ||
			contextReceipt.Context.Task != "" && contextReceipt.Context.Task != request.TaskID {
			issues = append(issues, "loadout context receipt binding is invalid")
			return issues
		}
		expectedPrompt = contextReceipt.Content +
			"\nThe following is the current task. Current task instructions override memory:\n" +
			request.Prompt
		expectedMemories = contextReceipt.Memories
	} else if request.LoadoutContextReceiptID != "" {
		issues = append(issues, "request loadout context is missing from the start record")
		return issues
	}
	add(string(promptData) == expectedPrompt && reflect.DeepEqual(started.MemoryReferences, expectedMemories),
		"rendered prompt does not match its task and memory sources")
	add(reflect.DeepEqual(started.Arguments, buildArguments(request)), "Codex arguments do not replay from the request")
	argumentSHA, _ := sha256JSON(started.Arguments)
	add(started.ArgumentsSHA256 == argumentSHA, "Codex argument hash is invalid")
	add(started.WorkingDirectorySHA256 == sha256Hex([]byte(request.WorkingDirectory)),
		"working directory hash is invalid")
	add(strings.TrimSpace(started.CodexVersion) != "" && len(started.CodexVersion) <= 256,
		"Codex version disclosure is invalid")
	add(started.EnvironmentPolicy == "inherited_for_auth; names hashed; values intentionally not recorded",
		"environment disclosure policy is invalid")
	add(started.EnvironmentNamesCount > 0 && validSHA256(started.EnvironmentNamesSHA256),
		"environment disclosure boundary is invalid")
	if err := verifyArtifactBlob(store, started.CodexExecutable, checked); err != nil {
		issues = append(issues, "Codex executable blob is invalid")
	}
	if err := verifyArtifactBlob(store, started.RunnerExecutable, checked); err != nil {
		issues = append(issues, "agentmem executable blob is invalid")
	}

	raw, rawErr := checkedBlob(store, receipt.RawEventsBlob, checked)
	parsed, parseErr := parseExecutionJSONL(raw)
	if rawErr != nil {
		issues = append(issues, "raw Codex events blob is invalid")
		return issues
	}
	if receipt.StderrBlob != nil {
		if _, err := checkedBlob(store, *receipt.StderrBlob, checked); err != nil {
			issues = append(issues, "stderr blob is invalid")
		}
	}
	if parseErr == nil {
		add(receipt.ThreadID == parsed.ThreadID && receipt.Events == parsed.Events &&
			receipt.ToolCalls == parsed.ToolCalls && reflect.DeepEqual(receipt.Usage, parsed.Usage) &&
			receipt.ReasoningVisibility == parsed.ReasoningVisibility,
			"receipt fields differ from raw Codex JSONL")
		if receipt.AgentMessageBlob == nil {
			issues = append(issues, "completed JSONL has no bound agent message")
		} else if message, err := checkedBlob(store, *receipt.AgentMessageBlob, checked); err != nil || string(message) != parsed.AgentMessage {
			issues = append(issues, "agent message blob differs from raw Codex JSONL")
		}
	} else {
		add(receipt.ThreadID == parsed.ThreadID && receipt.Events == parsed.Events &&
			receipt.ToolCalls == parsed.ToolCalls && reflect.DeepEqual(receipt.Usage, parsed.Usage) &&
			receipt.ReasoningVisibility == parsed.ReasoningVisibility &&
			receipt.AgentMessageBlob == nil,
			"failed JSONL receipt reports unverified parsed output")
	}
	switch receipt.Outcome {
	case OutcomeCompleted:
		add(receipt.ProcessExitCode == 0 && receipt.FailureKind == "" && parseErr == nil,
			"completed receipt is not a successful parsed process")
	case OutcomeFailed:
		add(receipt.FailureKind != "", "failed receipt has no failure kind")
		switch receipt.FailureKind {
		case "timeout":
			add(receipt.ProcessExitCode == 124, "timeout receipt exit code is invalid")
		case "process_exit":
			add(receipt.ProcessExitCode != 0 && receipt.ProcessExitCode != 124,
				"process failure receipt exit code is invalid")
		case "invalid_jsonl":
			add(receipt.ProcessExitCode == 0 && parseErr != nil,
				"invalid JSONL failure does not replay")
		case "executable_changed":
		default:
			issues = append(issues, "failed receipt kind is unsupported")
		}
	default:
		issues = append(issues, "receipt outcome is invalid")
	}
	return issues
}

func validStartedEvent(event ledger.Event, started Started) bool {
	expectedParents := []string{}
	if started.LoadoutContextReceiptID != "" {
		expectedParents = []string{started.LoadoutContextReceiptID}
	}
	return event.Kind == ledger.KindSystemEvent &&
		event.Source.Agent == ledger.AgentCodex &&
		event.Source.Adapter == "agentmem-native-agent" &&
		event.Source.AdapterVersion == AdapterVersion &&
		event.Source.ThreadID == started.ExecutionID &&
		event.Source.SessionID == started.ExecutionID &&
		event.Source.SourceEventID == event.EventID &&
		event.Source.SourceCursor == event.EventID &&
		event.ObservedAt.Equal(started.StartedAt) && event.RecordedAt.Equal(started.StartedAt) &&
		event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly &&
		reflect.DeepEqual(parentIDs(event), expectedParents)
}

func validReceiptEvent(event ledger.Event, receipt Receipt) bool {
	parents := parentIDs(event)
	reasoningMatches := event.Reasoning != nil &&
		event.Reasoning.Visibility == receipt.ReasoningVisibility &&
		event.Reasoning.Provider == "codex-cli"
	return event.Kind == ledger.KindToolResult &&
		event.Source.Agent == ledger.AgentCodex &&
		event.Source.Adapter == "agentmem-native-agent" &&
		event.Source.AdapterVersion == AdapterVersion &&
		event.Source.SourceEventID == event.EventID &&
		event.Source.SourceCursor == event.EventID &&
		event.ObservedAt.Equal(receipt.FinishedAt) && event.RecordedAt.Equal(receipt.FinishedAt) &&
		event.Completeness.Status == ledger.CompletenessComplete &&
		event.Privacy.Classification == PrivacyLocalOnly &&
		reflect.DeepEqual(parents, []string{receipt.Started.EventID}) && reasoningMatches
}

func parentIDs(event ledger.Event) []string {
	if event.Causality == nil {
		return []string{}
	}
	return event.Causality.ParentEventIDs
}

func verifyArtifactBlob(store *ledger.Store, artifact Artifact, checked map[string]struct{}) error {
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	data, err := checkedBlob(store, artifact.Blob, checked)
	if err != nil {
		return err
	}
	if int64(len(data)) != artifact.Bytes || sha256Hex(data) != artifact.SHA256 {
		return errors.New("artifact differs from its blob")
	}
	return nil
}

func checkedBlob(store *ledger.Store, reference ledger.BlobRef, checked map[string]struct{}) ([]byte, error) {
	data, err := readBlob(store, reference)
	if err == nil {
		checked[reference.SHA256] = struct{}{}
	}
	return data, err
}

func decodeCanonicalJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	if !canonicalEqual(data, target) {
		return errors.New("JSON is not canonical")
	}
	return nil
}
