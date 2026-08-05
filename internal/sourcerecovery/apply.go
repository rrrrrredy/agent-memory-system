package sourcerecovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeManifest(reader io.Reader) (Manifest, []byte, error) {
	if reader == nil {
		return Manifest{}, nil, errors.New("source recovery manifest reader is required")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("read source recovery manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, nil, fmt.Errorf("decode source recovery manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, nil, errors.New("source recovery manifest contains more than one JSON value")
		}
		return Manifest{}, nil, fmt.Errorf("decode trailing source recovery manifest data: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, nil, err
	}
	if manifest.RecoveryID == "" {
		manifest.RecoveryID, err = RecoveryID(manifest)
		if err != nil {
			return Manifest{}, nil, err
		}
	}
	return manifest, data, nil
}

func OpenManifestFile(path string) (*os.File, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("source recovery manifest path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve source recovery manifest: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect source recovery manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source recovery manifest must be a regular file, not a symbolic link")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve source recovery manifest links: %w", err)
	}
	if err := ensureOutputOutsideGit(resolved); err != nil {
		return nil, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("open source recovery manifest: %w", err)
	}
	return file, nil
}

func Apply(store *ledger.Store, sourceRoot string, reader io.Reader, now time.Time) (Result, error) {
	manifest, rawManifest, err := DecodeManifest(reader)
	if err != nil {
		return Result{SchemaVersion: ResultSchemaVersion, Privacy: "local_only"}, err
	}
	result := Result{
		SchemaVersion: ResultSchemaVersion, RecoveryID: manifest.RecoveryID,
		Agent: manifest.Agent, EntriesChecked: len(manifest.Entries), Privacy: "local_only",
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := validateManifest(manifest); err != nil {
		return result, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	root, err := resolveSourceRoot(sourceRoot)
	if err != nil {
		return result, err
	}
	manifestDigest := sha256.Sum256(rawManifest)
	result.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])
	manifestBlob, err := store.PutBlob(bytes.NewReader(rawManifest))
	if err != nil {
		return result, fmt.Errorf("preserve source recovery manifest: %w", err)
	}

	sources := make([]adapterjsonl.SourceFile, 0, len(manifest.Entries))
	missing := make([]Entry, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.Status == StatusMissing {
			missing = append(missing, entry)
			continue
		}
		path, err := resolveEntry(root, entry.RelativePath)
		if err != nil {
			return result, fmt.Errorf("resolve recovered source %s: %w", entry.LogicalSourcePathSHA256, err)
		}
		sources = append(sources, adapterjsonl.SourceFile{
			Path: path, LogicalSourcePathSHA256: entry.LogicalSourcePathSHA256,
			ExpectedContentSHA256: entry.ExpectedContentSHA256,
			ExpectedBytes:         entry.ExpectedBytes, ExpectedThreadID: entry.ThreadID,
		})
	}

	if len(sources) > 0 {
		switch manifest.Agent {
		case ledger.AgentCodex:
			imported, importErr := codex.ImportSources(store, sources, codex.Options{
				FullReconcile: true, Now: func() time.Time { return now },
			})
			result.Import, err = &imported, importErr
		case ledger.AgentClaudeCode:
			imported, importErr := claudecode.ImportSources(store, sources, claudecode.Options{
				FullReconcile: true, Now: func() time.Time { return now },
			})
			result.Import, err = &imported, importErr
		case ledger.AgentOpenCode:
			imported, importErr := opencode.ImportSources(store, sources, opencode.Options{Now: func() time.Time { return now }})
			result.OpenCodeImport, err = &imported, importErr
		default:
			return result, errors.New("source recovery does not support this agent")
		}
		if err != nil {
			return result, err
		}
		result.SourcesRecovered = len(sources)
	}
	result.SourcesMissing = len(missing)

	manifestEvent, gapEvents := recoveryEvents(store, manifest, manifestBlob, result.ManifestSHA256, missing, now)
	reusedManifest, appendedGaps, reusedGaps, err := appendRecoveryEvents(store, manifestEvent, gapEvents)
	result.ManifestReused = reusedManifest
	result.GapsAppended = appendedGaps
	result.GapsReused = reusedGaps
	return result, err
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported source recovery manifest schema %q", manifest.SchemaVersion)
	}
	if manifest.Agent != ledger.AgentCodex && manifest.Agent != ledger.AgentClaudeCode &&
		manifest.Agent != ledger.AgentOpenCode {
		return errors.New("source recovery manifest agent must be codex, claude_code, or opencode")
	}
	if manifest.CreatedAt.IsZero() || manifest.Privacy != "local_only" || len(manifest.Entries) == 0 {
		return errors.New("source recovery manifest requires created_at, entries, and local_only privacy")
	}
	expectedID, err := RecoveryID(manifest)
	if err != nil {
		return err
	}
	if manifest.RecoveryID != "" && manifest.RecoveryID != expectedID {
		return errors.New("source recovery manifest recovery_id does not match its content")
	}
	previous := ""
	for index, entry := range manifest.Entries {
		if !validSHA256(entry.LogicalSourcePathSHA256) || strings.TrimSpace(entry.ThreadID) == "" {
			return fmt.Errorf("source recovery entry %d has invalid source identity", index)
		}
		if entry.LogicalSourcePathSHA256 <= previous {
			return errors.New("source recovery entries must be unique and sorted by logical source path digest")
		}
		previous = entry.LogicalSourcePathSHA256
		switch entry.Status {
		case StatusAvailable:
			if !validRelativePath(entry.RelativePath) || !validSHA256(entry.ExpectedContentSHA256) ||
				entry.ExpectedBytes == nil || *entry.ExpectedBytes < 0 || entry.Reason != "" {
				return fmt.Errorf("available source recovery entry %d is incomplete", index)
			}
		case StatusMissing:
			if entry.RelativePath != "" || entry.ExpectedContentSHA256 != "" || entry.ExpectedBytes != nil ||
				!validReason(entry.Reason) {
				return fmt.Errorf("missing source recovery entry %d is invalid", index)
			}
		default:
			return fmt.Errorf("source recovery entry %d has unsupported status %q", index, entry.Status)
		}
	}
	return nil
}

