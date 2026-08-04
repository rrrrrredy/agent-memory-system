// Package adapterjsonl implements exact-byte, append-only capture for agents
// whose local history is exposed as newline-delimited JSON. Agent adapters own
// discovery and interpretation; this package owns preservation and replay.
package adapterjsonl

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type Options struct {
	FullReconcile bool
	Now           func() time.Time
}

type Result struct {
	FilesExamined  int            `json:"files_examined"`
	FilesChanged   int            `json:"files_changed"`
	SourceSegments int            `json:"source_segments"`
	EventsAppended int            `json:"events_appended"`
	EventsSkipped  int            `json:"events_skipped"`
	GapsAppended   int            `json:"gaps_appended"`
	BytesCaptured  int64          `json:"bytes_captured"`
	Kinds          map[string]int `json:"kinds"`
}

// Projection describes one normalized event derived from a preserved source
// line. Multiple projections may point at the same exact source bytes.
type Projection struct {
	Key                string
	Kind               ledger.EventKind
	ObservedAt         time.Time
	Reasoning          *ledger.ReasoningCapture
	SourceEventID      string
	SessionID          string
	CallID             string
	CompactionID       string
	CompletenessReason string
}

type Spec struct {
	Agent            ledger.Agent
	AdapterName      string
	AdapterVersion   string
	IDNamespace      string
	MediaType        string
	NoFilesError     string
	MatchFile        func(path string, entry fs.DirEntry) bool
	DiscoverThreadID func(path, sourcePathHash string) (string, error)
	Project          func(raw json.RawMessage) []Projection
}

type importState struct {
	knownIDs        map[string]struct{}
	committedOffset map[string]int64
}

type eventPointer struct {
	SourceSegmentEventID string `json:"source_segment_event_id"`
	ByteStart            int64  `json:"byte_start"`
	ByteEnd              int64  `json:"byte_end"`
}

