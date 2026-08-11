// Package hookcapture implements the bounded-latency side of Codex and Claude
// Code evidence capture. Hook invocations are streamed into immutable local
// envelopes and reconciled into the evidence ledger outside the hook hot path.
package hookcapture

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	EnvelopeSchemaVersion = "agent-hook-envelope/v1alpha1"
	AdapterName           = "agent-hook-spool"
	AdapterVersion        = "agent-hook-spool/v1alpha1"
)

type CaptureOptions struct {
	Now func() time.Time
}

type CaptureResult struct {
	CapturedAt time.Time `json:"captured_at"`
	RawSHA256  string    `json:"raw_sha256"`
	RawBytes   int64     `json:"raw_bytes"`
	Status     string    `json:"status"`
	Relative   string    `json:"relative_path"`
}

type Envelope struct {
	SchemaVersion string    `json:"schema_version"`
	CapturedAt    time.Time `json:"captured_at"`
	RawBase64     string    `json:"raw_base64"`
	RawSHA256     string    `json:"raw_sha256"`
	RawBytes      int64     `json:"raw_bytes"`
	CaptureStatus string    `json:"capture_status"`
	ErrorClass    string    `json:"error_class,omitempty"`
}

// Capture streams one exact hook stdin value into an immutable envelope. It
// deliberately does not open the transcript or append to the ledger: either
// operation could make a lifecycle hook wait on a multi-gigabyte evidence
// scan. Reconcile performs those operations later.
func Capture(store *ledger.Store, agent ledger.Agent, source io.Reader, options CaptureOptions) (CaptureResult, error) {
	result := CaptureResult{}
	if store == nil {
		return result, errors.New("store is required")
	}
	if agent != ledger.AgentCodex && agent != ledger.AgentClaudeCode {
		return result, errors.New("hook capture supports codex or claude_code")
	}
	if source == nil {
		return result, errors.New("hook input is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	now := options.Now().UTC()
	result.CapturedAt = now
	directory := SpoolRoot(store, agent)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return result, fmt.Errorf("create hook spool: %w", err)
	}
	if err := verifySpoolLocation(store.Root(), directory); err != nil {
		return result, err
	}

	temporary, err := os.CreateTemp(directory, ".hook-*.tmp")
	if err != nil {
		return result, fmt.Errorf("create hook spool temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	prefix := `{"schema_version":` + jsonString(EnvelopeSchemaVersion) +
		`,"captured_at":` + jsonString(now.Format(time.RFC3339Nano)) + `,"raw_base64":"`
	if _, err := io.WriteString(temporary, prefix); err != nil {
		return result, fmt.Errorf("write hook envelope prefix: %w", err)
	}
	hasher := sha256.New()
	encoder := base64.NewEncoder(base64.StdEncoding, temporary)
	readBytes, readErr := io.Copy(encoder, io.TeeReader(source, hasher))
	closeEncoderErr := encoder.Close()
	digest := hex.EncodeToString(hasher.Sum(nil))
	status := "complete"
	errorClass := ""
	if readErr != nil {
		status = "partial"
		errorClass = classifyError(readErr)
	}
	suffix := `","raw_sha256":` + jsonString(digest) +
		`,"raw_bytes":` + fmt.Sprintf("%d", readBytes) +
		`,"capture_status":` + jsonString(status)
	if errorClass != "" {
		suffix += `,"error_class":` + jsonString(errorClass)
	}
	suffix += "}\n"
	if _, err := io.WriteString(temporary, suffix); err != nil {
		return result, fmt.Errorf("write hook envelope suffix: %w", err)
	}
	if closeEncoderErr != nil {
		return result, fmt.Errorf("finish hook envelope encoding: %w", closeEncoderErr)
	}
	if err := temporary.Sync(); err != nil {
		return result, fmt.Errorf("sync hook envelope: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return result, fmt.Errorf("close hook envelope: %w", err)
	}

	eventID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	targetName := "hook-" + eventID + "-" + digest[:16] + ".jsonl"
	target := filepath.Join(directory, targetName)
	if err := os.Link(temporaryPath, target); err != nil {
		return result, fmt.Errorf("commit hook envelope without replacement: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(target)
		return result, fmt.Errorf("remove hook envelope temporary link: %w", err)
	}
	committed = true
	relative, err := filepath.Rel(store.Root(), target)
	if err != nil {
		return result, fmt.Errorf("resolve hook envelope relative path: %w", err)
	}
	result.RawSHA256 = digest
	result.RawBytes = readBytes
	result.Status = status
	result.Relative = filepath.ToSlash(relative)
	if err := syncDirectory(directory); err != nil {
		return result, fmt.Errorf("sync hook spool directory: %w", err)
	}
	if readErr != nil {
		return result, fmt.Errorf("read hook input: %w", readErr)
	}
	return result, nil
}

func SpoolRoot(store *ledger.Store, agent ledger.Agent) string {
	if store == nil {
		return ""
	}
	return filepath.Join(store.Root(), "evidence", "hook-spool", string(agent))
}

func DecodeEnvelope(data []byte) (Envelope, []byte, error) {
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return envelope, nil, errors.New("hook envelope is not valid JSON")
	}
	if envelope.SchemaVersion != EnvelopeSchemaVersion {
		return envelope, nil, fmt.Errorf("unsupported hook envelope schema %q", envelope.SchemaVersion)
	}
	raw, err := base64.StdEncoding.DecodeString(envelope.RawBase64)
	if err != nil {
		return envelope, nil, errors.New("hook envelope raw payload is not valid base64")
	}
	digest := sha256.Sum256(raw)
	if envelope.RawSHA256 != hex.EncodeToString(digest[:]) || envelope.RawBytes != int64(len(raw)) {
		return envelope, raw, errors.New("hook envelope raw payload metadata mismatch")
	}
	if envelope.CaptureStatus != "complete" && envelope.CaptureStatus != "partial" {
		return envelope, raw, errors.New("hook envelope capture status is invalid")
	}
	if envelope.CaptureStatus == "partial" && strings.TrimSpace(envelope.ErrorClass) == "" {
		return envelope, raw, errors.New("partial hook envelope has no error class")
	}
	return envelope, raw, nil
}

func verifySpoolLocation(root, directory string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve evidence root path: %w", err)
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve hook spool path: %w", err)
	}
	relativePath, err := filepath.Rel(absoluteRoot, absoluteDirectory)
	if err != nil || relativePath == "." || relativePath == ".." ||
		strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return errors.New("hook spool escapes the local evidence root")
	}
	current := absoluteRoot
	for _, component := range strings.Split(relativePath, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect hook spool path component: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("hook spool path contains a symbolic link")
		}
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve evidence root: %w", err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("resolve hook spool: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedDirectory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("hook spool escapes the local evidence root")
	}
	info, err := os.Lstat(resolvedDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("hook spool is not a regular directory")
	}
	return nil
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission_denied"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "unexpected_end"
	default:
		digest := sha256.Sum256([]byte(err.Error()))
		return "other-" + hex.EncodeToString(digest[:8])
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if runtime.GOOS == "windows" && err != nil {
		err = nil
	}
	return errors.Join(err, closeErr)
}