func RecoveryID(manifest Manifest) (string, error) {
	payload := struct {
		SchemaVersion string       `json:"schema_version"`
		CreatedAt     time.Time    `json:"created_at"`
		Agent         ledger.Agent `json:"agent"`
		Entries       []Entry      `json:"entries"`
		Privacy       string       `json:"privacy"`
	}{manifest.SchemaVersion, manifest.CreatedAt.UTC(), manifest.Agent, manifest.Entries, manifest.Privacy}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode source recovery identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "source-recovery-" + hex.EncodeToString(digest[:]), nil
}

func recoveryEvents(
	store *ledger.Store, manifest Manifest, manifestBlob ledger.BlobRef, manifestSHA256 string,
	missing []Entry, now time.Time,
) (ledger.Event, []ledger.Event) {
	manifestEventID := "source-recovery-manifest-" + manifestSHA256
	manifestEvent := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: manifestEventID, Kind: ledger.KindSourceSnapshot,
		ObservedAt: manifest.CreatedAt.UTC(), RecordedAt: now.UTC(),
		Source: ledger.Source{
			Agent: manifest.Agent, Adapter: AdapterName, AdapterVersion: AdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID:     "global-" + strings.ReplaceAll(string(manifest.Agent), "_", "-"),
			SourceCursor: "manifest:" + manifest.RecoveryID,
		},
		Payload: &ledger.Payload{
			Encoding: "json", MediaType: "application/json", Blob: &manifestBlob,
			SHA256: manifestBlob.SHA256, Bytes: manifestBlob.Bytes,
		},
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	gaps := make([]ledger.Event, 0, len(missing))
	for _, entry := range missing {
		payloadData, _ := json.Marshal(struct {
			RecoveryID              string `json:"recovery_id"`
			LogicalSourcePathSHA256 string `json:"logical_source_path_sha256"`
			Reason                  string `json:"reason"`
		}{manifest.RecoveryID, entry.LogicalSourcePathSHA256, entry.Reason})
		payload := ledger.InlinePayload("json", "application/json", string(payloadData))
		gapID := adapterjsonl.DeterministicID("source-recovery-gap", manifest.RecoveryID,
			string(manifest.Agent), entry.LogicalSourcePathSHA256, entry.ThreadID, entry.Reason)
		gaps = append(gaps, ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: gapID, Kind: ledger.KindGap,
			ObservedAt: manifest.CreatedAt.UTC(), RecordedAt: now.UTC(),
			Source: ledger.Source{
				Agent: manifest.Agent, Adapter: AdapterName, AdapterVersion: AdapterVersion,
				DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: entry.ThreadID,
				SourcePathHash: entry.LogicalSourcePathSHA256, SourceCursor: "recovery:missing",
			},
			Payload:      &payload,
			Completeness: ledger.Completeness{Status: ledger.CompletenessMissing, Reason: entry.Reason},
			Privacy:      ledger.Privacy{Classification: "local_only"},
		})
	}
	return manifestEvent, gaps
}

