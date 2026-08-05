package retrieval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeAdoptionRequest(reader io.Reader) (AdoptionRequest, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request AdoptionRequest
	if err := decoder.Decode(&request); err != nil {
		return AdoptionRequest{}, fmt.Errorf("decode adoption request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return AdoptionRequest{}, err
	}
	return request, nil
}

func RecordAdoption(store *ledger.Store, context Context, request AdoptionRequest) (AdoptionReceipt, error) {
	if store == nil {
		return AdoptionReceipt{}, errors.New("local evidence store is required")
	}
	if !validAgent(context.Agent) {
		return AdoptionReceipt{}, errors.New("adoption context has an invalid agent")
	}
	if context.Channel == "" {
		context.Channel = ChannelCLI
	}
	if !validChannel(context.Channel) {
		return AdoptionReceipt{}, errors.New("adoption context has an invalid delivery channel")
	}
	if err := validateAdoptionRequest(request); err != nil {
		return AdoptionReceipt{}, err
	}

	evidenceEvents := map[string]ledger.Event{}
	eventIndexes := map[string]int{}
	recordIndex := 0
	var retrievalReceipt *Receipt
	var injectionReceipt *InjectionReceipt
	if err := store.VisitRecords(func(record ledger.Record) error {
		recordIndex++
		evidenceEvents[record.Event.EventID] = record.Event
		eventIndexes[record.Event.EventID] = recordIndex
		if record.Event.EventID == request.RetrievalReceiptID {
			var decoded Receipt
			if err := decodeReceiptEvent(record.Event, &decoded); err != nil {
				return fmt.Errorf("referenced retrieval receipt is invalid: %w", err)
			}
			if issues := validateReceipt(record.Event, decoded); len(issues) != 0 {
				return fmt.Errorf("referenced retrieval receipt is invalid: %s", strings.Join(issues, "; "))
			}
			retrievalReceipt = &decoded
		}
		if request.InjectionID != "" && record.Event.EventID == request.InjectionID {
			var decoded InjectionReceipt
			if err := decodeReceiptEvent(record.Event, &decoded); err != nil {
				return fmt.Errorf("referenced injection receipt is invalid: %w", err)
			}
			if issues := validateInjectionReceipt(record.Event, decoded); len(issues) != 0 {
				return fmt.Errorf("referenced injection receipt is invalid: %s", strings.Join(issues, "; "))
			}
			injectionReceipt = &decoded
		}
		return nil
	}); err != nil {
		return AdoptionReceipt{}, fmt.Errorf("verify adoption references: %w", err)
	}
	if retrievalReceipt == nil || retrievalReceipt.ReceiptID != request.RetrievalReceiptID ||
		retrievalReceipt.Result.Status != "completed" {
		return AdoptionReceipt{}, errors.New("referenced completed retrieval receipt is unavailable")
	}
	alignedContext, err := alignAdoptionContext(context, retrievalReceipt.Request.Context)
	if err != nil {
		return AdoptionReceipt{}, err
	}
	context = alignedContext
	allowed := referencesFromMatches(retrievalReceipt.Result.Selected)
	if request.InjectionID != "" {
		if injectionReceipt == nil || injectionReceipt.InjectionID != request.InjectionID ||
			injectionReceipt.RetrievalReceiptID != request.RetrievalReceiptID {
			return AdoptionReceipt{}, errors.New("referenced injection receipt is unavailable or belongs to another retrieval")
		}
		injectionEvent := evidenceEvents[request.InjectionID]
		if eventIndexes[request.InjectionID] <= eventIndexes[request.RetrievalReceiptID] {
			return AdoptionReceipt{}, errors.New("referenced injection does not follow its retrieval")
		}
		if injectionReceipt.Channel != retrievalReceipt.Request.Context.Channel ||
			!sourceMatchesContext(injectionEvent, retrievalReceipt.Request.Context) ||
			!causalityMatches(injectionEvent, []string{request.RetrievalReceiptID}) {
			return AdoptionReceipt{}, errors.New("referenced injection has invalid context or causality")
		}
		for _, memory := range injectionReceipt.Memories {
			if _, ok := allowed[referenceKey(memory)]; !ok {
				return AdoptionReceipt{}, errors.New("referenced injection contains memory absent from its retrieval")
			}
		}
		expectedContent, expectedMemories := renderBoundedContext(
			retrievalReceipt.Result, retrievalReceipt.Request)
		if injectionReceipt.Content != expectedContent ||
			!sameReferences(injectionReceipt.Memories, expectedMemories) {
			return AdoptionReceipt{}, errors.New("referenced injection does not match deterministic retrieval output")
		}
		allowed = referenceSet(injectionReceipt.Memories)
	}
	for _, item := range request.Items {
		if _, ok := allowed[referenceKey(item.MemoryReference)]; !ok {
			return AdoptionReceipt{}, errors.New("adoption item was not returned by the referenced delivery")
		}
	}
	for _, eventID := range request.OutcomeEvidenceEventIDs {
		event, ok := evidenceEvents[eventID]
		if !ok {
			return AdoptionReceipt{}, fmt.Errorf("outcome evidence event %q is unavailable", eventID)
		}
		deliveryIndex := eventIndexes[request.RetrievalReceiptID]
		if request.InjectionID != "" {
			deliveryIndex = eventIndexes[request.InjectionID]
		}
		if eventIndexes[eventID] <= deliveryIndex {
			return AdoptionReceipt{}, fmt.Errorf("outcome evidence event %q does not follow memory delivery", eventID)
		}
		if !sourceMatchesContext(event, retrievalReceipt.Request.Context) {
			return AdoptionReceipt{}, fmt.Errorf("outcome evidence event %q belongs to another task context", eventID)
		}
		if err := verifyOutcomeEvidence(store, event); err != nil {
			return AdoptionReceipt{}, fmt.Errorf("outcome evidence event %q: %w", eventID, err)
		}
		causalParent := request.InjectionID
		if causalParent == "" {
			causalParent = request.RetrievalReceiptID
		}
		if !eventDescendsFrom(evidenceEvents, eventIndexes, eventID, causalParent) {
			return AdoptionReceipt{}, fmt.Errorf("outcome evidence event %q is not causally bound to memory delivery", eventID)
		}
	}

	requestBytes, err := json.Marshal(request)
	if err != nil {
		return AdoptionReceipt{}, fmt.Errorf("encode adoption request: %w", err)
	}
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return AdoptionReceipt{}, err
	}
	receipt := AdoptionReceipt{
		SchemaVersion:           AdoptionReceiptSchemaVersion,
		AdoptionID:              "adoption-" + randomID,
		RecordedAt:              now,
		Reporter:                request.Reporter,
		RetrievalReceiptID:      request.RetrievalReceiptID,
		InjectionID:             request.InjectionID,
		Items:                   append([]AdoptionItem(nil), request.Items...),
		OutcomeEvidenceEventIDs: append([]string(nil), request.OutcomeEvidenceEventIDs...),
		RequestSHA256:           digestBytes(requestBytes),
		Privacy:                 PrivacyLocalOnly,
	}
	if err := appendAdoptionEvent(store, receipt, context); err != nil {
		return AdoptionReceipt{}, err
	}
	return receipt, nil
}

