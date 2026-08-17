package deepseekharness

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
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	EventAdapterName    = "deepseek-harness-event-jsonl"
	EventAdapterVersion = "deepseek-harness-event-jsonl/v1alpha1"
	CaptureSchema       = "deepseek-harness-capture/v1alpha1"
	maxSafeInteger      = int64(1<<53 - 1)
)

type EventOptions = adapterjsonl.Options
type EventResult = adapterjsonl.Result

type captureRecord struct {
	SchemaVersion string          `json:"schema_version"`
	CapturedAt    string          `json:"captured_at"`
	Kind          string          `json:"kind"`
	Session       json.RawMessage `json:"session"`
	Event         json.RawMessage `json:"event"`
	Revision      string          `json:"revision"`
	Artifact      *rawArtifact    `json:"artifact"`
	ReasonCode    string          `json:"reason_code"`
	Reason        string          `json:"reason"`
}

type rawArtifact struct {
	Meta     json.RawMessage `json:"meta"`
	Filename string          `json:"filename"`
	Content  string          `json:"content"`
}

type sessionEnvelope struct {
	ID     string          `json:"id"`
	Header json.RawMessage `json:"header"`
}

type sessionEvent struct {
	Type      string          `json:"type"`
	Seq       *int64          `json:"seq"`
	Time      *int64          `json:"time"`
	Data      json.RawMessage `json:"data"`
	Ignorable bool            `json:"ignorable"`
}

// IsEventSourceFile reports whether a file belongs to the DeepSeek Harness
// crash-safe event spool. Recovered partial fragments remain importable so a
// torn writer becomes an explicit gap instead of disappearing.
func IsEventSourceFile(_ string, entry fs.DirEntry) bool {
	return strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl")
}

func ImportEventPath(store *ledger.Store, sourcePath string, options EventOptions) (EventResult, error) {
	return adapterjsonl.ImportPath(store, sourcePath, options, adapterjsonl.Spec{
		Agent: ledger.AgentDeepSeekHarness, AdapterName: EventAdapterName,
		AdapterVersion: EventAdapterVersion, IDNamespace: "deepseek-harness-event",
		MediaType:    "application/x-ndjson",
		NoFilesError: "no DeepSeek Harness event JSONL files found",
		MatchFile:    IsEventSourceFile, DiscoverThreadID: discoverSpoolID,
		Project: projectCaptureRecord,
	})
}

func projectCaptureRecord(raw json.RawMessage) []adapterjsonl.Projection {
	var record captureRecord
	if json.Unmarshal(raw, &record) != nil {
		return []adapterjsonl.Projection{gapProjection("", "", time.Time{},
			"capture_record_unreadable: exact source bytes preserved")}
	}
	sessionID := sessionIDFrom(record.Session)
	capturedAt, capturedAtOK := adapterjsonl.ParseObservedAt(record.CapturedAt)
	if record.SchemaVersion != CaptureSchema {
		return []adapterjsonl.Projection{gapProjection(sessionID, record.Revision, capturedAt,
			"capture_schema_unknown: exact source bytes preserved")}
	}
	switch record.Kind {
	case "session_event":
		return projectSessionEvent(record.Event, sessionID, capturedAt, capturedAtOK)
	case "raw_artifact":
		return projectRawArtifact(record, sessionID, capturedAt, capturedAtOK)
	case "gap":
		reason := strings.TrimSpace(record.ReasonCode)
		if reason == "" {
			reason = "capture_gap"
		}
		if detail := strings.TrimSpace(record.Reason); detail != "" {
			reason += ": " + detail
		}
		return []adapterjsonl.Projection{gapProjection(sessionID, record.Revision, capturedAt, reason)}
	default:
		return []adapterjsonl.Projection{gapProjection(sessionID, record.Revision, capturedAt,
			"capture_kind_unknown: exact source bytes preserved")}
	}
}