func appendRecoveryEvents(
	store *ledger.Store, manifestEvent ledger.Event, gaps []ledger.Event,
) (manifestReused bool, gapsAppended, gapsReused int, returnedErr error) {
	wanted := map[string]ledger.Event{manifestEvent.EventID: manifestEvent}
	for _, event := range gaps {
		wanted[event.EventID] = event
	}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		expected, ok := wanted[record.Event.EventID]
		if !ok {
			return nil
		}
		if !sameRecoveryEvent(record.Event, expected) {
			return fmt.Errorf("source recovery event id collision %q", record.Event.EventID)
		}
		if record.Event.EventID == manifestEvent.EventID {
			manifestReused = true
		} else {
			gapsReused++
		}
		delete(wanted, record.Event.EventID)
		return nil
	})
	if err != nil {
		return false, 0, 0, err
	}
	defer func() {
		if closeErr := appender.Close(); closeErr != nil {
			returnedErr = errors.Join(returnedErr, closeErr)
		}
	}()
	pending := make([]ledger.Event, 0, len(wanted))
	for _, event := range wanted {
		pending = append(pending, event)
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].EventID < pending[right].EventID })
	if _, err := appender.AppendBatch(pending); err != nil {
		return manifestReused, 0, gapsReused, err
	}
	for _, event := range pending {
		if event.EventID != manifestEvent.EventID {
			gapsAppended++
		}
	}
	return manifestReused, gapsAppended, gapsReused, nil
}

func sameRecoveryEvent(actual, expected ledger.Event) bool {
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		return false
	}
	expected.RecordedAt = actual.RecordedAt
	expected.Source.DeviceID = actual.Source.DeviceID
	expected.Source.OS = actual.Source.OS
	expectedJSON, err := json.Marshal(expected)
	return err == nil && bytes.Equal(actualJSON, expectedJSON)
}

func resolveSourceRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("source root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect source root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source root must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve source root links: %w", err)
	}
	if err := ensureOutsideGit(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func resolveEntry(root, relative string) (string, error) {
	if !validRelativePath(relative) {
		return "", errors.New("recovered source path is unsafe")
	}
	parts := strings.Split(relative, "/")
	current := root
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("inspect recovered source: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("recovered source path contains a symbolic link")
		}
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", fmt.Errorf("inspect recovered source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("recovered source is not a regular file")
	}
	return current, nil
}

func ensureOutsideGit(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return errors.New("recovered raw sources must be outside every Git worktree")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect source root ancestors: %w", err)
		}
		bare, err := looksLikeBareGit(current)
		if err != nil {
			return err
		}
		if bare {
			return errors.New("recovered raw sources must be outside every bare Git repository")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func looksLikeBareGit(path string) (bool, error) {
	head, headErr := os.Lstat(filepath.Join(path, "HEAD"))
	objects, objectsErr := os.Lstat(filepath.Join(path, "objects"))
	refs, refsErr := os.Lstat(filepath.Join(path, "refs"))
	for _, err := range []error{headErr, objectsErr, refsErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect bare Git ancestors: %w", err)
		}
	}
	return headErr == nil && head.Mode().IsRegular() && objectsErr == nil && objects.IsDir() &&
		refsErr == nil && refs.IsDir(), nil
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > 4096 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") ||
		strings.Contains(strings.SplitN(value, "/", 2)[0], ":") || strings.ContainsRune(value, '\x00') {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validReason(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