func alignAdoptionContext(observed, retrieved Context) (Context, error) {
	if observed.Agent == ledger.AgentUnknown {
		observed.Agent = retrieved.Agent
	} else if observed.Agent != retrieved.Agent {
		return Context{}, errors.New("adoption agent differs from the referenced retrieval")
	}
	if observed.ThreadID == "" {
		observed.ThreadID = retrieved.ThreadID
	} else if observed.ThreadID != retrieved.ThreadID {
		return Context{}, errors.New("adoption thread differs from the referenced retrieval")
	}
	if observed.SessionID == "" {
		observed.SessionID = retrieved.SessionID
	} else if observed.SessionID != retrieved.SessionID {
		return Context{}, errors.New("adoption session differs from the referenced retrieval")
	}
	return observed, nil
}

func validateAdoptionRequest(request AdoptionRequest) error {
	if request.SchemaVersion != AdoptionRequestSchemaVersion {
		return fmt.Errorf("unsupported adoption request schema %q", request.SchemaVersion)
	}
	if !validReporter(request.Reporter) {
		return errors.New("adoption reporter must identify a human, agent, or harness")
	}
	if !strings.HasPrefix(request.RetrievalReceiptID, "retrieval-") {
		return errors.New("adoption request requires a retrieval receipt id")
	}
	if request.InjectionID != "" && !strings.HasPrefix(request.InjectionID, "injection-") {
		return errors.New("adoption request has an invalid injection id")
	}
	if len(request.Items) == 0 {
		return errors.New("adoption request requires at least one memory item")
	}
	seen := map[string]struct{}{}
	requiresOutcomeEvidence := false
	attributedOutcomes := 0
	for _, item := range request.Items {
		if !validMemoryReference(item.MemoryReference) {
			return errors.New("adoption request has an invalid memory reference")
		}
		key := referenceKey(item.MemoryReference)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("adoption request repeats a memory reference")
		}
		seen[key] = struct{}{}
		if item.Adoption != AdoptionAdopted && item.Adoption != AdoptionNotAdopted &&
			item.Adoption != AdoptionUnknown {
			return errors.New("adoption request has an invalid adoption state")
		}
		if item.Outcome != OutcomeHelpful && item.Outcome != OutcomeNeutral &&
			item.Outcome != OutcomeHarmful && item.Outcome != OutcomeUnknown {
			return errors.New("adoption request has an invalid outcome state")
		}
		if item.Adoption != AdoptionAdopted && item.Outcome != OutcomeUnknown {
			return errors.New("a memory that was not adopted cannot be assigned a task outcome")
		}
		if item.Outcome != OutcomeUnknown {
			requiresOutcomeEvidence = true
			attributedOutcomes++
		}
		if strings.TrimSpace(item.Reason) == "" {
			return errors.New("each adoption item requires a reason")
		}
	}
	if !isSortedUnique(request.OutcomeEvidenceEventIDs) {
		return errors.New("outcome evidence event ids must be sorted and unique")
	}
	if requiresOutcomeEvidence && request.InjectionID == "" {
		return errors.New("task outcome claims require an exact memory injection")
	}
	if requiresOutcomeEvidence && len(request.OutcomeEvidenceEventIDs) == 0 {
		return errors.New("task outcome claims require causally bound evidence events")
	}
	if attributedOutcomes > 1 {
		return errors.New("one outcome evidence set cannot be attributed to multiple memories")
	}
	return nil
}

