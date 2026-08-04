package hookcapture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const ReconcileResultSchemaVersion = "capture-reconciliation/v1alpha1"

type ReconcileOptions struct {
	SourcePath    string
	FullReconcile bool
	Now           func() time.Time
}

type ReconcileResult struct {
	SchemaVersion string                 `json:"schema_version"`
	Agent         ledger.Agent           `json:"agent"`
	HookSpool     *ImportResult          `json:"hook_spool,omitempty"`
	Codex         *codex.Result          `json:"codex,omitempty"`
	ClaudeCode    *claudecode.HomeResult `json:"claude_code,omitempty"`
	GapsAppended  int                    `json:"gaps_appended"`
	Issues        []string               `json:"issues"`
	Privacy       string                 `json:"privacy"`
}

type reconcileFailure struct {
	Operation        string `json:"operation"`
	SourcePathSHA256 string `json:"source_path_sha256"`
	Reason           string `json:"reason"`
}

// Reconcile imports durable hook envelopes and the Agent's historical source.
// Codex sourcePath is a rollout file or sessions directory. Claude Code
// sourcePath is the Claude home directory so companion artifacts are included.
func Reconcile(store *ledger.Store, agent ledger.Agent, options ReconcileOptions) (ReconcileResult, error) {
	result := ReconcileResult{
		SchemaVersion: ReconcileResultSchemaVersion, Agent: agent,
		Issues: []string{}, Privacy: "local_only",
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if agent != ledger.AgentCodex && agent != ledger.AgentClaudeCode {
		return result, errors.New("reconciliation supports codex or claude_code")
	}
	if strings.TrimSpace(options.SourcePath) == "" {
		return result, errors.New("source path is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	var failures []reconcileFailure
	var operationErrors []error

	spool := SpoolRoot(store, agent)
	if hasHookEnvelopes(spool) {
		failures = append(failures, auditTranscriptHints(spool, options.SourcePath)...)
		hookResult, err := ImportPath(store, agent, spool, ImportOptions{
			FullReconcile: options.FullReconcile, Now: options.Now,
		})
		result.HookSpool = &hookResult
		if err != nil {
			failures = append(failures, newReconcileFailure("hook_spool_import", spool, err))
			operationErrors = append(operationErrors, err)
		}
	}

	switch agent {
	case ledger.AgentCodex:
		imported, err := codex.ImportPath(store, options.SourcePath, codex.Options{
			FullReconcile: options.FullReconcile, Now: options.Now,
		})
		result.Codex = &imported
		if err != nil {
			failures = append(failures,
				newReconcileFailure("codex_transcript_import", options.SourcePath, err))
			operationErrors = append(operationErrors, err)
		}
	case ledger.AgentClaudeCode:
		imported, err := claudecode.ImportHome(store, options.SourcePath, claudecode.Options{
			FullReconcile: options.FullReconcile, Now: options.Now,
		})
		result.ClaudeCode = &imported
		if err != nil {
			failures = append(failures,
				newReconcileFailure("claude_home_import", options.SourcePath, err))
			operationErrors = append(operationErrors, err)
		}
		for _, warning := range imported.Warnings {
			failures = append(failures, reconcileFailure{
				Operation: "claude_home_import", SourcePathSHA256: hashPath(options.SourcePath),
				Reason: normalizeWarning(warning),
			})
		}
	}

	if len(failures) > 0 {
		appended, err := appendReconcileGaps(store, agent, failures, options.Now())
		result.GapsAppended = appended
		for _, failure := range failures {
			result.Issues = append(result.Issues, failure.Operation+":"+failure.Reason)
		}
		if err != nil {
			operationErrors = append(operationErrors, err)
		}
	}
	return result, errors.Join(operationErrors...)
}

func hasHookEnvelopes(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && matchEnvelope(root, entry) {
			return true
		}
	}
	return false
}

func auditTranscriptHints(spool, configuredSource string) []reconcileFailure {
	configured, configuredIsDirectory, err := resolvedConfiguredSource(configuredSource)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(spool)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	failures := []reconcileFailure{}
	for _, entry := range entries {
		if entry.IsDir() || !matchEnvelope(spool, entry) {
			continue
		}
		path := filepath.Join(spool, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_, raw, err := DecodeEnvelope(data)
		if err != nil {
			continue
		}
		var input hookInput
		if json.Unmarshal(raw, &input) != nil {
			continue
		}
		for _, hint := range []*string{input.TranscriptPath, input.AgentTranscriptPath} {
			if hint == nil || strings.TrimSpace(*hint) == "" {
				continue
			}
			reason := auditTranscriptHint(*hint, configured, configuredIsDirectory)
			if reason == "" {
				continue
			}
			failure := reconcileFailure{
				Operation: "transcript_hint_audit", SourcePathSHA256: hashPath(*hint),
				Reason: reason,
			}
			key := failure.SourcePathSHA256 + "\x00" + failure.Reason
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			failures = append(failures, failure)
		}
	}
	return failures
}

func resolvedConfiguredSource(path string) (string, bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", false, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	return resolved, info.IsDir(), err
}

func auditTranscriptHint(path, configured string, configuredIsDirectory bool) string {
	if !filepath.IsAbs(path) {
		return "source_hint_not_absolute"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "source_hint_unresolvable"
	}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return "source_hint_not_found"
	}
	if err != nil {
		return "source_hint_" + classifyError(err)
	}
	if !info.Mode().IsRegular() {
		return "source_hint_not_regular_file"
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "source_hint_unresolvable"
	}
	if configuredIsDirectory {
		relative, err := filepath.Rel(configured, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "source_hint_outside_configured_root"
		}
		return ""
	}
	if !samePath(configured, resolved) {
		return "source_hint_outside_configured_root"
	}
	return ""
}

func samePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func newReconcileFailure(operation, path string, err error) reconcileFailure {
	return reconcileFailure{
		Operation: operation, SourcePathSHA256: hashPath(path),
		Reason: "source_" + classifyError(err),
	}
}

func normalizeWarning(warning string) string {
	switch warning {
	case "no Claude Code project transcripts found":
		return "source_transcripts_not_found"
	case "Claude Code prompt history is absent":
		return "source_prompt_history_not_found"
	default:
		digest := sha256.Sum256([]byte(warning))
		return "source_warning_" + hex.EncodeToString(digest[:8])
	}
}

func hashPath(path string) string {
	digest, err := adapterjsonl.HashSourcePath(path)
	if err == nil {
		return digest
	}
	fallback := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(fallback[:])
}

func appendReconcileGaps(store *ledger.Store, agent ledger.Agent, failures []reconcileFailure, now time.Time) (
	appended int, returnedErr error,
) {
	eventsByID := make(map[string]ledger.Event, len(failures))
	for _, failure := range failures {
		data, err := json.Marshal(failure)
		if err != nil {
			return 0, err
		}
		eventID := adapterjsonl.DeterministicID("capture-gap", string(agent), failure.Operation,
			failure.SourcePathSHA256, failure.Reason)
		payload := ledger.InlinePayload("json", "application/json", string(data))
		eventsByID[eventID] = ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: eventID,
			Kind: ledger.KindGap, ObservedAt: now.UTC(), RecordedAt: now.UTC(),
			Source: ledger.Source{
				Agent: agent, Adapter: AdapterName, AdapterVersion: AdapterVersion,
				DeviceID: store.DeviceID(), OS: runtime.GOOS,
				ThreadID:       "global-" + strings.ReplaceAll(string(agent), "_", "-"),
				SourcePathHash: failure.SourcePathSHA256,
				SourceCursor:   "reconcile:" + failure.Operation,
			},
			Payload:      &payload,
			Completeness: ledger.Completeness{Status: ledger.CompletenessMissing, Reason: failure.Reason},
			Privacy:      ledger.Privacy{Classification: "local_only"},
		}
	}
	pending := make([]ledger.Event, 0, len(eventsByID))
	var collision error
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		expected, wanted := eventsByID[record.Event.EventID]
		if !wanted {
			return nil
		}
		if !sameReconcileGap(record.Event, expected) {
			collision = fmt.Errorf("capture gap event id collision %q", record.Event.EventID)
		}
		delete(eventsByID, record.Event.EventID)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan reconciliation gaps: %w", err)
	}
	defer func() {
		if closeErr := appender.Close(); closeErr != nil {
			if returnedErr == nil {
				returnedErr = closeErr
			} else {
				returnedErr = errors.Join(returnedErr, closeErr)
			}
		}
	}()
	if collision != nil {
		return 0, collision
	}
	for _, event := range eventsByID {
		pending = append(pending, event)
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].EventID < pending[right].EventID })
	if _, err := appender.AppendBatch(pending); err != nil {
		return 0, fmt.Errorf("append reconciliation gaps: %w", err)
	}
	return len(pending), nil
}

func sameReconcileGap(actual, expected ledger.Event) bool {
	return actual.Kind == expected.Kind && actual.Source.Agent == expected.Source.Agent &&
		actual.Source.Adapter == expected.Source.Adapter &&
		actual.Source.AdapterVersion == expected.Source.AdapterVersion &&
		actual.Source.ThreadID == expected.Source.ThreadID &&
		actual.Source.SourcePathHash == expected.Source.SourcePathHash &&
		actual.Source.SourceCursor == expected.Source.SourceCursor &&
		actual.Completeness.Status == expected.Completeness.Status &&
		actual.Completeness.Reason == expected.Completeness.Reason &&
		actual.Privacy.Classification == expected.Privacy.Classification &&
		actual.Causality == nil && actual.Reasoning == nil &&
		actual.Payload != nil && expected.Payload != nil &&
		actual.Payload.Encoding == expected.Payload.Encoding &&
		actual.Payload.MediaType == expected.Payload.MediaType &&
		actual.Payload.SHA256 == expected.Payload.SHA256 && actual.Payload.Bytes == expected.Payload.Bytes
}