func projectRawArtifact(
	record captureRecord,
	sessionID string,
	capturedAt time.Time,
	capturedAtOK bool,
) []adapterjsonl.Projection {
	if record.Artifact == nil {
		return []adapterjsonl.Projection{gapProjection(sessionID, record.Revision, capturedAt,
			"raw_artifact_missing: persistence snapshot had no artifact payload")}
	}
	if sessionID == "" {
		sessionID = sessionIDFrom(record.Artifact.Meta)
	}
	lines := strings.Split(record.Artifact.Content, "\n")
	projections := make([]adapterjsonl.Projection, 0, len(lines))
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &envelope) != nil {
			projection := gapProjection(sessionID, record.Revision, capturedAt,
				fmt.Sprintf("raw_artifact_line_%d_invalid_json: exact artifact preserved", index+1))
			projection.Key = "artifact-line-" + strconv.Itoa(index+1)
			projections = append(projections, projection)
			continue
		}
		var eventType string
		_ = json.Unmarshal(envelope["type"], &eventType)
		if eventType == "session" {
			if sessionID == "" {
				_ = json.Unmarshal(envelope["id"], &sessionID)
			}
			continue
		}
		eventRecords := []json.RawMessage{json.RawMessage(trimmed)}
		if expanded, packed, expandErr := expandStorageRecord(json.RawMessage(trimmed)); packed {
			if expandErr != nil {
				projection := gapProjection(sessionID, record.Revision, capturedAt,
					fmt.Sprintf("raw_artifact_line_%d_malformed_packed_row: %v", index+1, expandErr))
				projection.Key = "artifact-line-" + strconv.Itoa(index+1)
				projections = append(projections, projection)
				continue
			}
			eventRecords = expanded
		}
		for memberIndex, eventRecord := range eventRecords {
			eventProjections := projectSessionEvent(eventRecord, sessionID, capturedAt, capturedAtOK)
			for projectionIndex := range eventProjections {
				if eventProjections[projectionIndex].Key == "" {
					eventProjections[projectionIndex].Key = fmt.Sprintf(
						"artifact-line-%d-member-%d-%d", index+1, memberIndex, projectionIndex)
				}
			}
			projections = append(projections, eventProjections...)
		}
	}
	if len(projections) == 0 {
		return []adapterjsonl.Projection{{
			Key: "artifact-snapshot", StableIdentity: stableTextIdentity("artifact", sessionID, record.Revision),
			Kind: ledger.KindSystemEvent, ObservedAt: capturedAt, ThreadID: sessionID,
			SessionID: sessionID, SourceEventID: record.Revision,
			CompletenessReason: timestampReason(capturedAtOK),
		}}
	}
	return projections
}

func projectSessionEvent(
	raw json.RawMessage,
	sessionID string,
	capturedAt time.Time,
	capturedAtOK bool,
) []adapterjsonl.Projection {
	var event sessionEvent
	if json.Unmarshal(raw, &event) != nil || strings.TrimSpace(event.Type) == "" {
		return []adapterjsonl.Projection{gapProjection(sessionID, "", capturedAt,
			"session_event_unreadable: exact event preserved")}
	}
	observedAt := capturedAt
	timestampOK := capturedAtOK
	if event.Time != nil && *event.Time >= 0 {
		observedAt = time.UnixMilli(*event.Time).UTC()
		timestampOK = true
	}
	sourceEventID := ""
	if event.Seq != nil {
		sourceEventID = strconv.FormatInt(*event.Seq, 10)
	}
	base := adapterjsonl.Projection{
		StableIdentity: stableEventIdentity(sessionID, event, raw),
		Kind:           ledger.KindSystemEvent, ObservedAt: observedAt, ThreadID: sessionID,
		SessionID: sessionID, SourceEventID: sourceEventID,
		CompletenessReason: timestampReason(timestampOK),
	}

	switch event.Type {
	case "user/message":
		base.Kind = ledger.KindUserMessage
	case "assistant/message":
		base.Kind = ledger.KindAgentMessage
		if hasReasoningText(event.Data) {
			base.Reasoning = &ledger.ReasoningCapture{
				Visibility: ledger.ReasoningRawExposed,
				Note:       "reasoning text locally exposed in the DeepSeek Harness session event; provider-hidden reasoning is not claimed",
			}
		}
	case "assistant/chunk":
		chunkType, reasoningText := assistantChunk(event.Data)
		if chunkType == "reasoning-delta" || chunkType == "block-end:reasoning" {
			base.Kind = ledger.KindReasoning
			visibility := ledger.ReasoningNotExposed
			if strings.TrimSpace(reasoningText) != "" {
				visibility = ledger.ReasoningRawExposed
			}
			base.Reasoning = &ledger.ReasoningCapture{
				Visibility: visibility,
				Note:       "reasoning chunk locally exposed by DeepSeek Harness; provider-hidden reasoning is not claimed",
			}
		}
	case "tool/call":
		base.Kind = ledger.KindToolCall
		base.CallID = recursiveString(decodeMap(event.Data), "callId", "callID", "call_id")
	case "tool/result":
		base.Kind = ledger.KindToolResult
		base.CallID = recursiveString(decodeMap(event.Data), "callId", "callID", "call_id", "toolCallId")
	default:
		switch {
		case strings.HasPrefix(event.Type, "approval/"), strings.HasPrefix(event.Type, "permission/"):
			base.Kind = ledger.KindApproval
		case strings.HasPrefix(event.Type, "compaction/"):
			base.Kind = ledger.KindCompaction
			base.CompactionID = firstNonEmpty(recursiveString(decodeMap(event.Data), "id", "compactionId"), sourceEventID)
		case strings.HasPrefix(event.Type, "subagent/"), strings.HasPrefix(event.Type, "delegation/"):
			base.Kind = ledger.KindSubagentEvent
		case isKnownSystemEvent(event.Type):
			base.Kind = ledger.KindSystemEvent
		default:
			base.Kind = ledger.KindGap
			base.CompletenessReason = joinReason(base.CompletenessReason,
				"unknown_harness_event_type_"+event.Type+": exact event preserved")
		}
	}
	return []adapterjsonl.Projection{base}
}