func eventDescendsFrom(events map[string]ledger.Event, indexes map[string]int,
	eventID, ancestorID string) bool {
	if eventID == ancestorID {
		_, exists := events[eventID]
		return exists
	}
	if _, exists := events[ancestorID]; !exists {
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
		event, exists := events[current]
		if !exists || event.Causality == nil {
			continue
		}
		for _, parent := range event.Causality.ParentEventIDs {
			if _, parentExists := events[parent]; !parentExists || indexes[parent] >= indexes[current] {
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

func appendAdoptionEvent(store *ledger.Store, receipt AdoptionReceipt, context Context) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode adoption receipt: %w", err)
	}
	payload := ledger.InlinePayload("json", "application/json", string(data))
	parents := append([]string{receipt.RetrievalReceiptID}, receipt.OutcomeEvidenceEventIDs...)
	if receipt.InjectionID != "" {
		parents = append(parents, receipt.InjectionID)
	}
	parents = sortedUnique(parents)
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       receipt.AdoptionID,
		Kind:          ledger.KindAdoption,
		ObservedAt:    receipt.RecordedAt,
		RecordedAt:    receipt.RecordedAt,
		Source: ledger.Source{
			Agent:          context.Agent,
			Adapter:        "agentmem-retrieval",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       threadIDForContext(context),
			SessionID:      context.SessionID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality:    &ledger.Causality{ParentEventIDs: parents},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	if err := appendEvidenceWithRetry(store, event); err != nil {
		return fmt.Errorf("append adoption receipt: %w", err)
	}
	return nil
}

func referencesFromMatches(matches []Match) map[string]struct{} {
	references := make([]MemoryReference, 0, len(matches))
	for _, match := range matches {
		references = append(references, match.MemoryReference)
	}
	return referenceSet(references)
}

func referenceSet(references []MemoryReference) map[string]struct{} {
	result := make(map[string]struct{}, len(references))
	for _, reference := range references {
		result[referenceKey(reference)] = struct{}{}
	}
	return result
}

func sameReferences(left, right []MemoryReference) bool {
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

func verifyOutcomeEvidence(store *ledger.Store, event ledger.Event) error {
	if event.Kind != ledger.KindToolResult && event.Kind != ledger.KindFileChange {
		return errors.New("only a tool result or file change can prove a non-human outcome claim")
	}
	if event.Completeness.Status != ledger.CompletenessComplete || event.Payload == nil {
		return errors.New("result evidence is not complete")
	}
	if event.Payload.Blob == nil {
		return nil
	}
	file, err := store.OpenBlob(*event.Payload.Blob)
	if err != nil {
		return fmt.Errorf("open result evidence blob: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return fmt.Errorf("read result evidence blob: %w", err)
	}
	if size != event.Payload.Bytes || hex.EncodeToString(hasher.Sum(nil)) != event.Payload.SHA256 {
		return errors.New("result evidence blob failed content verification")
	}
	return nil
}

func referenceKey(reference MemoryReference) string {
	return reference.MemoryID + "\x00" + reference.RevisionID
}

func validMemoryReference(reference MemoryReference) bool {
	return validPrefixedHash(reference.MemoryID, "memory-") &&
		validPrefixedHash(reference.RevisionID, "portable-revision-")
}

func validPrefixedHash(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	for _, char := range digest {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func validReporter(reporter Reporter) bool {
	if strings.TrimSpace(reporter.ID) == "" {
		return false
	}
	return reporter.Kind == "human" || reporter.Kind == "agent" || reporter.Kind == "harness"
}

func isSortedUnique(values []string) bool {
	for index, value := range values {
		if strings.TrimSpace(value) == "" || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON input contains trailing data")
		}
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}
