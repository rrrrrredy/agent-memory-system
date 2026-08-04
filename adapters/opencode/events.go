package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	EventAdapterName    = "opencode-event-jsonl"
	EventAdapterVersion = "opencode-event-jsonl/v1alpha1"
)

type EventOptions = adapterjsonl.Options
type EventResult = adapterjsonl.Result

type capturedEvent struct {
	CapturedAt string          `json:"captured_at"`
	Event      json.RawMessage `json:"event"`
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

type busEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

func ImportEventPath(store *ledger.Store, sourcePath string, options EventOptions) (EventResult, error) {
	return adapterjsonl.ImportPath(store, sourcePath, options, adapterjsonl.Spec{
		Agent: ledger.AgentOpenCode, AdapterName: EventAdapterName,
		AdapterVersion: EventAdapterVersion, IDNamespace: "opencode-event",
		MediaType: "application/x-ndjson", NoFilesError: "no OpenCode event JSONL files found",
		MatchFile: func(_ string, entry fs.DirEntry) bool {
			return strings.HasSuffix(strings.ToLower(entry.Name()), ".jsonl")
		},
		DiscoverThreadID: discoverSpoolID,
		Project:          projectBusEvent,
	})
}

func projectBusEvent(raw json.RawMessage) []adapterjsonl.Projection {
	event, capturedAt := unwrapEvent(raw)
	if event.Type == "" {
		return nil
	}
	observedAt, timestampOK := adapterjsonl.ParseObservedAt(capturedAt)
	reason := ""
	if !timestampOK {
		reason = "capture_timestamp_missing_or_invalid: used source modification time"
	}
	properties := decodeMap(event.Properties)
	threadID := recursiveString(properties, "sessionID", "sessionId")
	if threadID == "" && strings.HasPrefix(event.Type, "session.") {
		threadID = recursiveString(properties, "id")
	}
	base := adapterjsonl.Projection{
		Kind: ledger.KindSystemEvent, ObservedAt: observedAt, ThreadID: threadID,
		SessionID: threadID, SourceEventID: recursiveString(properties, "id"),
		CallID:             recursiveString(properties, "callID", "callId", "call_id"),
		CompletenessReason: reason,
	}

	switch event.Type {
	case "message.part.updated":
		return projectUpdatedPart(event.Properties, base)
	case "message.updated", "message.removed", "message.part.removed",
		"session.created", "session.updated", "session.deleted", "session.status",
		"session.idle", "session.error", "todo.updated", "command.executed",
		"shell.env", "server.connected", "installation.updated", "lsp.updated",
		"lsp.client.diagnostics", "vcs.branch.updated", "file.watcher.updated":
		return []adapterjsonl.Projection{base}
	case "session.compacted":
		base.Kind = ledger.KindCompaction
		base.CompactionID = firstNonEmpty(base.SourceEventID, threadID)
		return []adapterjsonl.Projection{base}
	case "session.diff", "file.edited":
		base.Kind = ledger.KindFileChange
		return []adapterjsonl.Projection{base}
	case "permission.asked", "permission.updated", "permission.replied",
		"permission.denied", "question.asked", "question.replied", "question.rejected":
		base.Kind = ledger.KindApproval
		return []adapterjsonl.Projection{base}
	case "tool.execute.before":
		base.Kind = ledger.KindToolCall
		return []adapterjsonl.Projection{base}
	case "tool.execute.after":
		base.Kind = ledger.KindToolResult
		return []adapterjsonl.Projection{base}
	default:
		base.Kind = ledger.KindUnknown
		return []adapterjsonl.Projection{base}
	}
}

func projectUpdatedPart(rawProperties json.RawMessage, base adapterjsonl.Projection) []adapterjsonl.Projection {
	var properties struct {
		Part json.RawMessage `json:"part"`
	}
	if err := json.Unmarshal(rawProperties, &properties); err != nil || len(properties.Part) == 0 {
		base.Kind = ledger.KindUnknown
		base.CompletenessReason = joinReason(base.CompletenessReason,
			"part_update_unreadable: exact event preserved")
		return []adapterjsonl.Projection{base}
	}
	var part partInfo
	if err := json.Unmarshal(properties.Part, &part); err != nil {
		base.Kind = ledger.KindUnknown
		base.CompletenessReason = joinReason(base.CompletenessReason,
			"part_update_unreadable: exact event preserved")
		return []adapterjsonl.Projection{base}
	}
	base.ThreadID = firstNonEmpty(part.SessionID, base.ThreadID)
	base.SessionID = base.ThreadID
	base.SourceEventID = firstNonEmpty(part.ID, base.SourceEventID)
	base.CallID = firstNonEmpty(part.CallID, base.CallID)
	if base.ObservedAt.IsZero() {
		base.ObservedAt = observedTime(firstNonZero(part.Time.Start, part.Time.Created),
			time.Time{}, time.Time{})
	}
	switch part.Type {
	case "reasoning":
		base.Kind = ledger.KindReasoning
		visibility := ledger.ReasoningNotExposed
		if strings.TrimSpace(part.Text) != "" {
			visibility = ledger.ReasoningRawExposed
		}
		base.Reasoning = &ledger.ReasoningCapture{
			Visibility: visibility, Note: "OpenCode reasoning part from live event",
		}
		return []adapterjsonl.Projection{base}
	case "tool":
		call := base
		call.Key = "tool-call"
		call.Kind = ledger.KindToolCall
		result := []adapterjsonl.Projection{call}
		var state toolState
		if json.Unmarshal(part.State, &state) == nil &&
			(state.Status == "completed" || state.Status == "error") {
			output := base
			output.Key = "tool-result"
			output.Kind = ledger.KindToolResult
			result = append(result, output)
		}
		return result
	case "file":
		base.Kind = ledger.KindAttachment
	case "patch", "snapshot":
		base.Kind = ledger.KindFileChange
	case "subtask", "agent":
		base.Kind = ledger.KindSubagentEvent
	case "compaction":
		base.Kind = ledger.KindCompaction
		base.CompactionID = part.ID
	case "step-start", "step-finish", "retry", "text":
		// The part event does not carry a trustworthy role for text. Historical
		// export reconciliation supplies user/assistant classification.
		base.Kind = ledger.KindSystemEvent
	default:
		base.Kind = ledger.KindUnknown
	}
	return []adapterjsonl.Projection{base}
}

func unwrapEvent(raw json.RawMessage) (busEvent, string) {
	var captured capturedEvent
	if json.Unmarshal(raw, &captured) != nil {
		return busEvent{}, ""
	}
	if len(captured.Event) > 0 {
		var event busEvent
		if json.Unmarshal(captured.Event, &event) == nil {
			return event, captured.CapturedAt
		}
	}
	return busEvent{Type: captured.Type, Properties: captured.Properties}, captured.CapturedAt
}

func discoverSpoolID(path, sourcePathHash string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open OpenCode event spool: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	seen := map[string]struct{}{}
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			event, _ := unwrapEvent(json.RawMessage(bytes.TrimSpace(line)))
			threadID := recursiveString(decodeMap(event.Properties), "sessionID", "sessionId")
			if threadID != "" {
				seen[threadID] = struct{}{}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read OpenCode event spool: %w", readErr)
		}
	}
	if len(seen) == 1 {
		for threadID := range seen {
			return threadID, nil
		}
	}
	return "opencode-event-spool-" + sourcePathHash[:16], nil
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
