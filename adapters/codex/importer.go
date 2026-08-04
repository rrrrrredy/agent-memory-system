package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	AdapterName    = "codex-jsonl"
	AdapterVersion = "codex-jsonl/v1alpha1"
)

var threadIDPattern = regexp.MustCompile(
	`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
)

type Options = adapterjsonl.Options
type Result = adapterjsonl.Result

type rawEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func ImportPath(store *ledger.Store, sourcePath string, options Options) (Result, error) {
	return adapterjsonl.ImportPath(store, sourcePath, options, adapterjsonl.Spec{
		Agent:            ledger.AgentCodex,
		AdapterName:      AdapterName,
		AdapterVersion:   AdapterVersion,
		IDNamespace:      "codex",
		MediaType:        "application/x-ndjson",
		NoFilesError:     "no Codex rollout JSONL files found",
		MatchFile:        matchRollout,
		DiscoverThreadID: discoverThreadID,
		Project:          projectEvent,
	})
}

func matchRollout(_ string, entry fs.DirEntry) bool {
	name := strings.ToLower(entry.Name())
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

func projectEvent(raw json.RawMessage) []adapterjsonl.Projection {
	var envelope rawEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	kind, reasoning := classifyEvent(envelope)
	observedAt, timestampOK := adapterjsonl.ParseObservedAt(envelope.Timestamp)
	reason := ""
	if !timestampOK {
		reason = "timestamp_missing_or_invalid: used source modification time"
	}
	sourceEventID, callID, compactionID := sourceIdentifiers(envelope)
	return []adapterjsonl.Projection{{
		Kind: kind, ObservedAt: observedAt, Reasoning: reasoning,
		SourceEventID: sourceEventID, CallID: callID, CompactionID: compactionID,
		CompletenessReason: reason,
	}}
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
				Visibility: visibility, Note: "Codex event_msg agent_reasoning artifact",
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
			return ledger.KindReasoning, &ledger.ReasoningCapture{Visibility: visibility, Note: note}
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
	return firstString(payload, "id", "event_id"), firstString(payload, "call_id"),
		firstString(payload, "window_id", "compaction_id")
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
			if json.Unmarshal(bytes.TrimSpace(line), &envelope) == nil && envelope.Type == "session_meta" {
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

// Kept as package-private compatibility helpers for tests and downstream code.
func hashSourcePath(path string) (string, error) {
	return adapterjsonl.HashSourcePath(path)
}

func deterministicID(namespace string, parts ...string) string {
	return adapterjsonl.DeterministicID(namespace, parts...)
}

func parseObservedAt(value string) (time.Time, bool) {
	return adapterjsonl.ParseObservedAt(value)
}
