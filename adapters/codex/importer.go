package codex

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	AdapterName    = "codex-jsonl"
	AdapterVersion = "codex-jsonl/v1alpha1"
)

var threadIDPattern = regexp.MustCompile(
	`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
)

type Options struct {
	FullReconcile bool
	Now           func() time.Time
}

type Result struct {
	FilesExamined  int            `json:"files_examined"`
	FilesChanged   int            `json:"files_changed"`
	SourceSegments int            `json:"source_segments"`
	EventsAppended int            `json:"events_appended"`
	EventsSkipped  int            `json:"events_skipped"`
	GapsAppended   int            `json:"gaps_appended"`
	BytesCaptured  int64          `json:"bytes_captured"`
	Kinds          map[string]int `json:"kinds"`
}

type importState struct {
	knownIDs        map[string]struct{}
	committedOffset map[string]int64
}

type rawEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type eventPointer struct {
	SourceSegmentEventID string `json:"source_segment_event_id"`
	ByteStart            int64  `json:"byte_start"`
	ByteEnd              int64  `json:"byte_end"`
}

func ImportPath(store *ledger.Store, sourcePath string, options Options) (Result, error) {
	result := Result{Kinds: map[string]int{}}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return result, errors.New("source path is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	files, err := collectRollouts(sourcePath)
	if err != nil {
		return result, err
	}
	if len(files) == 0 {
		return result, fmt.Errorf("no Codex rollout JSONL files found")
	}
	state, err := loadImportState(store)
	if err != nil {
		return result, err
	}
	appender, err := store.NewAppender()
	if err != nil {
		return result, fmt.Errorf("open evidence appender: %w", err)
	}
	for _, path := range files {
		result.FilesExamined++
		if err := importFile(store, appender, path, options, state, &result); err != nil {
			_ = appender.Close()
			return result, fmt.Errorf("import Codex rollout: %w", err)
		}
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func collectRollouts(sourcePath string) ([]string, error) {
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect source path: %w", err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, errors.New("source path is not a regular file")
		}
		return []string{absolute}, nil
	}

	var files []string
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := strings.ToLower(entry.Name())
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source path: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func loadImportState(store *ledger.Store) (*importState, error) {
	state := &importState{
		knownIDs:        map[string]struct{}{},
		committedOffset: map[string]int64{},
	}
	err := store.VisitRecords(func(record ledger.Record) error {
		event := record.Event
		state.knownIDs[event.EventID] = struct{}{}
		if event.Source.Agent != ledger.AgentCodex ||
			event.Source.SourcePathHash == "" ||
			event.Source.ByteEnd == nil ||
			event.Kind == ledger.KindSourceSnapshot {
			return nil
		}
		committed := event.Kind != ledger.KindGap ||
			strings.HasPrefix(event.Completeness.Reason, "invalid_json_line_terminated")
		if committed && *event.Source.ByteEnd > state.committedOffset[event.Source.SourcePathHash] {
			state.committedOffset[event.Source.SourcePathHash] = *event.Source.ByteEnd
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load Codex import state: %w", err)
	}
	return state, nil
}

func importFile(
	store *ledger.Store,
	appender *ledger.Appender,
	path string,
	options Options,
	state *importState,
	result *Result,
) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect rollout: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("rollout is not a regular file")
	}
	sourcePathHash, err := hashSourcePath(path)
	if err != nil {
		return err
	}
	threadID, err := discoverThreadID(path, sourcePathHash)
	if err != nil {
		return err
	}

	pending := make([]ledger.Event, 0, 256)
	committed := state.committedOffset[sourcePathHash]
	if committed > info.Size() {
		if err := appendTruncationGap(
			store, state, &pending, sourcePathHash, threadID, committed, info.Size(),
			info.ModTime(), options.Now(), result,
		); err != nil {
			return err
		}
		committed = 0
		state.committedOffset[sourcePathHash] = 0
	}

	start := committed
	if options.FullReconcile {
		start = 0
	}
	if start == info.Size() {
		if len(pending) > 0 {
			result.FilesChanged++
			if _, err := appender.AppendBatch(pending); err != nil {
				return fmt.Errorf("commit Codex evidence batch: %w", err)
			}
		}
		return nil
	}
	if start > info.Size() {
		return fmt.Errorf("invalid import offset %d for %d-byte rollout", start, info.Size())
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open rollout: %w", err)
	}
	defer file.Close()

	segmentLength := info.Size() - start
	blob, err := store.PutBlob(io.NewSectionReader(file, start, segmentLength))
	if err != nil {
		return fmt.Errorf("preserve source segment: %w", err)
	}
	capturedEnd := start + blob.Bytes
	segmentID := deterministicID(
		"codex-segment", sourcePathHash, strconv.FormatInt(start, 10),
		strconv.FormatInt(capturedEnd, 10), blob.SHA256,
	)
	startValue, endValue := start, capturedEnd
	segmentCompleteness := ledger.Completeness{Status: ledger.CompletenessComplete}
	if blob.Bytes != segmentLength {
		expectedBytes, capturedBytes := segmentLength, blob.Bytes
		segmentCompleteness = ledger.Completeness{
			Status:        ledger.CompletenessPartial,
			Reason:        "source_changed_during_capture: captured fewer bytes than the initial source size",
			ExpectedBytes: &expectedBytes,
			CapturedBytes: &capturedBytes,
		}
	}
	segmentEvent := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       segmentID,
		Kind:          ledger.KindSourceSnapshot,
		ObservedAt:    observedFileTime(info.ModTime(), options.Now()),
		RecordedAt:    options.Now().UTC(),
		Source: ledger.Source{
			Agent:          ledger.AgentCodex,
			Adapter:        AdapterName,
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			OS:             runtime.GOOS,
			ThreadID:       threadID,
			SourcePathHash: sourcePathHash,
			SourceCursor:   fmt.Sprintf("bytes:%d-%d", start, capturedEnd),
			ByteStart:      &startValue,
			ByteEnd:        &endValue,
		},
		Payload: &ledger.Payload{
			Encoding:  "binary",
			MediaType: "application/x-ndjson",
			Blob:      &blob,
			SHA256:    blob.SHA256,
			Bytes:     blob.Bytes,
		},
		Completeness: segmentCompleteness,
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	if _, exists := state.knownIDs[segmentID]; exists {
		result.EventsSkipped++
	} else {
		pending = append(pending, segmentEvent)
		state.knownIDs[segmentID] = struct{}{}
		result.SourceSegments++
	}
	result.FilesChanged++
	result.BytesCaptured += blob.Bytes

	preserved, err := store.OpenBlob(blob)
	if err != nil {
		return fmt.Errorf("open preserved source segment: %w", err)
	}
	defer preserved.Close()
	reader := bufio.NewReader(preserved)
	offset := start
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineStart := offset
			lineEnd := offset + int64(len(line))
			terminated := line[len(line)-1] == '\n'
			offset = lineEnd

			var envelope rawEnvelope
			parseErr := json.Unmarshal(bytes.TrimSpace(line), &envelope)
			if parseErr != nil {
				reason := "invalid_json_line_terminated: source record is not valid JSON"
				if !terminated && errors.Is(readErr, io.EOF) {
					reason = "trailing_partial_json: source may still be writing; retry from this byte"
				}
				if err := appendLineEvent(
					store, state, &pending, segmentID, sourcePathHash, threadID,
					lineStart, lineEnd, line, info.ModTime(), options.Now(),
					ledger.KindGap, nil, "", "", reason, result,
				); err != nil {
					return err
				}
				if terminated {
					state.committedOffset[sourcePathHash] = lineEnd
				}
			} else {
				kind, reasoning := classifyEvent(envelope)
				observedAt, timestampOK := parseObservedAt(envelope.Timestamp)
				reason := ""
				if !timestampOK {
					observedAt = observedFileTime(info.ModTime(), options.Now())
					reason = "timestamp_missing_or_invalid: used source modification time"
				}
				sourceEventID, callID, compactionID := sourceIdentifiers(envelope)
				if err := appendLineEvent(
					store, state, &pending, segmentID, sourcePathHash, threadID,
					lineStart, lineEnd, line, observedAt, options.Now(),
					kind, reasoning, sourceEventID, callID, reason, result,
					compactionID,
				); err != nil {
					return err
				}
				state.committedOffset[sourcePathHash] = lineEnd
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read preserved source segment: %w", readErr)
		}
	}
	if _, err := appender.AppendBatch(pending); err != nil {
		return fmt.Errorf("commit Codex evidence batch: %w", err)
	}
	return nil
}

func appendLineEvent(
	store *ledger.Store,
	state *importState,
	pending *[]ledger.Event,
	segmentID string,
	sourcePathHash string,
	threadID string,
	byteStart int64,
	byteEnd int64,
	rawLine []byte,
	observedAt time.Time,
	recordedAt time.Time,
	kind ledger.EventKind,
	reasoning *ledger.ReasoningCapture,
	sourceEventID string,
	callID string,
	completenessReason string,
	result *Result,
	compactionIDs ...string,
) error {
	rawDigest := sha256.Sum256(rawLine)
	eventID := deterministicID(
		"codex-event", sourcePathHash, threadID,
		strconv.FormatInt(byteStart, 10), strconv.FormatInt(byteEnd, 10),
		hex.EncodeToString(rawDigest[:]),
	)
	if _, exists := state.knownIDs[eventID]; exists {
		result.EventsSkipped++
		return nil
	}
	pointerJSON, err := json.Marshal(eventPointer{
		SourceSegmentEventID: segmentID,
		ByteStart:            byteStart,
		ByteEnd:              byteEnd,
	})
	if err != nil {
		return fmt.Errorf("encode source pointer: %w", err)
	}
	pointer := ledger.InlinePayload("json", "application/json", string(pointerJSON))
	startValue, endValue := byteStart, byteEnd
	status := ledger.CompletenessComplete
	if completenessReason != "" {
		status = ledger.CompletenessPartial
	}
	causality := &ledger.Causality{
		ParentEventIDs: []string{segmentID},
		CallID:         callID,
	}
	if len(compactionIDs) > 0 {
		causality.CompactionID = compactionIDs[0]
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          kind,
		ObservedAt:    observedAt.UTC(),
		RecordedAt:    recordedAt.UTC(),
		Source: ledger.Source{
			Agent:          ledger.AgentCodex,
			Adapter:        AdapterName,
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			OS:             runtime.GOOS,
			ThreadID:       threadID,
			SourceEventID:  sourceEventID,
			SourcePathHash: sourcePathHash,
			SourceCursor:   fmt.Sprintf("bytes:%d-%d", byteStart, byteEnd),
			ByteStart:      &startValue,
			ByteEnd:        &endValue,
		},
		Payload:      &pointer,
		Reasoning:    reasoning,
		Completeness: ledger.Completeness{Status: status, Reason: completenessReason},
		Causality:    causality,
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	state.knownIDs[eventID] = struct{}{}
	result.EventsAppended++
	result.Kinds[string(kind)]++
	if kind == ledger.KindGap {
		result.GapsAppended++
	}
	return nil
}

func appendTruncationGap(
	store *ledger.Store,
	state *importState,
	pending *[]ledger.Event,
	sourcePathHash string,
	threadID string,
	previousOffset int64,
	currentSize int64,
	modifiedAt time.Time,
	recordedAt time.Time,
	result *Result,
) error {
	eventID := deterministicID(
		"codex-truncation", sourcePathHash,
		strconv.FormatInt(previousOffset, 10), strconv.FormatInt(currentSize, 10),
	)
	if _, exists := state.knownIDs[eventID]; exists {
		result.EventsSkipped++
		return nil
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          ledger.KindGap,
		ObservedAt:    observedFileTime(modifiedAt, recordedAt),
		RecordedAt:    recordedAt.UTC(),
		Source: ledger.Source{
			Agent:          ledger.AgentCodex,
			Adapter:        AdapterName,
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			OS:             runtime.GOOS,
			ThreadID:       threadID,
			SourcePathHash: sourcePathHash,
			SourceCursor:   fmt.Sprintf("truncated:%d-to-%d", previousOffset, currentSize),
		},
		Completeness: ledger.Completeness{
			Status: ledger.CompletenessPartial,
			Reason: "source_truncated: previously committed bytes are no longer present at this path",
		},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	state.knownIDs[eventID] = struct{}{}
	result.EventsAppended++
	result.GapsAppended++
	result.Kinds[string(ledger.KindGap)]++
	return nil
}

func classifyEvent(envelope rawEnvelope) (ledger.EventKind, *ledger.ReasoningCapture) {
	payload := decodePayload(envelope.Payload)
	subtype := stringValue(payload["type"])
	switch envelope.Type {
	case "session_meta", "turn_context", "world_state":
		return ledger.KindSystemEvent, nil
	case "inter_agent_communication_metadata":
		return ledger.KindSubagentEvent, nil
	case "compacted":
		return ledger.KindCompaction, nil
	case "event_msg":
		switch subtype {
		case "user_message":
			return ledger.KindUserMessage, nil
		case "agent_message":
			return ledger.KindAgentMessage, nil
		case "agent_reasoning":
			visibility := ledger.ReasoningNotExposed
			if hasNonEmpty(payload["text"]) {
				visibility = ledger.ReasoningRawExposed
			}
			return ledger.KindReasoning, &ledger.ReasoningCapture{
				Visibility: visibility,
				Note:       "Codex event_msg agent_reasoning artifact",
			}
		case "context_compacted":
			return ledger.KindCompaction, nil
		case "sub_agent_activity":
			return ledger.KindSubagentEvent, nil
		case "mcp_tool_call_end", "patch_apply_end", "web_search_end":
			return ledger.KindToolResult, nil
		default:
			return ledger.KindSystemEvent, nil
		}
	case "response_item":
		switch subtype {
		case "message":
			switch stringValue(payload["role"]) {
			case "user":
				return ledger.KindUserMessage, nil
			case "assistant":
				return ledger.KindAgentMessage, nil
			default:
				return ledger.KindSystemEvent, nil
			}
		case "agent_message":
			return ledger.KindSubagentEvent, nil
		case "reasoning":
			visibility := ledger.ReasoningNotExposed
			note := "Codex response_item reasoning artifact"
			switch {
			case hasNonEmpty(payload["content"]):
				visibility = ledger.ReasoningRawExposed
			case hasNonEmpty(payload["summary"]):
				visibility = ledger.ReasoningSummaryOnly
				if hasNonEmpty(payload["encrypted_content"]) {
					note += "; opaque encrypted_content is preserved in the source segment"
				}
			case hasNonEmpty(payload["encrypted_content"]):
				visibility = ledger.ReasoningEncryptedOpaque
			}
			return ledger.KindReasoning, &ledger.ReasoningCapture{
				Visibility: visibility,
				Note:       note,
			}
		default:
			if strings.HasSuffix(subtype, "_call_output") {
				return ledger.KindToolResult, nil
			}
			if strings.HasSuffix(subtype, "_call") {
				return ledger.KindToolCall, nil
			}
			return ledger.KindUnknown, nil
		}
	default:
		return ledger.KindUnknown, nil
	}
}

func sourceIdentifiers(envelope rawEnvelope) (sourceEventID, callID, compactionID string) {
	payload := decodePayload(envelope.Payload)
	sourceEventID = firstString(payload, "id", "event_id")
	callID = firstString(payload, "call_id")
	compactionID = firstString(payload, "window_id", "compaction_id")
	return sourceEventID, callID, compactionID
}

func decodePayload(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return map[string]any{}
	}
	return payload
}

func firstString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(payload[key]); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func hasNonEmpty(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		for _, item := range typed {
			if hasNonEmpty(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if hasNonEmpty(item) {
				return true
			}
		}
	}
	return false
}

func parseObservedAt(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func observedFileTime(modifiedAt, fallback time.Time) time.Time {
	if modifiedAt.IsZero() {
		return fallback.UTC()
	}
	return modifiedAt.UTC()
}

func discoverThreadID(path, sourcePathHash string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open rollout for thread id: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var envelope rawEnvelope
			if json.Unmarshal(bytes.TrimSpace(line), &envelope) == nil &&
				envelope.Type == "session_meta" {
				payload := decodePayload(envelope.Payload)
				if id := firstString(payload, "id", "session_id"); id != "" {
					return id, nil
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read rollout for thread id: %w", readErr)
		}
	}
	if match := threadIDPattern.FindString(filepath.Base(path)); match != "" {
		return strings.ToLower(match), nil
	}
	return "unknown-" + sourcePathHash[:16], nil
}

func hashSourcePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve rollout path: %w", err)
	}
	normalized := filepath.ToSlash(filepath.Clean(absolute))
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:]), nil
}

func deterministicID(namespace string, parts ...string) string {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, namespace)
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, part)
	}
	return namespace + "-" + hex.EncodeToString(hasher.Sum(nil))
}
