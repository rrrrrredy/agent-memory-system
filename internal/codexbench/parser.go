package codexbench

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type execEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Usage    *Usage          `json:"usage,omitempty"`
	Message  string          `json:"message,omitempty"`
}

type execItem struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type parsedExecution struct {
	ThreadID     string
	AgentMessage string
	Usage        Usage
	Events       int
	ToolCalls    int
}

func parseExecutionJSONL(data []byte) (parsedExecution, error) {
	result := parsedExecution{}
	if len(data) == 0 {
		return result, errors.New("Codex exec emitted no JSONL events")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	threads, completions := 0, 0
	toolItems := map[string]struct{}{}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event execEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return result, fmt.Errorf("decode Codex exec event %d: %w", result.Events+1, err)
		}
		result.Events++
		switch event.Type {
		case "thread.started":
			threads++
			if strings.TrimSpace(event.ThreadID) == "" || result.ThreadID != "" && result.ThreadID != event.ThreadID {
				return result, errors.New("Codex exec thread identity is invalid")
			}
			result.ThreadID = event.ThreadID
		case "item.started", "item.completed":
			var item execItem
			if len(event.Item) == 0 || json.Unmarshal(event.Item, &item) != nil {
				return result, errors.New("Codex exec item is invalid")
			}
			if isToolItem(item.Type) {
				identity := item.ID
				if identity == "" {
					identity = fmt.Sprintf("event-%d", result.Events)
				}
				toolItems[identity] = struct{}{}
			}
			if event.Type == "item.completed" && item.Type == "agent_message" && strings.TrimSpace(item.Text) != "" {
				result.AgentMessage = item.Text
			}
		case "turn.completed":
			completions++
			if event.Usage == nil || !validUsage(*event.Usage) {
				return result, errors.New("Codex exec token usage is invalid")
			}
			result.Usage = *event.Usage
		case "turn.failed", "error":
			message := strings.TrimSpace(event.Message)
			if message == "" {
				message = event.Type
			}
			return result, fmt.Errorf("Codex exec reported failure: %s", message)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan Codex exec JSONL: %w", err)
	}
	if threads != 1 || completions != 1 || result.ThreadID == "" || result.AgentMessage == "" {
		return result, errors.New("Codex exec did not produce one complete thread, turn, and agent message")
	}
	result.ToolCalls = len(toolItems)
	return result, nil
}

func isToolItem(kind string) bool {
	return kind != "" && kind != "agent_message" && kind != "reasoning"
}

func validUsage(value Usage) bool {
	return value.InputTokens >= 0 && value.CachedInputTokens >= 0 &&
		value.CacheWriteInputTokens >= 0 && value.OutputTokens >= 0 &&
		value.ReasoningOutputTokens >= 0 && value.CachedInputTokens <= value.InputTokens
}

func totalTokens(value Usage) int {
	return value.InputTokens + value.CacheWriteInputTokens + value.OutputTokens + value.ReasoningOutputTokens
}
