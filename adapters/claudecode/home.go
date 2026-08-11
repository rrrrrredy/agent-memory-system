package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/filesnapshot"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	PromptHistoryAdapterName    = "claude-code-prompt-history-jsonl"
	PromptHistoryAdapterVersion = "claude-code-prompt-history-jsonl/v1alpha1"
	CompanionAdapterName        = "claude-code-companion-files"
	CompanionAdapterVersion     = "claude-code-companion-files/v1alpha1"
)

var uuidAnywherePattern = regexp.MustCompile(
	`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`,
)

type HomeResult struct {
	Transcripts   Result              `json:"transcripts"`
	PromptHistory Result              `json:"prompt_history"`
	Companion     filesnapshot.Result `json:"companion_files"`
	Warnings      []string            `json:"warnings"`
}

// ImportHome captures Claude Code's documented session application-data
// surfaces. It deliberately excludes auth, settings, plugins, config backups,
// and generic caches because those are not task-process evidence.
func ImportHome(store *ledger.Store, claudeHome string, options Options) (HomeResult, error) {
	result := HomeResult{
		Transcripts: Result{SchemaVersion: adapterjsonl.ResultSchemaVersion,
			Kinds: map[string]int{}},
		PromptHistory: Result{SchemaVersion: adapterjsonl.ResultSchemaVersion,
			Kinds: map[string]int{}},
		Warnings: []string{},
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	absolute, err := filepath.Abs(claudeHome)
	if err != nil {
		return result, fmt.Errorf("resolve Claude home: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return result, fmt.Errorf("inspect Claude home: %w", err)
	}
	if !info.IsDir() {
		return result, errors.New("Claude home is not a directory")
	}

	projects := filepath.Join(absolute, "projects")
	if hasTranscript(projects) {
		result.Transcripts, err = ImportPath(store, projects, options)
		if err != nil {
			return result, err
		}
	} else {
		result.Warnings = append(result.Warnings, "no Claude Code project transcripts found")
	}

	history := filepath.Join(absolute, "history.jsonl")
	if fileExists(history) {
		result.PromptHistory, err = adapterjsonl.ImportPath(store, history, options, historySpec())
		if err != nil {
			return result, err
		}
	} else {
		result.Warnings = append(result.Warnings, "Claude Code prompt history is absent")
	}

	result.Companion, err = filesnapshot.CapturePath(store, absolute,
		filesnapshot.Options{Now: options.Now, Context: options.Context,
			ExpectedSources: options.ExpectedSources, ExpectedTracker: options.ExpectedTracker}, companionSpec())
	if err != nil {
		return result, err
	}
	return result, nil
}

func historySpec() adapterjsonl.Spec {
	return adapterjsonl.Spec{
		Agent: ledger.AgentClaudeCode, AdapterName: PromptHistoryAdapterName,
		AdapterVersion: PromptHistoryAdapterVersion, IDNamespace: "claude-code-history",
		MediaType: "application/x-ndjson", NoFilesError: "Claude Code prompt history is absent",
		MatchFile: func(_ string, entry fs.DirEntry) bool {
			return strings.EqualFold(entry.Name(), "history.jsonl")
		},
		DiscoverThreadID: func(_, _ string) (string, error) {
			return "global-claude-prompt-history", nil
		},
		Project: projectHistory,
	}
}

func projectHistory(raw json.RawMessage) []adapterjsonl.Projection {
	var entry struct {
		Timestamp json.RawMessage `json:"timestamp"`
		SessionID string          `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil
	}
	observedAt, ok := parseFlexibleTime(entry.Timestamp)
	reason := ""
	if !ok {
		reason = "timestamp_missing_or_invalid: used source modification time"
	}
	return []adapterjsonl.Projection{{
		Kind: ledger.KindUserMessage, ObservedAt: observedAt, SessionID: entry.SessionID,
		CompletenessReason: reason,
	}}
}

func parseFlexibleTime(raw json.RawMessage) (time.Time, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return time.Time{}, false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if parsed, ok := adapterjsonl.ParseObservedAt(text); ok {
			return parsed, true
		}
		if numeric, err := strconv.ParseInt(text, 10, 64); err == nil {
			return unixTimestamp(numeric), true
		}
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if numeric, err := number.Int64(); err == nil {
			return unixTimestamp(numeric), true
		}
	}
	return time.Time{}, false
}

func unixTimestamp(value int64) time.Time {
	if value > 10_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func companionSpec() filesnapshot.Spec {
	return filesnapshot.Spec{
		Agent: ledger.AgentClaudeCode, AdapterName: CompanionAdapterName,
		AdapterVersion: CompanionAdapterVersion, IDNamespace: "claude-code-companion",
		Include: includeCompanion,
		ThreadID: func(relativePath, sourcePathHash string) string {
			if match := uuidAnywherePattern.FindString(relativePath); match != "" {
				return strings.ToLower(match)
			}
			return "global-claude-home"
		},
		Kind: companionKind,
	}
}

func includeCompanion(relativePath string, _ fs.DirEntry) bool {
	relativePath = filepath.ToSlash(relativePath)
	parts := strings.Split(relativePath, "/")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "projects":
		lower := strings.ToLower("/" + relativePath + "/")
		if strings.Contains(lower, "/tool-results/") {
			return true
		}
		// Session-root and subagent JSONL are handled incrementally by ImportPath.
		if strings.HasSuffix(strings.ToLower(relativePath), ".jsonl") &&
			(len(parts) == 3 || strings.Contains(lower, "/subagents/")) {
			return false
		}
		return true
	case "file-history", "plans", "debug", "paste-cache", "image-cache",
		"session-env", "tasks", "shell-snapshots", "feedback-bundles",
		"sessions", "todos", "logs":
		return true
	case "stats-cache.json":
		return len(parts) == 1
	default:
		return false
	}
}

// IsHomeEvidenceFile reports whether a regular file is part of the Claude Code
// task-evidence surface captured by ImportHome. Authentication, settings,
// plugins, configuration backups, and generic caches remain excluded.
func IsHomeEvidenceFile(relativePath string, entry fs.DirEntry) bool {
	relativePath = filepath.ToSlash(relativePath)
	if strings.EqualFold(relativePath, "history.jsonl") {
		return true
	}
	parts := strings.Split(relativePath, "/")
	if len(parts) > 0 && parts[0] == "projects" && matchTranscript(relativePath, entry) {
		return true
	}
	return includeCompanion(relativePath, entry)
}

func companionKind(relativePath string) ledger.EventKind {
	relativePath = "/" + filepath.ToSlash(relativePath) + "/"
	switch {
	case strings.HasPrefix(relativePath, "/file-history/"):
		return ledger.KindFileChange
	case strings.Contains(relativePath, "/tool-results/"):
		return ledger.KindToolResult
	case strings.HasPrefix(relativePath, "/paste-cache/") ||
		strings.HasPrefix(relativePath, "/image-cache/"):
		return ledger.KindAttachment
	default:
		return ledger.KindSourceSnapshot
	}
}

func hasTranscript(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if matchTranscript(path, entry) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
