package hookcapture

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
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type ImportOptions = adapterjsonl.Options
type ImportResult = adapterjsonl.Result

type hookInput struct {
	SessionID           string  `json:"session_id"`
	TranscriptPath      *string `json:"transcript_path"`
	AgentTranscriptPath *string `json:"agent_transcript_path"`
	HookEventName       string  `json:"hook_event_name"`
	TurnID              string  `json:"turn_id"`
	CompactionID        string  `json:"compaction_id"`
	ToolUseID           string  `json:"tool_use_id"`
	Trigger             string  `json:"trigger"`
	AgentID             string  `json:"agent_id"`
	Prompt              string  `json:"prompt"`
	CompactSummary      string  `json:"compact_summary"`
}

func ImportPath(store *ledger.Store, agent ledger.Agent, sourcePath string, options ImportOptions) (ImportResult, error) {
	return adapterjsonl.ImportPath(store, sourcePath, options, adapterjsonl.Spec{
		Agent:            agent,
		AdapterName:      AdapterName,
		AdapterVersion:   AdapterVersion,
		IDNamespace:      "agent-hook-" + string(agent),
		MediaType:        "application/x-ndjson",
		NoFilesError:     "no captured hook envelopes found",
		MatchFile:        matchEnvelope,
		DiscoverThreadID: discoverHookThreadID,
		Project:          projectEnvelope,
	})
}

func matchEnvelope(_ string, entry fs.DirEntry) bool {
	name := strings.ToLower(entry.Name())
	return strings.HasPrefix(name, "hook-") && strings.HasSuffix(name, ".jsonl")
}

func projectEnvelope(raw json.RawMessage) []adapterjsonl.Projection {
	envelope, hookBytes, err := DecodeEnvelope(raw)
	if err != nil {
		return []adapterjsonl.Projection{{
			Key: "envelope-gap", Kind: ledger.KindGap,
			CompletenessReason: "hook_envelope_invalid: exact envelope bytes preserved",
		}}
	}
	observedAt := envelope.CapturedAt.UTC()
	if envelope.CaptureStatus != "complete" {
		return []adapterjsonl.Projection{{
			Key: "capture-gap", Kind: ledger.KindGap, ObservedAt: observedAt,
			CompletenessReason: "hook_input_partial: " + envelope.ErrorClass,
		}}
	}
	var input hookInput
	decoder := json.NewDecoder(bytes.NewReader(hookBytes))
	if err := decoder.Decode(&input); err != nil || requireHookEOF(decoder) != nil {
		return []adapterjsonl.Projection{{
			Key: "input-gap", Kind: ledger.KindGap, ObservedAt: observedAt,
			CompletenessReason: "hook_input_invalid_json: exact raw bytes preserved in envelope",
		}}
	}
	base := adapterjsonl.Projection{
		Key: "invocation", Kind: hookEventKind(input.HookEventName), ObservedAt: observedAt,
		ThreadID: input.SessionID, SessionID: input.SessionID,
		SourceEventID: firstHookValue(input.TurnID, input.ToolUseID), CallID: input.ToolUseID,
	}
	projections := []adapterjsonl.Projection{base}
	if strings.TrimSpace(input.SessionID) == "" {
		projections = append(projections, hookGap(base, "session-gap",
			"hook_session_id_unavailable: source retained under an unknown thread"))
	}
	if strings.TrimSpace(input.HookEventName) == "" {
		projections = append(projections, hookGap(base, "event-name-gap",
			"hook_event_name_unavailable: lifecycle point cannot be classified"))
	}
	if input.HookEventName == "UserPromptSubmit" && strings.TrimSpace(input.Prompt) == "" {
		projections = append(projections, hookGap(base, "prompt-gap",
			"hook_user_prompt_unavailable: exact hook input contains no prompt"))
	}
	if input.HookEventName == "PostCompact" && strings.TrimSpace(input.CompactSummary) == "" {
		projections = append(projections, hookGap(base, "compaction-summary-gap",
			"hook_compaction_summary_unavailable: compacted representation cannot be evaluated"))
	}
	if input.TranscriptPath == nil || strings.TrimSpace(*input.TranscriptPath) == "" {
		projections = append(projections, hookGap(base, "transcript-gap",
			"hook_transcript_path_unavailable: periodic source reconciliation required"))
	}
	if input.HookEventName == "SubagentStop" &&
		(input.AgentTranscriptPath == nil || strings.TrimSpace(*input.AgentTranscriptPath) == "") {
		gap := hookGap(base, "subagent-transcript-gap",
			"hook_subagent_transcript_path_unavailable: periodic source reconciliation required")
		gap.SourceEventID = input.AgentID
		projections = append(projections, gap)
	}
	if input.HookEventName == "PreCompact" || input.HookEventName == "PostCompact" {
		projections[0].CompactionID = firstHookValue(input.CompactionID, input.TurnID)
	}
	return projections
}

func hookGap(base adapterjsonl.Projection, key, reason string) adapterjsonl.Projection {
	base.Key = key
	base.Kind = ledger.KindGap
	base.CompletenessReason = reason
	return base
}

func hookEventKind(name string) ledger.EventKind {
	switch name {
	case "UserPromptSubmit", "UserPromptExpansion":
		return ledger.KindUserMessage
	case "PreCompact", "PostCompact":
		return ledger.KindCompaction
	case "PreToolUse":
		return ledger.KindToolCall
	case "PostToolUse", "PostToolUseFailure", "PostToolBatch":
		return ledger.KindToolResult
	case "PermissionRequest", "PermissionDenied":
		return ledger.KindApproval
	case "SubagentStart", "SubagentStop", "TeammateIdle", "TaskCreated", "TaskCompleted":
		return ledger.KindSubagentEvent
	case "FileChanged":
		return ledger.KindFileChange
	default:
		return ledger.KindSystemEvent
	}
}

func discoverHookThreadID(path, sourcePathHash string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open hook envelope for thread id: %w", err)
	}
	defer file.Close()
	line, readErr := bufio.NewReader(file).ReadBytes('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("read hook envelope for thread id: %w", readErr)
	}
	if len(bytes.TrimSpace(line)) > 0 {
		_, hookBytes, decodeErr := DecodeEnvelope(bytes.TrimSpace(line))
		if decodeErr == nil {
			var input hookInput
			if json.Unmarshal(hookBytes, &input) == nil && strings.TrimSpace(input.SessionID) != "" {
				return input.SessionID, nil
			}
		}
	}
	return "unknown-" + sourcePathHash[:16], nil
}

func requireHookEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func firstHookValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func DefaultSpoolPath(store *ledger.Store, agent ledger.Agent) string {
	return filepath.Clean(SpoolRoot(store, agent))
}