func ImportPath(store *ledger.Store, sourcePath string, options Options, spec Spec) (Result, error) {
	result := Result{Kinds: map[string]int{}}
	if err := validateInputs(store, sourcePath, &options, spec); err != nil {
		return result, err
	}
	files, err := collectFiles(sourcePath, spec.MatchFile)
	if err != nil {
		return result, err
	}
	if len(files) == 0 {
		message := spec.NoFilesError
		if message == "" {
			message = "no matching JSONL files found"
		}
		return result, errors.New(message)
	}
	state, appender, err := loadImportState(store, spec)
	if err != nil {
		return result, err
	}
	for _, path := range files {
		result.FilesExamined++
		if err := importFile(store, appender, path, options, spec, state, &result); err != nil {
			_ = appender.Close()
			return result, fmt.Errorf("import %s JSONL: %w", spec.Agent, err)
		}
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func validateInputs(store *ledger.Store, sourcePath string, options *Options, spec Spec) error {
	if store == nil {
		return errors.New("store is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return errors.New("source path is required")
	}
	if spec.Agent == "" || spec.AdapterName == "" || spec.AdapterVersion == "" ||
		spec.IDNamespace == "" || spec.DiscoverThreadID == nil || spec.Project == nil {
		return errors.New("JSONL adapter spec is incomplete")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return nil
}

func collectFiles(sourcePath string, match func(string, fs.DirEntry) bool) ([]string, error) {
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect source path: %w", err)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, errors.New("source path is not a regular file")
		}
		return []string{absolute}, nil
	}

	var files []string
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if match != nil && match(path, entry) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk source path: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func loadImportState(store *ledger.Store, spec Spec) (*importState, *ledger.Appender, error) {
	state := &importState{
		knownIDs:        map[string]struct{}{},
		committedOffset: map[string]int64{},
	}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		event := record.Event
		state.knownIDs[event.EventID] = struct{}{}
		if event.Source.Agent != spec.Agent || event.Source.Adapter != spec.AdapterName ||
			event.Source.SourcePathHash == "" || event.Source.ByteEnd == nil ||
			event.Kind == ledger.KindSourceSnapshot {
			return nil
		}
		committed := event.Kind != ledger.KindGap ||
			strings.HasPrefix(event.Completeness.Reason, "invalid_json_line_terminated")
		if committed && *event.Source.ByteEnd > state.committedOffset[event.Source.SourcePathHash] {
			state.committedOffset[event.Source.SourcePathHash] = *event.Source.ByteEnd
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("load %s import state: %w", spec.Agent, err)
	}
	return state, appender, nil
}

func importFile(
	store *ledger.Store,
	appender *ledger.Appender,
	path string,
	options Options,
	spec Spec,
	state *importState,
	result *Result,
) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	sourcePathHash, err := HashSourcePath(path)
	if err != nil {
		return err
	}
	threadID, err := spec.DiscoverThreadID(path, sourcePathHash)
	if err != nil {
		return err
	}

	pending := make([]ledger.Event, 0, 256)
	committed := state.committedOffset[sourcePathHash]
	if committed > info.Size() {
		queueTruncationGap(store, state, &pending, spec, sourcePathHash, threadID,
			committed, info.Size(), info.ModTime(), options.Now(), result)
		committed = 0
		state.committedOffset[sourcePathHash] = 0
	}

	start := committed
	if options.FullReconcile {
		start = 0
	}
	if start == info.Size() {
		if len(pending) > 0 {
			result.FilesChanged++
			if _, err := appender.AppendBatch(pending); err != nil {
				return fmt.Errorf("commit evidence batch: %w", err)
			}
		}
		return nil
	}
	if start > info.Size() {
		return fmt.Errorf("invalid import offset %d for %d-byte source", start, info.Size())
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer file.Close()

	segmentLength := info.Size() - start
	blob, err := store.PutBlob(io.NewSectionReader(file, start, segmentLength))
	if err != nil {
		return fmt.Errorf("preserve source segment: %w", err)
	}
	capturedEnd := start + blob.Bytes
	segmentID := DeterministicID(spec.IDNamespace+"-segment", sourcePathHash,
		strconv.FormatInt(start, 10), strconv.FormatInt(capturedEnd, 10), blob.SHA256)
	startValue, endValue := start, capturedEnd
	segmentCompleteness := ledger.Completeness{Status: ledger.CompletenessComplete}
	if blob.Bytes != segmentLength {
		expectedBytes, capturedBytes := segmentLength, blob.Bytes
		segmentCompleteness = ledger.Completeness{
			Status: ledger.CompletenessPartial, Reason: "source_changed_during_capture: captured fewer bytes than the initial source size",
			ExpectedBytes: &expectedBytes, CapturedBytes: &capturedBytes,
		}
	}
	mediaType := spec.MediaType
	if mediaType == "" {
		mediaType = "application/x-ndjson"
	}
	segmentEvent := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: segmentID, Kind: ledger.KindSourceSnapshot,
		ObservedAt: ObservedFileTime(info.ModTime(), options.Now()), RecordedAt: options.Now().UTC(),
		Source: ledger.Source{
			Agent: spec.Agent, Adapter: spec.AdapterName, AdapterVersion: spec.AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SourcePathHash: sourcePathHash, SourceCursor: fmt.Sprintf("bytes:%d-%d", start, capturedEnd),
			ByteStart: &startValue, ByteEnd: &endValue,
		},
		Payload:      &ledger.Payload{Encoding: "binary", MediaType: mediaType, Blob: &blob, SHA256: blob.SHA256, Bytes: blob.Bytes},
		Completeness: segmentCompleteness, Privacy: ledger.Privacy{Classification: "local_only"},
	}
	if _, exists := state.knownIDs[segmentID]; exists {
		result.EventsSkipped++
	} else {
		pending = append(pending, segmentEvent)
		state.knownIDs[segmentID] = struct{}{}
		result.SourceSegments++
	}
	result.FilesChanged++
	result.BytesCaptured += blob.Bytes

	preserved, err := store.OpenBlob(blob)
	if err != nil {
		return fmt.Errorf("open preserved source segment: %w", err)
	}
	defer preserved.Close()
	reader := bufio.NewReader(preserved)
	offset := start
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineStart := offset
			lineEnd := offset + int64(len(line))
			terminated := line[len(line)-1] == '\n'
			offset = lineEnd
			trimmed := bytes.TrimSpace(line)
			if !json.Valid(trimmed) {
				reason := "invalid_json_line_terminated: source record is not valid JSON"
				if !terminated && errors.Is(readErr, io.EOF) {
					reason = "trailing_partial_json: source may still be writing; retry from this byte"
				}
				queueProjection(store, state, &pending, spec, segmentID, sourcePathHash, threadID,
					lineStart, lineEnd, line, info.ModTime(), options.Now(),
					Projection{Kind: ledger.KindGap, CompletenessReason: reason}, result)
				if terminated {
					state.committedOffset[sourcePathHash] = lineEnd
				}
			} else {
				projections := spec.Project(json.RawMessage(trimmed))
				if len(projections) == 0 {
					projections = []Projection{{Kind: ledger.KindUnknown,
						CompletenessReason: "valid_json_without_projection: exact source bytes preserved"}}
				}
				keys := uniqueProjectionKeys(projections)
				for index, projection := range projections {
					projection.Key = keys[index]
					observedAt := projection.ObservedAt
					if observedAt.IsZero() {
						observedAt = ObservedFileTime(info.ModTime(), options.Now())
						if projection.CompletenessReason == "" {
							projection.CompletenessReason = "timestamp_missing_or_invalid: used source modification time"
						}
					}
					queueProjection(store, state, &pending, spec, segmentID, sourcePathHash, threadID,
						lineStart, lineEnd, line, observedAt, options.Now(), projection, result)
				}
				state.committedOffset[sourcePathHash] = lineEnd
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read preserved source segment: %w", readErr)
		}
	}
	if _, err := appender.AppendBatch(pending); err != nil {
		return fmt.Errorf("commit evidence batch: %w", err)
	}
	return nil
}

func uniqueProjectionKeys(projections []Projection) []string {
	keys := make([]string, len(projections))
	used := map[string]int{}
	for index, projection := range projections {
		key := projection.Key
		if len(projections) == 1 && key == "" {
			keys[index] = ""
			continue
		}
		if key == "" {
			key = "projection-" + strconv.Itoa(index)
		}
		count := used[key]
		used[key]++
		if count > 0 {
			key += "-duplicate-" + strconv.Itoa(count)
		}
		keys[index] = key
	}
	return keys
}

func queueProjection(
	store *ledger.Store, state *importState, pending *[]ledger.Event, spec Spec,
	segmentID, sourcePathHash, threadID string, byteStart, byteEnd int64,
	rawLine []byte, observedAt, recordedAt time.Time, projection Projection, result *Result,
) {
	rawDigest := sha256.Sum256(rawLine)
	parts := []string{sourcePathHash, threadID, strconv.FormatInt(byteStart, 10),
		strconv.FormatInt(byteEnd, 10), hex.EncodeToString(rawDigest[:])}
	if projection.Key != "" {
		parts = append(parts, projection.Key)
	}
	eventID := DeterministicID(spec.IDNamespace+"-event", parts...)
	if _, exists := state.knownIDs[eventID]; exists {
		result.EventsSkipped++
		return
	}
	pointerJSON, _ := json.Marshal(eventPointer{SourceSegmentEventID: segmentID, ByteStart: byteStart, ByteEnd: byteEnd})
	pointer := ledger.InlinePayload("json", "application/json", string(pointerJSON))
	startValue, endValue := byteStart, byteEnd
	status := ledger.CompletenessComplete
	if projection.CompletenessReason != "" {
		status = ledger.CompletenessPartial
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: projection.Kind,
		ObservedAt: observedAt.UTC(), RecordedAt: recordedAt.UTC(),
		Source: ledger.Source{
			Agent: spec.Agent, Adapter: spec.AdapterName, AdapterVersion: spec.AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SessionID: projection.SessionID, SourceEventID: projection.SourceEventID,
			SourcePathHash: sourcePathHash, SourceCursor: fmt.Sprintf("bytes:%d-%d", byteStart, byteEnd),
			ByteStart: &startValue, ByteEnd: &endValue,
		},
		Payload: &pointer, Reasoning: projection.Reasoning,
		Completeness: ledger.Completeness{Status: status, Reason: projection.CompletenessReason},
		Causality: &ledger.Causality{ParentEventIDs: []string{segmentID}, CallID: projection.CallID,
			CompactionID: projection.CompactionID},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	state.knownIDs[eventID] = struct{}{}
	result.EventsAppended++
	result.Kinds[string(projection.Kind)]++
	if projection.Kind == ledger.KindGap {
		result.GapsAppended++
	}
}

func queueTruncationGap(
	store *ledger.Store, state *importState, pending *[]ledger.Event, spec Spec,
	sourcePathHash, threadID string, previousOffset, currentSize int64,
	modifiedAt, recordedAt time.Time, result *Result,
) {
	eventID := DeterministicID(spec.IDNamespace+"-truncation", sourcePathHash,
		strconv.FormatInt(previousOffset, 10), strconv.FormatInt(currentSize, 10))
	if _, exists := state.knownIDs[eventID]; exists {
		result.EventsSkipped++
		return
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindGap,
		ObservedAt: ObservedFileTime(modifiedAt, recordedAt), RecordedAt: recordedAt.UTC(),
		Source: ledger.Source{
			Agent: spec.Agent, Adapter: spec.AdapterName, AdapterVersion: spec.AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SourcePathHash: sourcePathHash,
			SourceCursor:   fmt.Sprintf("truncated:%d-to-%d", previousOffset, currentSize),
		},
		Completeness: ledger.Completeness{Status: ledger.CompletenessPartial,
			Reason: "source_truncated: previously committed bytes are no longer present at this path"},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	state.knownIDs[eventID] = struct{}{}
	result.EventsAppended++
	result.GapsAppended++
	result.Kinds[string(ledger.KindGap)]++
}

func HashSourcePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	normalized := filepath.ToSlash(filepath.Clean(absolute))
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:]), nil
}

func DeterministicID(namespace string, parts ...string) string {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, namespace)
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = io.WriteString(hasher, part)
	}
	return namespace + "-" + hex.EncodeToString(hasher.Sum(nil))
}

func ParseObservedAt(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func ObservedFileTime(modifiedAt, fallback time.Time) time.Time {
	if modifiedAt.IsZero() {
		return fallback.UTC()
	}
	return modifiedAt.UTC()
}
