// Package filesnapshot preserves locally exposed companion artifacts that do
// not have a safe append-only format. Each observed content version becomes a
// content-addressed immutable snapshot in the local evidence ledger.
package filesnapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type Options struct {
	Now func() time.Time
}

type Result struct {
	FilesExamined     int   `json:"files_examined"`
	SnapshotsAppended int   `json:"snapshots_appended"`
	SnapshotsSkipped  int   `json:"snapshots_skipped"`
	GapsAppended      int   `json:"gaps_appended"`
	BytesRead         int64 `json:"bytes_read"`
}

type Spec struct {
	Agent          ledger.Agent
	AdapterName    string
	AdapterVersion string
	IDNamespace    string
	Include        func(relativePath string, entry fs.DirEntry) bool
	ThreadID       func(relativePath, sourcePathHash string) string
	Kind           func(relativePath string) ledger.EventKind
}

type collectionIssue struct {
	path   string
	reason string
}

func CapturePath(store *ledger.Store, root string, options Options, spec Spec) (Result, error) {
	result := Result{}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(root) == "" {
		return result, errors.New("source root is required")
	}
	if spec.Agent == "" || spec.AdapterName == "" || spec.AdapterVersion == "" ||
		spec.IDNamespace == "" || spec.Include == nil || spec.ThreadID == nil || spec.Kind == nil {
		return result, errors.New("file snapshot spec is incomplete")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return result, fmt.Errorf("resolve source root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return result, fmt.Errorf("inspect source root: %w", err)
	}
	if !info.IsDir() {
		return result, errors.New("source root is not a directory")
	}

	files, issues := collect(absoluteRoot, spec.Include)
	known := map[string]struct{}{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		known[record.Event.EventID] = struct{}{}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("load file snapshot state: %w", err)
	}
	defer appender.Close()
	pending := make([]ledger.Event, 0, len(files)+len(issues))

	for _, issue := range issues {
		pathHash, hashErr := adapterjsonl.HashSourcePath(issue.path)
		if hashErr != nil {
			continue
		}
		relative := relativeOrHash(absoluteRoot, issue.path, pathHash)
		queueGap(store, spec, known, &pending, relative, pathHash, issue.reason,
			options.Now(), &result)
	}
	for _, path := range files {
		result.FilesExamined++
		pathHash, err := adapterjsonl.HashSourcePath(path)
		if err != nil {
			return result, err
		}
		relative := relativeOrHash(absoluteRoot, path, pathHash)
		before, err := os.Stat(path)
		if err != nil {
			queueGap(store, spec, known, &pending, relative, pathHash,
				"source_stat_failed: "+errorClass(err), options.Now(), &result)
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			queueGap(store, spec, known, &pending, relative, pathHash,
				"source_open_failed: "+errorClass(err), options.Now(), &result)
			continue
		}
		blob, captureErr := store.PutBlob(file)
		closeErr := file.Close()
		if captureErr != nil {
			queueGap(store, spec, known, &pending, relative, pathHash,
				"source_capture_failed: "+errorClass(captureErr), options.Now(), &result)
			continue
		}
		if closeErr != nil {
			queueGap(store, spec, known, &pending, relative, pathHash,
				"source_close_failed: "+errorClass(closeErr), options.Now(), &result)
		}
		result.BytesRead += blob.Bytes
		after, statErr := os.Stat(path)
		completeness := ledger.Completeness{Status: ledger.CompletenessComplete}
		if statErr != nil || before.Size() != blob.Bytes ||
			(after != nil && (after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()))) {
			expected, captured := before.Size(), blob.Bytes
			completeness = ledger.Completeness{
				Status:        ledger.CompletenessPartial,
				Reason:        "source_changed_during_snapshot: captured bytes retained; reconciliation required",
				ExpectedBytes: &expected, CapturedBytes: &captured,
			}
		}
		eventID := adapterjsonl.DeterministicID(spec.IDNamespace+"-snapshot",
			pathHash, blob.SHA256, fmt.Sprint(blob.Bytes))
		if _, exists := known[eventID]; exists {
			result.SnapshotsSkipped++
			continue
		}
		start, end := int64(0), blob.Bytes
		threadID := spec.ThreadID(relative, pathHash)
		if strings.TrimSpace(threadID) == "" {
			threadID = "unknown-" + pathHash[:16]
		}
		mediaType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		event := ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: spec.Kind(relative),
			ObservedAt: observedFileTime(before.ModTime(), options.Now()), RecordedAt: options.Now().UTC(),
			Source: ledger.Source{
				Agent: spec.Agent, Adapter: spec.AdapterName, AdapterVersion: spec.AdapterVersion,
				DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
				SessionID: sessionID(threadID), SourcePathHash: pathHash,
				SourceCursor: "file:" + filepath.ToSlash(relative), ByteStart: &start, ByteEnd: &end,
			},
			Payload: &ledger.Payload{Encoding: "binary", MediaType: mediaType, Blob: &blob,
				SHA256: blob.SHA256, Bytes: blob.Bytes},
			Completeness: completeness, Privacy: ledger.Privacy{Classification: "local_only"},
		}
		pending = append(pending, event)
		known[eventID] = struct{}{}
		result.SnapshotsAppended++
	}
	if _, err := appender.AppendBatch(pending); err != nil {
		return result, fmt.Errorf("commit file snapshots: %w", err)
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func collect(root string, include func(string, fs.DirEntry) bool) ([]string, []collectionIssue) {
	var files []string
	var issues []collectionIssue
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			issues = append(issues, collectionIssue{path: path,
				reason: "source_walk_failed: " + errorClass(walkErr)})
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			issues = append(issues, collectionIssue{path: path,
				reason: "source_relative_path_failed: " + errorClass(err)})
			return nil
		}
		if include(filepath.ToSlash(relative), entry) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, issues
}

func queueGap(
	store *ledger.Store, spec Spec, known map[string]struct{}, pending *[]ledger.Event,
	relative, pathHash, reason string, recordedAt time.Time, result *Result,
) {
	eventID := adapterjsonl.DeterministicID(spec.IDNamespace+"-gap", pathHash, reason)
	if _, exists := known[eventID]; exists {
		return
	}
	threadID := spec.ThreadID(relative, pathHash)
	if strings.TrimSpace(threadID) == "" {
		threadID = "unknown-" + pathHash[:16]
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindGap,
		ObservedAt: recordedAt.UTC(), RecordedAt: recordedAt.UTC(),
		Source: ledger.Source{
			Agent: spec.Agent, Adapter: spec.AdapterName, AdapterVersion: spec.AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SessionID: sessionID(threadID), SourcePathHash: pathHash,
			SourceCursor: "file:" + filepath.ToSlash(relative),
		},
		Completeness: ledger.Completeness{Status: ledger.CompletenessMissing, Reason: reason},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	known[eventID] = struct{}{}
	result.GapsAppended++
}

func relativeOrHash(root, path, pathHash string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return "unresolved-" + pathHash[:16]
	}
	return filepath.ToSlash(relative)
}

func observedFileTime(modifiedAt, fallback time.Time) time.Time {
	if modifiedAt.IsZero() {
		return fallback.UTC()
	}
	return modifiedAt.UTC()
}

func sessionID(threadID string) string {
	if strings.HasPrefix(threadID, "global-") || strings.HasPrefix(threadID, "unknown-") {
		return ""
	}
	return threadID
}

func errorClass(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "permission_denied"
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "not_found"
	}
	sum := sha256.Sum256([]byte(err.Error()))
	return "other-" + hex.EncodeToString(sum[:8])
}
