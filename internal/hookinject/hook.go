package hookinject

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
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	AdapterVersion            = "v1alpha1"
	MaximumInputBytes         = 4 * 1024 * 1024
	MaximumInlinePayloadBytes = 64 * 1024
)

type Config struct {
	EvidenceRoot string
	PortableRoot string
	Agent        ledger.Agent
	Repository   string
	Project      string
	Task         string
	Limit        int
	TokenBudget  int
	ByteBudget   int
}

type Input struct {
	SessionID      string  `json:"session_id"`
	TranscriptPath *string `json:"transcript_path"`
	CWD            string  `json:"cwd"`
	HookEventName  string  `json:"hook_event_name"`
	TurnID         string  `json:"turn_id"`
	Prompt         string  `json:"prompt"`
	Source         string  `json:"source"`
}

type Output struct {
	Continue           bool                `json:"continue"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func Process(reader io.Reader, config Config) (Output, error) {
	output := Output{Continue: true}
	if strings.TrimSpace(config.EvidenceRoot) == "" || strings.TrimSpace(config.PortableRoot) == "" {
		return output, errors.New("hook injection requires local evidence and portable memory roots")
	}
	if config.Agent != ledger.AgentCodex && config.Agent != ledger.AgentClaudeCode &&
		config.Agent != ledger.AgentOpenCode && config.Agent != ledger.AgentDeepSeekHarness {
		return output, errors.New("hook injection supports codex, claude_code, opencode, or deepseek_harness")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaximumInputBytes+1))
	if err != nil {
		return output, fmt.Errorf("read hook input: %w", err)
	}
	store, err := ledger.Open(config.EvidenceRoot)
	if err != nil {
		return output, err
	}
	if len(data) > MaximumInputBytes {
		data = data[:MaximumInputBytes]
		_ = appendGap(store, config.Agent, Input{}, data,
			"hook input exceeded the capture limit; a bounded prefix was preserved")
		return output, errors.New("hook input exceeded the capture limit")
	}
	var input Input
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&input); err != nil {
		_ = appendGap(store, config.Agent, Input{}, data, "hook input was not valid JSON")
		return output, errors.New("hook input is invalid")
	}
	if err := requireEOF(decoder); err != nil {
		_ = appendGap(store, config.Agent, input, data, "hook input contained trailing JSON data")
		return output, errors.New("hook input contains trailing data")
	}
	parentID, err := appendInvocation(store, config.Agent, input, data)
	if err != nil {
		return output, err
	}
	if !supportsRetrievalEvent(config.Agent, input.HookEventName) || strings.TrimSpace(input.Prompt) == "" {
		return output, nil
	}
	channel := retrieval.ChannelCodexHook
	if config.Agent == ledger.AgentClaudeCode {
		channel = retrieval.ChannelClaudeHook
	} else if config.Agent == ledger.AgentOpenCode {
		channel = retrieval.ChannelOpenCodePlugin
	} else if config.Agent == ledger.AgentDeepSeekHarness {
		channel = retrieval.ChannelHarness
	}
	contextResult, contextErr := retrieval.BuildContext(store, config.PortableRoot, retrieval.Request{
		SchemaVersion: retrieval.RequestSchemaVersion,
		Query:         input.Prompt,
		Context: retrieval.Context{
			Agent:      config.Agent,
			ThreadID:   input.SessionID,
			SessionID:  input.SessionID,
			Repository: config.Repository,
			Project:    config.Project,
			Task:       config.Task,
			Channel:    channel,
		},
		Limit:       config.Limit,
		TokenBudget: config.TokenBudget,
		ByteBudget:  config.ByteBudget,
	})
	if contextErr != nil {
		_ = appendProcessingGap(store, config.Agent, input, parentID, contextErr)
		return output, contextErr
	}
	if contextResult.Content != "" {
		output.HookSpecificOutput = &HookSpecificOutput{
			HookEventName:     input.HookEventName,
			AdditionalContext: contextResult.Content,
		}
	}
	return output, nil
}

func supportsRetrievalEvent(agent ledger.Agent, eventName string) bool {
	if eventName == "UserPromptSubmit" {
		return true
	}
	return agent == ledger.AgentOpenCode && eventName == "SessionCompacting"
}

func appendInvocation(store *ledger.Store, agent ledger.Agent, input Input, data []byte) (string, error) {
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return "", err
	}
	eventID := "hook-input-" + randomID
	payload, err := hookPayload(store, data)
	if err != nil {
		return "", err
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          ledger.KindSystemEvent,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: ledger.Source{
			Agent:          agent,
			Adapter:        "agentmem-injection-hook",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       fallbackThread(input.SessionID),
			SessionID:      input.SessionID,
			SourceEventID:  input.TurnID,
			SourcePathHash: transcriptPathHash(input.TranscriptPath),
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: retrieval.PrivacyLocalOnly},
	}
	if err := appendWithRetry(store, event); err != nil {
		return "", fmt.Errorf("append hook invocation: %w", err)
	}
	return eventID, nil
}

func appendProcessingGap(store *ledger.Store, agent ledger.Agent, input Input, parentID string, processingErr error) error {
	payload, _ := json.Marshal(map[string]any{
		"schema_version":  "hook-injection-gap/v1alpha1",
		"hook_event_name": input.HookEventName,
		"error":           processingErr.Error(),
		"privacy":         retrieval.PrivacyLocalOnly,
	})
	return appendGapEvent(store, agent, input, parentID, payload,
		"memory injection could not be completed")
}

func appendGap(store *ledger.Store, agent ledger.Agent, input Input, data []byte, reason string) error {
	payload, _ := json.Marshal(map[string]any{
		"schema_version":  "hook-input-gap/v1alpha1",
		"captured_prefix": string(data),
		"captured_bytes":  len(data),
		"reason":          reason,
		"privacy":         retrieval.PrivacyLocalOnly,
	})
	return appendGapEvent(store, agent, input, "", payload, reason)
}

func appendGapEvent(store *ledger.Store, agent ledger.Agent, input Input, parentID string, data []byte, reason string) error {
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return err
	}
	payload, err := hookPayload(store, data)
	if err != nil {
		return err
	}
	var causality *ledger.Causality
	if parentID != "" {
		causality = &ledger.Causality{ParentEventIDs: []string{parentID}}
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       "hook-gap-" + randomID,
		Kind:          ledger.KindGap,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: ledger.Source{
			Agent:          agent,
			Adapter:        "agentmem-injection-hook",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       fallbackThread(input.SessionID),
			SessionID:      input.SessionID,
			SourceEventID:  input.TurnID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessPartial, Reason: reason},
		Causality:    causality,
		Privacy:      ledger.Privacy{Classification: retrieval.PrivacyLocalOnly},
	}
	return appendWithRetry(store, event)
}

func hookPayload(store *ledger.Store, data []byte) (ledger.Payload, error) {
	if len(data) <= MaximumInlinePayloadBytes {
		return ledger.InlinePayload("json", "application/json", string(data)), nil
	}
	blob, err := store.PutBlob(bytes.NewReader(data))
	if err != nil {
		return ledger.Payload{}, fmt.Errorf("store hook payload blob: %w", err)
	}
	return ledger.Payload{
		Encoding:  "json",
		MediaType: "application/json",
		Blob:      &blob,
		SHA256:    blob.SHA256,
		Bytes:     blob.Bytes,
	}, nil
}

func appendWithRetry(store *ledger.Store, event ledger.Event) error {
	delay := 5 * time.Millisecond
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		_, err := store.Append(event)
		if err == nil {
			return nil
		}
		last = err
		if !errors.Is(err, ledger.ErrWriterLocked) || attempt == 5 {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return last
}

func transcriptPathHash(path *string) string {
	if path == nil || *path == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(*path))
	return hex.EncodeToString(digest[:])
}

func fallbackThread(sessionID string) string {
	if sessionID == "" {
		return "unknown-hook-session"
	}
	return sessionID
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}
