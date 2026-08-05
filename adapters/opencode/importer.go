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
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	AdapterName    = "opencode-session-export"
	AdapterVersion = "opencode-session-export/v1alpha1"
)

type Options struct {
	Now     func() time.Time
	Context context.Context
}

type SourceFile = adapterjsonl.SourceFile

type Result struct {
	FilesExamined   int            `json:"files_examined"`
	FilesChanged    int            `json:"files_changed"`
	SourceSnapshots int            `json:"source_snapshots"`
	EventsAppended  int            `json:"events_appended"`
	EventsSkipped   int            `json:"events_skipped"`
	GapsAppended    int            `json:"gaps_appended"`
	BytesCaptured   int64          `json:"bytes_captured"`
	Kinds           map[string]int `json:"kinds"`
}

type exportDocument struct {
	Info     json.RawMessage `json:"info"`
	Messages []exportMessage `json:"messages"`
}

type exportMessage struct {
	Info  json.RawMessage   `json:"info"`
	Parts []json.RawMessage `json:"parts"`
}

type sessionInfo struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Time     struct {
		Created    int64 `json:"created"`
		Updated    int64 `json:"updated"`
		Compacting int64 `json:"compacting"`
	} `json:"time"`
}

type messageInfo struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	Role      string          `json:"role"`
	Summary   json.RawMessage `json:"summary"`
	Time      struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

