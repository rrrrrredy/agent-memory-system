package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/filesnapshot"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	CaptureAdapterName    = "opencode-cli-capture"
	CaptureAdapterVersion = "opencode-cli-capture/v1alpha1"
)

type CaptureOptions struct {
	Binary      string
	StagingRoot string
	Now         func() time.Time
	Runner      commandRunner
}

type CaptureResult struct {
	SessionsListed   int                 `json:"sessions_listed"`
	ExportsWritten   int                 `json:"exports_written"`
	ExportsSucceeded int                 `json:"exports_succeeded"`
	ExportsPartial   int                 `json:"exports_partial"`
	ExportsFailed    int                 `json:"exports_failed"`
	GapsAppended     int                 `json:"gaps_appended"`
	Import           Result              `json:"import"`
	Metadata         filesnapshot.Result `json:"metadata"`
}

type commandRunner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, binary string, args ...string) error
}

type execCommandRunner struct{}

func (execCommandRunner) Run(
	ctx context.Context, stdout, stderr io.Writer, binary string, args ...string,
) error {
	command := exec.CommandContext(ctx, binary, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type sessionListEntry struct {
	ID string `json:"id"`
}

type captureFailure struct {
	SessionID    string `json:"session_id"`
	Operation    string `json:"operation"`
	Reason       string `json:"reason"`
	StdoutSHA256 string `json:"stdout_sha256,omitempty"`
	StderrSHA256 string `json:"stderr_sha256,omitempty"`
}

type captureManifest struct {
	SchemaVersion string           `json:"schema_version"`
	CapturedAt    time.Time        `json:"captured_at"`
	Sessions      int              `json:"sessions"`
	Exports       []captureExport  `json:"exports"`
	Failures      []captureFailure `json:"failures"`
}

type captureExport struct {
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`
	ExportSHA256 string `json:"export_sha256"`
	StderrSHA256 string `json:"stderr_sha256"`
}

func CaptureAll(ctx context.Context, store *ledger.Store, options CaptureOptions) (CaptureResult, error) {
	result := CaptureResult{}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(options.StagingRoot) == "" {
		return result, errors.New("staging root is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Binary == "" {
		options.Binary = "opencode"
	}
	if options.Runner == nil {
		options.Runner = execCommandRunner{}
	}
	staging, err := filepath.Abs(options.StagingRoot)
	if err != nil {
		return result, fmt.Errorf("resolve staging root: %w", err)
	}
	staging, err = resolveProjectedPath(staging)
	if err != nil {
		return result, fmt.Errorf("resolve staging root links: %w", err)
	}
	storeRoot, err := resolveProjectedPath(store.Root())
	if err != nil {
		return result, fmt.Errorf("resolve evidence root links: %w", err)
	}
	if pathsOverlap(staging, storeRoot) {
		return result, errors.New("OpenCode raw staging and the evidence root must be separate directories")
	}
	if err := rejectGitPath(staging); err != nil {
		return result, err
	}
	exportsRoot := filepath.Join(staging, "exports")
	metadataRoot := filepath.Join(staging, "metadata")
	logsRoot := filepath.Join(staging, "logs")
	for _, directory := range []string{exportsRoot, metadataRoot, logsRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return result, fmt.Errorf("create staging directory: %w", err)
		}
	}

	var listOut, listErr bytes.Buffer
	listRunErr := options.Runner.Run(ctx, &listOut, &listErr, options.Binary,
		"session", "list", "--format", "json")
	if ctx.Err() != nil {
		listRunErr = ctx.Err()
	}
	listRef, err := writeArtifact(metadataRoot, "session-list", ".json", listOut.Bytes())
	if err != nil {
		return result, err
	}
	listErrRef, err := writeArtifact(logsRoot, "session-list", ".stderr.log", listErr.Bytes())
	if err != nil {
		return result, err
	}
	if listRunErr != nil {
		failures := []captureFailure{{
			SessionID: "global-opencode-capture", Operation: "opencode session list",
			Reason:       "session_list_" + commandErrorClass(listRunErr),
			StdoutSHA256: listRef.SHA256, StderrSHA256: listErrRef.SHA256,
		}}
		return finishFailedCapture(store, staging, options.Now(), result, nil, failures,
			fmt.Errorf("OpenCode session list failed: %w", listRunErr))
	}

	sessions, err := decodeSessionList(listOut.Bytes())
	if err != nil {
		failures := []captureFailure{{
			SessionID: "global-opencode-capture", Operation: "opencode session list",
			Reason:       "session_list_invalid_json",
			StdoutSHA256: listRef.SHA256, StderrSHA256: listErrRef.SHA256,
		}}
		return finishFailedCapture(store, staging, options.Now(), result, nil, failures, err)
	}
	result.SessionsListed = len(sessions)
	manifest := captureManifest{
		SchemaVersion: "opencode-capture-manifest/v1alpha1", CapturedAt: options.Now().UTC(),
		Sessions: len(sessions), Exports: []captureExport{}, Failures: []captureFailure{},
	}

	for index, session := range sessions {
		exportRecord, failure, captureErr := captureSession(
			ctx, options.Runner, options.Binary, session.ID, exportsRoot, logsRoot,
		)
		if captureErr != nil && failure == nil {
			failure = &captureFailure{
				SessionID: session.ID, Operation: "opencode export",
				Reason:       "export_capture_internal_error",
				StdoutSHA256: exportRecord.ExportSHA256, StderrSHA256: exportRecord.StderrSHA256,
			}
		}
		manifest.Exports = append(manifest.Exports, exportRecord)
		if exportRecord.ExportSHA256 != "" {
			result.ExportsWritten++
		}
		switch exportRecord.Status {
		case "complete":
			result.ExportsSucceeded++
		case "partial":
			result.ExportsPartial++
		default:
			result.ExportsFailed++
		}
		if failure != nil {
			manifest.Failures = append(manifest.Failures, *failure)
		}
		if captureErr != nil && errors.Is(captureErr, context.Canceled) {
			for _, remaining := range sessions[index+1:] {
				manifest.Exports = append(manifest.Exports, captureExport{
					SessionID: remaining.ID, Status: "not_attempted",
				})
				manifest.Failures = append(manifest.Failures, captureFailure{
					SessionID: remaining.ID, Operation: "opencode export",
					Reason: "export_not_attempted_capture_canceled",
				})
				result.ExportsFailed++
			}
			break
		}
	}

	var importErr error
	if result.ExportsWritten > 0 {
		result.Import, importErr = ImportPath(store, exportsRoot, Options{Now: options.Now})
		if importErr != nil {
			importErrorRef, artifactErr := writeArtifact(logsRoot, "import", ".stderr.log",
				[]byte(importErr.Error()+"\n"))
			if artifactErr != nil {
				return result, errors.Join(importErr, artifactErr)
			}
			manifest.Failures = append(manifest.Failures, captureFailure{
				SessionID: "global-opencode-capture", Operation: "agentmem import opencode-export",
				Reason: "export_import_failed", StderrSHA256: importErrorRef.SHA256,
			})
		}
	}
	if _, err := writeManifest(metadataRoot, manifest); err != nil {
		return result, errors.Join(importErr, err)
	}
	result.GapsAppended, err = appendCaptureGaps(store, manifest.Failures, options.Now())
	if err != nil {
		return result, errors.Join(importErr, err)
	}
	result.Metadata, err = captureStagingMetadata(store, staging, options.Now)
	if err != nil {
		return result, errors.Join(importErr, err)
	}
	if importErr != nil {
		return result, importErr
	}
	if len(manifest.Failures) > 0 {
		return result, fmt.Errorf("OpenCode capture completed with %d incomplete operations", len(manifest.Failures))
	}
	return result, nil
}

func captureSession(
	ctx context.Context, runner commandRunner, binary, sessionID, exportsRoot, logsRoot string,
) (captureExport, *captureFailure, error) {
	prefix := "session-" + shortHash(sessionID)
	stdout, err := os.CreateTemp(exportsRoot, prefix+"-*.json.tmp")
	if err != nil {
		return captureExport{SessionID: sessionID, Status: "failed"}, nil, err
	}
	stdoutPath := stdout.Name()
	cleanup := func() { _ = os.Remove(stdoutPath) }
	var stderr bytes.Buffer
	runErr := runner.Run(ctx, stdout, &stderr, binary, "export", sessionID)
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	syncErr := stdout.Sync()
	closeErr := stdout.Close()
	if syncErr != nil || closeErr != nil {
		cleanup()
		return captureExport{SessionID: sessionID, Status: "failed"}, nil,
			errors.New("persist OpenCode export output")
	}
	exportRef, err := commitTempArtifact(stdoutPath, exportsRoot, prefix, ".json")
	if err != nil {
		cleanup()
		return captureExport{SessionID: sessionID, Status: "failed"}, nil, err
	}
	stderrRef, err := writeArtifact(logsRoot, prefix, ".stderr.log", stderr.Bytes())
	if err != nil {
		return captureExport{SessionID: sessionID, Status: "failed"}, nil, err
	}
	record := captureExport{
		SessionID: sessionID, Status: "complete", ExportSHA256: exportRef.SHA256,
		StderrSHA256: stderrRef.SHA256,
	}
	if runErr != nil {
		record.Status = "failed"
		failure := &captureFailure{
			SessionID: sessionID, Operation: "opencode export",
			Reason:       "export_" + commandErrorClass(runErr),
			StdoutSHA256: exportRef.SHA256, StderrSHA256: stderrRef.SHA256,
		}
		return record, failure, runErr
	}
	data, err := os.ReadFile(exportRef.Path)
	if err != nil {
		return captureExport{SessionID: sessionID, Status: "failed"}, nil, err
	}
	reason := validateExportForSession(data, sessionID)
	if reason != "" {
		record.Status = "partial"
		failure := &captureFailure{
			SessionID: sessionID, Operation: "opencode export", Reason: reason,
			StdoutSHA256: exportRef.SHA256,
			StderrSHA256: stderrRef.SHA256,
		}
		return record, failure, nil
	}
	return record, nil, nil
}

func decodeSessionList(data []byte) ([]sessionListEntry, error) {
	var sessions []sessionListEntry
	if err := json.Unmarshal(data, &sessions); err != nil {
		var wrapper struct {
			Sessions json.RawMessage `json:"sessions"`
		}
		if wrapperErr := json.Unmarshal(data, &wrapper); wrapperErr != nil ||
			len(wrapper.Sessions) == 0 || json.Unmarshal(wrapper.Sessions, &sessions) != nil {
			return nil, errors.New("OpenCode session list is not valid JSON")
		}
	}
	seen := map[string]struct{}{}
	filtered := make([]sessionListEntry, 0, len(sessions))
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) == "" {
			return nil, errors.New("OpenCode session list contains an entry without id")
		}
		if _, duplicate := seen[session.ID]; duplicate {
			continue
		}
		seen[session.ID] = struct{}{}
		filtered = append(filtered, session)
	}
	return filtered, nil
}

func validateExportForSession(data []byte, sessionID string) string {
	if !json.Valid(data) {
		return "export_invalid_json"
	}
	var document struct {
		Info struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Directory string `json:"directory"`
		} `json:"info"`
	}
	if json.Unmarshal(data, &document) != nil || document.Info.ID == "" {
		return "export_schema_invalid"
	}
	if document.Info.ID != sessionID {
		return "export_session_mismatch"
	}
	if document.Info.Title == fmt.Sprintf("[redacted:session-title:%s]", sessionID) ||
		document.Info.Directory == fmt.Sprintf("[redacted:session-directory:%s]", sessionID) {
		return "export_sanitized"
	}
	return ""
}

type artifactRef struct {
	Path   string
	SHA256 string
}

func writeArtifact(directory, prefix, extension string, data []byte) (artifactRef, error) {
	temp, err := os.CreateTemp(directory, prefix+"-*.tmp")
	if err != nil {
		return artifactRef{}, fmt.Errorf("create artifact temp: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return artifactRef{}, fmt.Errorf("write artifact: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return artifactRef{}, fmt.Errorf("sync artifact: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return artifactRef{}, fmt.Errorf("close artifact: %w", err)
	}
	return commitTempArtifact(tempPath, directory, prefix, extension)
}

func commitTempArtifact(tempPath, directory, prefix, extension string) (artifactRef, error) {
	file, err := os.Open(tempPath)
	if err != nil {
		return artifactRef{}, err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		_ = file.Close()
		return artifactRef{}, err
	}
	if err := file.Close(); err != nil {
		return artifactRef{}, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	target := filepath.Join(directory, prefix+"-"+digest+extension)
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(tempPath)
		return artifactRef{Path: target, SHA256: digest}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return artifactRef{}, err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return artifactRef{}, fmt.Errorf("commit artifact: %w", err)
	}
	return artifactRef{Path: target, SHA256: digest}, nil
}

func writeManifest(directory string, manifest captureManifest) (artifactRef, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return artifactRef{}, err
	}
	data = append(data, '\n')
	return writeArtifact(directory, "capture-manifest", ".json", data)
}

func captureStagingMetadata(
	store *ledger.Store, staging string, now func() time.Time,
) (filesnapshot.Result, error) {
	return filesnapshot.CapturePath(store, staging, filesnapshot.Options{Now: now}, filesnapshot.Spec{
		Agent: ledger.AgentOpenCode, AdapterName: CaptureAdapterName,
		AdapterVersion: CaptureAdapterVersion, IDNamespace: "opencode-cli-capture",
		Include: func(relativePath string, _ os.DirEntry) bool {
			return !strings.HasPrefix(filepath.ToSlash(relativePath), "exports/")
		},
		ThreadID: func(_, _ string) string { return "global-opencode-capture" },
		Kind:     func(string) ledger.EventKind { return ledger.KindSourceSnapshot },
	})
}

func appendCaptureGaps(store *ledger.Store, failures []captureFailure, now time.Time) (int, error) {
	if len(failures) == 0 {
		return 0, nil
	}
	known := map[string]struct{}{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		known[record.Event.EventID] = struct{}{}
		return nil
	})
	if err != nil {
		return 0, err
	}
	defer appender.Close()
	pending := make([]ledger.Event, 0, len(failures))
	for _, failure := range failures {
		payloadJSON, err := json.Marshal(failure)
		if err != nil {
			return 0, err
		}
		eventID := adapterjsonl.DeterministicID("opencode-cli-capture-gap",
			failure.SessionID, failure.Operation, failure.Reason,
			failure.StdoutSHA256, failure.StderrSHA256)
		if _, exists := known[eventID]; exists {
			continue
		}
		threadID := failure.SessionID
		if threadID == "" {
			threadID = "global-opencode-capture"
		}
		payload := ledger.InlinePayload("json", "application/json", string(payloadJSON))
		operation := failure.Operation
		if operation == "" {
			operation = "opencode export"
		}
		pending = append(pending, ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindGap,
			ObservedAt: now.UTC(), RecordedAt: now.UTC(),
			Source: ledger.Source{
				Agent: ledger.AgentOpenCode, Adapter: CaptureAdapterName,
				AdapterVersion: CaptureAdapterVersion, DeviceID: store.DeviceID(),
				OS: runtime.GOOS, ThreadID: threadID, SessionID: failure.SessionID,
				SourceCursor: "command:" + operation,
			},
			Payload: &payload,
			Completeness: ledger.Completeness{Status: ledger.CompletenessPartial,
				Reason: failure.Reason},
			Privacy: ledger.Privacy{Classification: "local_only"},
		})
		known[eventID] = struct{}{}
	}
	if _, err := appender.AppendBatch(pending); err != nil {
		return 0, err
	}
	if err := appender.Close(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

func finishFailedCapture(
	store *ledger.Store, staging string, now time.Time, result CaptureResult,
	exports []captureExport, failures []captureFailure, captureErr error,
) (CaptureResult, error) {
	manifest := captureManifest{
		SchemaVersion: "opencode-capture-manifest/v1alpha1", CapturedAt: now.UTC(),
		Sessions: result.SessionsListed, Exports: exports, Failures: failures,
	}
	if _, err := writeManifest(filepath.Join(staging, "metadata"), manifest); err != nil {
		return result, err
	}
	var err error
	result.Metadata, err = captureStagingMetadata(store, staging, func() time.Time { return now })
	if err != nil {
		return result, err
	}
	result.GapsAppended, err = appendCaptureGaps(store, failures, now)
	if err != nil {
		return result, err
	}
	return result, captureErr
}

func rejectGitPath(path string) error {
	current := filepath.Clean(path)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return errors.New("OpenCode raw staging must be outside a Git worktree")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect staging ancestors: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func resolveProjectedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missing := []string{}
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(absolute), nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func pathWithin(path, parent string) bool {
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
		parent = strings.ToLower(parent)
	}
	relative, err := filepath.Rel(parent, path)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func commandErrorClass(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Sprintf("exit_%d", exitError.ExitCode())
	}
	var execError *exec.Error
	if errors.As(err, &execError) {
		return "executable_unavailable"
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return "executable_unavailable"
	}
	return "command_failed"
}
