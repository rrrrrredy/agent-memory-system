package episodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type rawResolver struct {
	store           *ledger.Store
	events          map[string]ledger.Event
	sourceSnapshots map[string]ledger.Event
	documentCache   map[string]any
}

type sourceSegmentPointer struct {
	SourceSegmentEventID string `json:"source_segment_event_id"`
	ByteStart            *int64 `json:"byte_start"`
	ByteEnd              *int64 `json:"byte_end"`
}

type sourceDocumentPointer struct {
	SourceSnapshotEventID string `json:"source_snapshot_event_id"`
	JSONPointer           string `json:"json_pointer"`
}

func newRawResolver(
	store *ledger.Store, events []ledger.Event, sourceSnapshots map[string]ledger.Event,
) *rawResolver {
	byID := make(map[string]ledger.Event, len(events))
	for _, event := range events {
		byID[event.EventID] = event
	}
	return &rawResolver{
		store: store, events: byID, sourceSnapshots: sourceSnapshots,
		documentCache: map[string]any{},
	}
}

func (r *rawResolver) eventByID(eventID string) (ledger.Event, bool) {
	if event, ok := r.events[eventID]; ok {
		return event, true
	}
	event, ok := r.sourceSnapshots[eventID]
	return event, ok
}

func (r *rawResolver) resolve(event ledger.Event) ([]byte, error) {
	if event.Payload == nil {
		return nil, errors.New("event has no payload")
	}
	if event.Payload.Blob != nil {
		return r.readBlob(*event.Payload.Blob)
	}
	if event.Payload.Content == nil {
		return nil, errors.New("event payload has neither content nor blob")
	}
	content := []byte(*event.Payload.Content)

	var segment sourceSegmentPointer
	if json.Unmarshal(content, &segment) == nil && segment.SourceSegmentEventID != "" &&
		segment.ByteStart != nil && segment.ByteEnd != nil {
		return r.resolveSegment(segment)
	}
	var document sourceDocumentPointer
	if json.Unmarshal(content, &document) == nil && document.SourceSnapshotEventID != "" &&
		document.JSONPointer != "" {
		return r.resolveDocument(document)
	}
	return content, nil
}

func (r *rawResolver) resolveSegment(pointer sourceSegmentPointer) ([]byte, error) {
	parent, ok := r.eventByID(pointer.SourceSegmentEventID)
	if !ok || parent.Payload == nil || parent.Payload.Blob == nil {
		return nil, fmt.Errorf("source segment %q is unavailable", pointer.SourceSegmentEventID)
	}
	base := int64(0)
	if parent.Source.ByteStart != nil {
		base = *parent.Source.ByteStart
	}
	start := *pointer.ByteStart - base
	end := *pointer.ByteEnd - base
	if start < 0 || end < start || end > parent.Payload.Blob.Bytes {
		return nil, fmt.Errorf("source segment range %d-%d is outside blob bounds", start, end)
	}
	file, err := r.store.OpenBlob(*parent.Payload.Blob)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data := make([]byte, end-start)
	if _, err := io.ReadFull(io.NewSectionReader(file, start, end-start), data); err != nil {
		return nil, fmt.Errorf("read source segment range: %w", err)
	}
	return data, nil
}

func (r *rawResolver) resolveDocument(pointer sourceDocumentPointer) ([]byte, error) {
	parent, ok := r.eventByID(pointer.SourceSnapshotEventID)
	if !ok || parent.Payload == nil || parent.Payload.Blob == nil {
		return nil, fmt.Errorf("source document %q is unavailable", pointer.SourceSnapshotEventID)
	}
	digest := parent.Payload.Blob.SHA256
	document, ok := r.documentCache[digest]
	if !ok {
		data, err := r.readBlob(*parent.Payload.Blob)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode source document: %w", err)
		}
		r.documentCache[digest] = document
	}
	value, err := resolveJSONPointer(document, pointer.JSONPointer)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode source document value: %w", err)
	}
	return data, nil
}

func (r *rawResolver) readBlob(reference ledger.BlobRef) ([]byte, error) {
	file, err := r.store.OpenBlob(reference)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read evidence blob: %w", err)
	}
	return data, nil
}

func resolveJSONPointer(document any, pointer string) (any, error) {
	if pointer == "" {
		return document, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", pointer)
	}
	current := document
	for _, encoded := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			value, ok := typed[token]
			if !ok {
				return nil, fmt.Errorf("JSON pointer token %q is unavailable", token)
			}
			current = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("JSON pointer index %q is invalid", token)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("JSON pointer cannot descend through %T", current)
		}
	}
	return current, nil
}