type partInfo struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionID"`
	MessageID string          `json:"messageID"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	CallID    string          `json:"callID"`
	Metadata  json.RawMessage `json:"metadata"`
	State     json.RawMessage `json:"state"`
	Time      struct {
		Start   int64 `json:"start"`
		End     int64 `json:"end"`
		Created int64 `json:"created"`
	} `json:"time"`
}

type toolState struct {
	Status string `json:"status"`
}

type projection struct {
	key                string
	kind               ledger.EventKind
	pointer            string
	raw                json.RawMessage
	observedAt         time.Time
	sourceEventID      string
	callID             string
	compactionID       string
	reasoning          *ledger.ReasoningCapture
	completenessReason string
}

type documentPointer struct {
	SourceSnapshotEventID string `json:"source_snapshot_event_id"`
	JSONPointer           string `json:"json_pointer"`
}

func ImportPath(store *ledger.Store, sourcePath string, options Options) (Result, error) {
	result := Result{Kinds: map[string]int{}}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(sourcePath) == "" {
		return result, errors.New("source path is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	files, err := collectExports(options.Context, sourcePath)
	if err != nil {
		return result, err
	}
	if len(files) == 0 {
		return result, errors.New("no OpenCode session export JSON files found")
	}
	sources := make([]SourceFile, 0, len(files))
	for _, path := range files {
		sources = append(sources, SourceFile{Path: path})
	}
	return ImportSources(store, sources, options)
}

func ImportSources(store *ledger.Store, sources []SourceFile, options Options) (Result, error) {
	result := Result{Kinds: map[string]int{}}
	if store == nil {
		return result, errors.New("store is required")
	}
	if len(sources) == 0 {
		return result, errors.New("at least one source file is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	validated, err := adapterjsonl.ValidateSources(sources)
	if err != nil {
		return result, err
	}

	known := map[string]struct{}{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		if err := options.Context.Err(); err != nil {
			return err
		}
		known[record.Event.EventID] = struct{}{}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("load OpenCode import state: %w", err)
	}
	for _, source := range validated {
		result.FilesExamined++
		if err := importFile(store, appender, source, options, known, &result); err != nil {
			_ = appender.Close()
			return result, fmt.Errorf("import OpenCode export: %w", err)
		}
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	return result, nil
}

func collectExports(ctx context.Context, sourcePath string) ([]string, error) {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
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

func importFile(
	store *ledger.Store,
	appender *ledger.Appender,
	source SourceFile,
	options Options,
	known map[string]struct{},
	result *Result,
) error {
	if err := options.Context.Err(); err != nil {
		return err
	}
	path := source.Path
	before, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect export: %w", err)
	}
	acquisitionPathHash, err := adapterjsonl.HashSourcePath(path)
	if err != nil {
		return err
	}
	pathHash := acquisitionPathHash
	if source.LogicalSourcePathSHA256 != "" {
		pathHash = source.LogicalSourcePathSHA256
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open export: %w", err)
	}
	blob, captureErr := store.PutBlob(&contextReader{ctx: options.Context, reader: file})
	closeErr := file.Close()
	if captureErr != nil {
		return fmt.Errorf("preserve export: %w", captureErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close export: %w", closeErr)
	}
	if source.ExpectedContentSHA256 != "" && blob.SHA256 != source.ExpectedContentSHA256 {
		return errors.New("recovered source content digest does not match the manifest")
	}
	if source.ExpectedBytes != nil && blob.Bytes != *source.ExpectedBytes {
		return errors.New("recovered source byte count does not match the manifest")
	}
	result.BytesCaptured += blob.Bytes
	preserved, err := store.OpenBlob(blob)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(&contextReader{ctx: options.Context, reader: preserved})
	closeErr = preserved.Close()
	if readErr != nil {
		return fmt.Errorf("read preserved export: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close preserved export: %w", closeErr)
	}

	var document exportDocument
	parseErr := json.Unmarshal(data, &document)
	var info sessionInfo
	if parseErr == nil {
		parseErr = json.Unmarshal(document.Info, &info)
	}
	threadID := info.ID
	if threadID == "" {
		threadID = "unknown-" + pathHash[:16]
	}
	if source.ExpectedThreadID != "" && threadID != source.ExpectedThreadID {
		return errors.New("recovered source thread identity does not match the manifest")
	}
	snapshotID := adapterjsonl.DeterministicID("opencode-export-snapshot", pathHash, blob.SHA256)
	completeness := ledger.Completeness{Status: ledger.CompletenessComplete}
	after, statErr := os.Stat(path)
	changedDuringCapture := statErr != nil || before.Size() != blob.Bytes ||
		(after != nil && (after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime())))
	sanitized := bytes.Contains(data, []byte("[redacted:")) || bytes.Contains(data, []byte(`"redacted":`))
	var inheritedReason string
	if changedDuringCapture {
		expected, captured := before.Size(), blob.Bytes
		inheritedReason = "source_changed_during_capture: preserved bytes require reconciliation"
		completeness = ledger.Completeness{Status: ledger.CompletenessPartial,
			Reason: inheritedReason, ExpectedBytes: &expected, CapturedBytes: &captured}
	}
	if sanitized {
		inheritedReason = joinReason(inheritedReason,
			"sanitized_export_detected: redacted data cannot satisfy complete-process capture")
		completeness.Status = ledger.CompletenessPartial
		completeness.Reason = inheritedReason
	}
	start, end := int64(0), blob.Bytes
	snapshot := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: snapshotID, Kind: ledger.KindSourceSnapshot,
		ObservedAt: observedTime(info.Time.Updated, before.ModTime(), options.Now()), RecordedAt: options.Now().UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentOpenCode, Adapter: AdapterName, AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SessionID: info.ID, SourcePathHash: pathHash,
			AcquisitionPathHash: relocationHash(pathHash, acquisitionPathHash), SourceCursor: "json:/",
			ByteStart: &start, ByteEnd: &end,
		},
		Payload: &ledger.Payload{Encoding: "binary", MediaType: "application/json", Blob: &blob,
			SHA256: blob.SHA256, Bytes: blob.Bytes},
		Completeness: completeness, Privacy: ledger.Privacy{Classification: "local_only"},
	}
	pending := make([]ledger.Event, 0, 2+len(document.Messages)*4)
	if _, exists := known[snapshotID]; exists {
		result.EventsSkipped++
	} else {
		pending = append(pending, snapshot)
		known[snapshotID] = struct{}{}
		result.SourceSnapshots++
	}

	if parseErr != nil || info.ID == "" {
		reason := "invalid_export_schema: exact JSON document preserved but session info could not be decoded"
		queueGap(store, known, &pending, snapshotID, pathHash, acquisitionPathHash, threadID, reason,
			before.ModTime(), options.Now(), result)
		if len(pending) > 0 {
			result.FilesChanged++
		}
		_, err := appender.AppendBatch(pending)
		return err
	}
	if inheritedReason != "" {
		queueGap(store, known, &pending, snapshotID, pathHash, acquisitionPathHash, threadID, inheritedReason,
			before.ModTime(), options.Now(), result)
	}

	projections := projectDocument(document, info, inheritedReason)
	for _, item := range projections {
		queueProjection(store, known, &pending, snapshotID, pathHash, acquisitionPathHash, threadID, item,
			options.Now(), result)
	}
	if len(pending) > 0 {
		result.FilesChanged++
	}
	if _, err := appender.AppendBatch(pending); err != nil {
		return fmt.Errorf("commit OpenCode export evidence: %w", err)
	}
	return nil
}

func projectDocument(document exportDocument, info sessionInfo, inheritedReason string) []projection {
	result := []projection{{
		key: "session-info", kind: ledger.KindSystemEvent, pointer: "/info", raw: document.Info,
		observedAt: observedTime(info.Time.Updated, time.Time{}, time.Time{}), sourceEventID: info.ID,
		completenessReason: inheritedReason,
	}}
	for messageIndex, message := range document.Messages {
		var messageData messageInfo
		if err := json.Unmarshal(message.Info, &messageData); err != nil {
			result = append(result, projection{
				key: fmt.Sprintf("message-%d-info", messageIndex), kind: ledger.KindUnknown,
				pointer: fmt.Sprintf("/messages/%d/info", messageIndex), raw: message.Info,
				completenessReason: joinReason(inheritedReason,
					"message_info_unreadable: exact source object preserved"),
			})
			continue
		}
		messageKind := ledger.KindSystemEvent
		compactionID := ""
		if hasJSONValue(messageData.Summary) {
			messageKind = ledger.KindCompaction
			compactionID = messageData.ID
		}
		result = append(result, projection{
			key: "message-info", kind: messageKind,
			pointer: fmt.Sprintf("/messages/%d/info", messageIndex), raw: message.Info,
			observedAt:    observedTime(messageData.Time.Created, time.Time{}, time.Time{}),
			sourceEventID: messageData.ID, compactionID: compactionID,
			completenessReason: inheritedReason,
		})
		for partIndex, rawPart := range message.Parts {
			pointer := fmt.Sprintf("/messages/%d/parts/%d", messageIndex, partIndex)
			result = append(result, projectPart(rawPart, pointer, messageData, inheritedReason)...)
		}
	}
	return result
}

func projectPart(raw json.RawMessage, pointer string, message messageInfo, inheritedReason string) []projection {
	var part partInfo
	if err := json.Unmarshal(raw, &part); err != nil {
		return []projection{{
			key: "unreadable", kind: ledger.KindUnknown, pointer: pointer, raw: raw,
			completenessReason: joinReason(inheritedReason,
				"message_part_unreadable: exact source object preserved"),
		}}
	}
	base := projection{
		key: part.Type, pointer: pointer, raw: raw,
		observedAt: observedTime(firstNonZero(part.Time.Start, part.Time.Created, message.Time.Created),
			time.Time{}, time.Time{}),
		sourceEventID: part.ID, callID: part.CallID, completenessReason: inheritedReason,
	}
	switch part.Type {
	case "text":
		base.kind = roleKind(message.Role)
		return []projection{base}
	case "reasoning":
		base.kind = ledger.KindReasoning
		visibility := ledger.ReasoningNotExposed
		if strings.TrimSpace(part.Text) != "" &&
			!strings.Contains(inheritedReason, "sanitized_export_detected") {
			visibility = ledger.ReasoningRawExposed
		}
		base.reasoning = &ledger.ReasoningCapture{
			Visibility: visibility,
			Note:       "OpenCode reasoning part from session export",
		}
		return []projection{base}
	case "tool":
		call := base
		call.key = "tool-call"
		call.kind = ledger.KindToolCall
		projections := []projection{call}
		var state toolState
		if json.Unmarshal(part.State, &state) == nil &&
			(state.Status == "completed" || state.Status == "error") {
			output := base
			output.key = "tool-result"
			output.kind = ledger.KindToolResult
			projections = append(projections, output)
		}
		return projections
	case "file":
		base.kind = ledger.KindAttachment
	case "patch", "snapshot":
		base.kind = ledger.KindFileChange
	case "subtask", "agent":
		base.kind = ledger.KindSubagentEvent
	case "compaction":
		base.kind = ledger.KindCompaction
		base.compactionID = part.ID
	case "step-start", "step-finish", "retry":
		base.kind = ledger.KindSystemEvent
	default:
		base.kind = ledger.KindUnknown
	}
	return []projection{base}
}

func queueProjection(
	store *ledger.Store, known map[string]struct{}, pending *[]ledger.Event,
	snapshotID, pathHash, acquisitionPathHash, threadID string, item projection, recordedAt time.Time, result *Result,
) {
	digest := sha256.Sum256(item.raw)
	identity := item.sourceEventID
	if identity == "" {
		identity = item.pointer
	}
	eventID := adapterjsonl.DeterministicID("opencode-export-event", pathHash, threadID,
		identity, hex.EncodeToString(digest[:]), item.key)
	if _, exists := known[eventID]; exists {
		result.EventsSkipped++
		return
	}
	pointerJSON, _ := json.Marshal(documentPointer{
		SourceSnapshotEventID: snapshotID, JSONPointer: item.pointer,
	})
	payload := ledger.InlinePayload("json", "application/json", string(pointerJSON))
	status := ledger.CompletenessComplete
	if item.completenessReason != "" {
		status = ledger.CompletenessPartial
	}
	observedAt := item.observedAt
	reason := item.completenessReason
	if observedAt.IsZero() {
		observedAt = recordedAt.UTC()
		reason = joinReason(reason, "timestamp_missing_or_invalid: used capture time")
		status = ledger.CompletenessPartial
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: item.kind,
		ObservedAt: observedAt.UTC(), RecordedAt: recordedAt.UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentOpenCode, Adapter: AdapterName, AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SessionID: threadID, SourceEventID: item.sourceEventID,
			SourcePathHash:      pathHash,
			AcquisitionPathHash: relocationHash(pathHash, acquisitionPathHash),
			SourceCursor:        "json:" + item.pointer,
		},
		Payload: &payload, Reasoning: item.reasoning,
		Completeness: ledger.Completeness{Status: status, Reason: reason},
		Causality: &ledger.Causality{ParentEventIDs: []string{snapshotID}, CallID: item.callID,
			CompactionID: item.compactionID},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	known[eventID] = struct{}{}
	result.EventsAppended++
	result.Kinds[string(item.kind)]++
}

func queueGap(
	store *ledger.Store, known map[string]struct{}, pending *[]ledger.Event,
	snapshotID, pathHash, acquisitionPathHash, threadID, reason string, modifiedAt, recordedAt time.Time, result *Result,
) {
	eventID := adapterjsonl.DeterministicID("opencode-export-gap", pathHash, snapshotID, reason)
	if _, exists := known[eventID]; exists {
		result.EventsSkipped++
		return
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindGap,
		ObservedAt: observedTime(0, modifiedAt, recordedAt), RecordedAt: recordedAt.UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentOpenCode, Adapter: AdapterName, AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SessionID: threadID, SourcePathHash: pathHash,
			AcquisitionPathHash: relocationHash(pathHash, acquisitionPathHash), SourceCursor: "json:/",
		},
		Completeness: ledger.Completeness{Status: ledger.CompletenessPartial, Reason: reason},
		Causality:    &ledger.Causality{ParentEventIDs: []string{snapshotID}},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	*pending = append(*pending, event)
	known[eventID] = struct{}{}
	result.EventsAppended++
	result.GapsAppended++
	result.Kinds[string(ledger.KindGap)]++
}

func observedTime(milliseconds int64, modifiedAt, fallback time.Time) time.Time {
	if milliseconds > 0 {
		return time.UnixMilli(milliseconds).UTC()
	}
	if !modifiedAt.IsZero() {
		return modifiedAt.UTC()
	}
	if !fallback.IsZero() {
		return fallback.UTC()
	}
	return time.Time{}
}

func roleKind(role string) ledger.EventKind {
	switch role {
	case "user":
		return ledger.KindUserMessage
	case "assistant":
		return ledger.KindAgentMessage
	default:
		return ledger.KindUnknown
	}
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) &&
		!bytes.Equal(trimmed, []byte("{}"))
}

func relocationHash(logical, acquisition string) string {
	if logical == acquisition {
		return ""
	}
	return acquisition
}

func joinReason(existing, added string) string {
	if existing == "" {
		return added
	}
	if added == "" {
		return existing
	}
	return existing + "; " + added
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
