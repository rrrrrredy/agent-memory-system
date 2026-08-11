package claudecode

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

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	AdapterName    = "claude-code-jsonl"
	AdapterVersion = "claude-code-jsonl/v1alpha1"
)

var sessionIDPattern = regexp.MustCompile(
	`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
)

type Options = adapterjsonl.Options
type Result = adapterjsonl.Result
type SourceFile = adapterjsonl.SourceFile

type transcriptRecord struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	Timestamp    string          `json:"timestamp"`
	UUID         string          `json:"uuid"`
	SessionID    string          `json:"sessionId"`
	ParentUUID   string          `json:"parentUuid"`
	LeafUUID     string          `json:"leafUuid"`
	IsSidechain  bool            `json:"isSidechain"`
	Summary      string          `json:"summary"`
	Message      json.RawMessage `json:"message"`
	Data         json.RawMessage `json:"data"`
	ToolUseID    string          `json:"toolUseID"`
	Compact      json.RawMessage `json:"compactMetadata"`
	AgentID      string          `json:"agentId"`
	ParentToolID string          `json:"parentToolUseID"`
}

type messageEnvelope struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Data      json.RawMessage `json:"data"`
}

func ImportPath(store *ledger.Store, sourcePath string, options Options) (Result, error) {
	return adapterjsonl.ImportPath(store, sourcePath, options, importerSpec())
}

func ImportSources(store *ledger.Store, sources []SourceFile, options Options) (Result, error) {
	return adapterjsonl.ImportSources(store, sources, options, importerSpec())
}

func importerSpec() adapterjsonl.Spec {
	return adapterjsonl.Spec{
		Agent:            ledger.AgentClaudeCode,
		AdapterName:      AdapterName,
		AdapterVersion:   AdapterVersion,
		IDNamespace:      "claude-code",
		MediaType:        "application/x-ndjson",
		NoFilesError:     "no Claude Code transcript JSONL files found",
		MatchFile:        matchTranscript,
		DiscoverThreadID: discoverThreadID,
		Project:          projectRecord,
	}
}

func matchTranscript(path string, entry fs.DirEntry) bool {
	name := strings.ToLower(entry.Name())
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	normalized := "/" + strings.ToLower(filepath.ToSlash(path)) + "/"
	if strings.Contains(normalized, "/tool-results/") {
		return false
	}
	if strings.Contains(normalized, "/subagents/") {
		return true
	}
	return sessionIDPattern.MatchString(name)
}

func projectRecord(raw json.RawMessage) []adapterjsonl.Projection {
	var record transcriptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil
	}
	observedAt, timestampOK := adapterjsonl.ParseObservedAt(record.Timestamp)
	reason := ""
	if !timestampOK {
		reason = "timestamp_missing_or_invalid: used source modification time"
	}
	base := adapterjsonl.Projection{
		ObservedAt: observedAt, SourceEventID: record.UUID, SessionID: record.SessionID,
		CompletenessReason: reason,
	}

	switch record.Type {
	case "user", "assistant":
		return projectMessage(record, base)
	case "summary":
		base.Key = "summary"
		base.Kind = ledger.KindCompaction
		base.CompactionID = firstNonEmpty(record.UUID, record.LeafUUID)
		return []adapterjsonl.Projection{base}
	case "system":
		base.Key = firstNonEmpty(record.Subtype, "system")
		base.Kind = ledger.KindSystemEvent
		if isCompactionSubtype(record.Subtype) || len(record.Compact) > 0 {
			base.Kind = ledger.KindCompaction
			base.CompactionID = firstNonEmpty(record.UUID, record.LeafUUID)
		}
		return []adapterjsonl.Projection{base}
	case "progress":
		base.Key = "progress"
		base.Kind = ledger.KindSystemEvent
		if record.AgentID != "" || record.ParentToolID != "" {
			base.Kind = ledger.KindSubagentEvent
			base.CallID = record.ParentToolID
		}
		return []adapterjsonl.Projection{base}
	case "queue-operation", "file-history-snapshot", "last-prompt", "pr-link", "task-notification":
		base.Key = record.Type
		base.Kind = ledger.KindSystemEvent
		return []adapterjsonl.Projection{base}
	default:
		base.Key = firstNonEmpty(record.Type, "unknown")
		base.Kind = ledger.KindUnknown
		return []adapterjsonl.Projection{base}
	}
}

func projectMessage(record transcriptRecord, base adapterjsonl.Projection) []adapterjsonl.Projection {
	var message messageEnvelope
	if err := json.Unmarshal(record.Message, &message); err != nil {
		base.Key = "message-unreadable"
		base.Kind = kindForMessage(record, message.Role)
		base.CompletenessReason = joinReason(base.CompletenessReason,
			"message_content_unreadable: exact source bytes preserved")
		return []adapterjsonl.Projection{base}
	}
	if base.SourceEventID == "" {
		base.SourceEventID = message.ID
	}

	if len(message.Content) == 0 || bytes.Equal(bytes.TrimSpace(message.Content), []byte("null")) {
		base.Key = "message-empty"
		base.Kind = kindForMessage(record, message.Role)
		base.CompletenessReason = joinReason(base.CompletenessReason,
			"message_content_missing: exact source record preserved")
		return []adapterjsonl.Projection{base}
	}
	var text string
	if json.Unmarshal(message.Content, &text) == nil {
		base.Key = "message-text"
		base.Kind = kindForMessage(record, message.Role)
		return []adapterjsonl.Projection{base}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		base.Key = "message-unknown-content"
		base.Kind = kindForMessage(record, message.Role)
		base.CompletenessReason = joinReason(base.CompletenessReason,
			"message_content_shape_unknown: exact source bytes preserved")
		return []adapterjsonl.Projection{base}
	}
	if len(blocks) == 0 {
		base.Key = "message-empty-blocks"
		base.Kind = kindForMessage(record, message.Role)
		return []adapterjsonl.Projection{base}
	}

	projections := make([]adapterjsonl.Projection, 0, len(blocks))
	for index, block := range blocks {
		projection := base
		projection.Key = fmt.Sprintf("block-%d-%s", index, firstNonEmpty(block.Type, "unknown"))
		switch block.Type {
		case "text", "input_text", "output_text":
			projection.Kind = kindForMessage(record, message.Role)
		case "thinking":
			projection.Kind = ledger.KindReasoning
			visibility := ledger.ReasoningNotExposed
			if strings.TrimSpace(block.Thinking) != "" || strings.TrimSpace(block.Text) != "" {
				visibility = ledger.ReasoningRawExposed
			}
			projection.Reasoning = &ledger.ReasoningCapture{
				Visibility: visibility,
				Note:       "Claude Code thinking block locally present in transcript",
			}
		case "redacted_thinking":
			projection.Kind = ledger.KindReasoning
			projection.Reasoning = &ledger.ReasoningCapture{
				Visibility: ledger.ReasoningEncryptedOpaque,
				Note:       "Claude Code redacted_thinking payload preserved but not readable",
			}
		case "tool_use", "server_tool_use":
			projection.Kind = ledger.KindToolCall
			projection.CallID = block.ID
		case "tool_result", "web_search_tool_result":
			projection.Kind = ledger.KindToolResult
			projection.CallID = block.ToolUseID
		default:
			projection.Kind = ledger.KindUnknown
			projection.CompletenessReason = joinReason(projection.CompletenessReason,
				"unknown_message_block: exact source bytes preserved")
		}
		if record.IsSidechain && (projection.Kind == ledger.KindUserMessage ||
			projection.Kind == ledger.KindAgentMessage) {
			projection.Kind = ledger.KindSubagentEvent
		}
		projections = append(projections, projection)
	}
	return projections
}

func kindForMessage(record transcriptRecord, role string) ledger.EventKind {
	if record.IsSidechain {
		return ledger.KindSubagentEvent
	}
	switch firstNonEmpty(role, record.Type) {
	case "user":
		return ledger.KindUserMessage
	case "assistant":
		return ledger.KindAgentMessage
	default:
		return ledger.KindUnknown
	}
}

func isCompactionSubtype(value string) bool {
	value = strings.ToLower(value)
	return value == "compact_boundary" || value == "context_compacted" || value == "compacted"
}

func joinReason(existing, added string) string {
	if existing == "" {
		return added
	}
	return existing + "; " + added
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func discoverThreadID(path, sourcePathHash string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Claude transcript for session id: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var record transcriptRecord
			if json.Unmarshal(bytes.TrimSpace(line), &record) == nil && record.SessionID != "" {
				return record.SessionID, nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read Claude transcript for session id: %w", readErr)
		}
	}
	if match := sessionIDPattern.FindString(filepath.Base(path)); match != "" {
		return strings.ToLower(match), nil
	}
	return "unknown-" + sourcePathHash[:16], nil
}