func isKnownSystemEvent(eventType string) bool {
	switch eventType {
	case "turn/start", "turn/end", "step/start", "step/end", "request/header",
		"request/context", "session/title", "session/end-seed", "todo/write", "agent/inbox/spliced",
		"agent-preset/selected", "goal/change", "llm/retry", "sandbox/mode":
		return true
	}
	return strings.HasPrefix(eventType, "hook/")
}

func assistantChunk(data json.RawMessage) (string, string) {
	value := decodeMap(data)
	chunk, _ := value["chunk"].(map[string]any)
	chunkType, _ := chunk["type"].(string)
	if chunkType == "reasoning-delta" {
		text, _ := chunk["text"].(string)
		return chunkType, text
	}
	if chunkType == "block-end" {
		block, _ := chunk["block"].(map[string]any)
		blockType, _ := block["type"].(string)
		if blockType == "reasoning" {
			text, _ := block["text"].(string)
			return "block-end:reasoning", text
		}
	}
	return chunkType, ""
}

func hasReasoningText(data json.RawMessage) bool {
	value := decodeMap(data)
	message, _ := value["message"].(map[string]any)
	content, _ := message["content"].([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if block["type"] == "reasoning" {
			if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

func gapProjection(sessionID, sourceID string, observedAt time.Time, reason string) adapterjsonl.Projection {
	return adapterjsonl.Projection{
		StableIdentity: stableTextIdentity("gap", sessionID, sourceID, reason),
		Kind:           ledger.KindGap, ObservedAt: observedAt, ThreadID: sessionID,
		SessionID: sessionID, SourceEventID: sourceID, CompletenessReason: reason,
	}
}

func stableEventIdentity(sessionID string, event sessionEvent, raw json.RawMessage) string {
	compact := canonicalJSON(raw)
	sequence := "missing"
	if event.Seq != nil {
		sequence = strconv.FormatInt(*event.Seq, 10)
	}
	sum := sha256.Sum256(compact)
	return stableTextIdentity("event", sessionID, sequence, hex.EncodeToString(sum[:]))
}

func canonicalJSON(raw json.RawMessage) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			return encoded
		}
	}
	compact := bytes.Buffer{}
	if json.Compact(&compact, raw) != nil {
		return append([]byte(nil), raw...)
	}
	return compact.Bytes()
}

func expandStorageRecord(raw json.RawMessage) ([]json.RawMessage, bool, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, false, nil
	}
	var tag string
	_ = json.Unmarshal(envelope["type"], &tag)
	if tag != "text-chunks" && tag != "reasoning-chunks" && tag != "tool-call-chunks" {
		return nil, false, nil
	}
	if !exactKeys(envelope, "type", "seq0", "time0", "data") {
		return nil, true, errors.New("envelope must be exactly {type, seq0, time0, data}")
	}
	seq0, ok := safeInteger(envelope["seq0"])
	if !ok || seq0 < 0 {
		return nil, true, errors.New("seq0 must be a non-negative safe integer")
	}
	time0, ok := safeInteger(envelope["time0"])
	if !ok {
		return nil, true, errors.New("time0 must be a safe integer")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope["data"], &data) != nil {
		return nil, true, errors.New("data must be an object")
	}
	if tag == "tool-call-chunks" {
		if !exactKeys(data, "turn", "step", "index", "id", "dt", "args") &&
			!exactKeys(data, "turn", "step", "index", "id", "name", "dt", "args") {
			return nil, true, errors.New("tool-call data keys are invalid")
		}
	} else if !exactKeys(data, "turn", "step", "index", "dt", "texts") {
		return nil, true, errors.New("text or reasoning data keys are invalid")
	}
	turn, turnOK := safeInteger(data["turn"])
	step, stepOK := safeInteger(data["step"])
	index, indexOK := safeInteger(data["index"])
	if !turnOK || !stepOK || !indexOK {
		return nil, true, errors.New("turn, step, and index must be safe integers")
	}
	var deltas []int64
	if json.Unmarshal(data["dt"], &deltas) != nil {
		return nil, true, errors.New("dt must be an array of safe integers")
	}
	for _, delta := range deltas {
		if delta < -maxSafeInteger || delta > maxSafeInteger {
			return nil, true, errors.New("dt must contain safe integers")
		}
	}
	payloadKey := "texts"
	if tag == "tool-call-chunks" {
		payloadKey = "args"
	}
	var members []string
	if json.Unmarshal(data[payloadKey], &members) != nil || len(members) == 0 {
		return nil, true, fmt.Errorf("%s must be a non-empty string array", payloadKey)
	}
	if len(deltas) != len(members)-1 {
		return nil, true, errors.New("dt length does not match the packed member count")
	}
	if int64(len(members)-1) > maxSafeInteger-seq0 {
		return nil, true, errors.New("member sequences leave the safe integer range")
	}
	callID := ""
	callName := ""
	if tag == "tool-call-chunks" {
		if json.Unmarshal(data["id"], &callID) != nil {
			return nil, true, errors.New("tool-call id must be a string")
		}
		if rawName, exists := data["name"]; exists && json.Unmarshal(rawName, &callName) != nil {
			return nil, true, errors.New("tool-call name must be a string")
		}
	}

	result := make([]json.RawMessage, 0, len(members))
	currentTime := time0
	for memberIndex, member := range members {
		if memberIndex > 0 {
			delta := deltas[memberIndex-1]
			if delta > 0 && currentTime > maxSafeInteger-delta ||
				delta < 0 && currentTime < -maxSafeInteger-delta {
				return nil, true, errors.New("member timestamps leave the safe integer range")
			}
			currentTime += delta
		}
		chunk := map[string]any{"index": index}
		switch tag {
		case "text-chunks":
			chunk["type"] = "text-delta"
			chunk["text"] = member
		case "reasoning-chunks":
			chunk["type"] = "reasoning-delta"
			chunk["text"] = member
		case "tool-call-chunks":
			chunk["type"] = "tool-call-delta"
			chunk["id"] = callID
			chunk["argumentsDelta"] = member
			if _, exists := data["name"]; exists {
				chunk["name"] = callName
			}
		}
		event, err := json.Marshal(map[string]any{
			"type": "assistant/chunk", "seq": seq0 + int64(memberIndex), "time": currentTime,
			"data": map[string]any{"turn": turn, "step": step, "chunk": chunk},
		})
		if err != nil {
			return nil, true, fmt.Errorf("encode expanded packed event: %w", err)
		}
		result = append(result, event)
	}
	return result, true, nil
}

func safeInteger(raw json.RawMessage) (int64, bool) {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil ||
		value < -maxSafeInteger || value > maxSafeInteger {
		return 0, false
	}
	return value, true
}

func exactKeys(value map[string]json.RawMessage, keys ...string) bool {
	if len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := value[key]; !exists {
			return false
		}
	}
	return true
}

func stableTextIdentity(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, part)
	}
	return "deepseek-harness-" + hex.EncodeToString(hasher.Sum(nil))
}

func timestampReason(ok bool) string {
	if ok {
		return ""
	}
	return "event_and_capture_timestamp_missing_or_invalid: used source modification time"
}

func discoverSpoolID(path, sourcePathHash string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open DeepSeek Harness event spool: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	seen := map[string]struct{}{}
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var record captureRecord
			if json.Unmarshal(bytes.TrimSpace(line), &record) == nil {
				id := sessionIDFrom(record.Session)
				if id == "" && record.Artifact != nil {
					id = sessionIDFrom(record.Artifact.Meta)
				}
				if id != "" {
					seen[id] = struct{}{}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read DeepSeek Harness event spool: %w", readErr)
		}
	}
	if len(seen) == 1 {
		for id := range seen {
			return id, nil
		}
	}
	return "deepseek-harness-event-spool-" + sourcePathHash[:16], nil
}

func sessionIDFrom(raw json.RawMessage) string {
	var session sessionEnvelope
	if json.Unmarshal(raw, &session) == nil && strings.TrimSpace(session.ID) != "" {
		return session.ID
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) == nil {
		return recursiveString(value, "id")
	}
	return ""
}

func decodeMap(raw json.RawMessage) map[string]any {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func recursiveString(value any, keys ...string) string {
	keySet := map[string]struct{}{}
	for _, key := range keys {
		keySet[key] = struct{}{}
	}
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range keys {
				if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
					return text
				}
			}
			for key, child := range typed {
				if _, reserved := keySet[key]; reserved {
					continue
				}
				if found := visit(child); found != "" {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := visit(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return visit(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func joinReason(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "; " + right
}
