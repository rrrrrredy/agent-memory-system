package ledger

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	storeSchema    = "agent-memory-store/v1alpha1"
	eventsPath     = "evidence/events.jsonl"
	writerLockPath = "state/evidence-writer.lock"
)

var ErrWriterLocked = errors.New("evidence ledger is locked")

type Store struct {
	root     string
	deviceID string
}

type Appender struct {
	store      *Store
	file       *os.File
	writerLock *writerLock
	previous   string
	failed     bool
	closed     bool
}

type writerLock struct {
	path string
}

type writerLockMetadata struct {
	SchemaVersion string    `json:"schema_version"`
	DeviceID      string    `json:"device_id"`
	ProcessID     int       `json:"process_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type storeManifest struct {
	SchemaVersion string    `json:"schema_version"`
	DeviceID      string    `json:"device_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func Init(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := ensureEvidenceRootOutsideGit(absolute); err != nil {
		return nil, err
	}
	for _, directory := range []string{
		filepath.Join(absolute, "evidence", "blobs", "sha256"),
		filepath.Join(absolute, "evidence", "tmp"),
		filepath.Join(absolute, "indexes"),
		filepath.Join(absolute, "state"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", directory, err)
		}
	}

	manifestPath := filepath.Join(absolute, "store.json")
	deviceID, err := newDeviceID()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		writeErr := encoder.Encode(storeManifest{
			SchemaVersion: storeSchema,
			DeviceID:      deviceID,
			CreatedAt:     time.Now().UTC(),
		})
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil {
			return nil, fmt.Errorf("write manifest: %w", writeErr)
		}
		if syncErr != nil {
			return nil, fmt.Errorf("sync manifest: %w", syncErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close manifest: %w", closeErr)
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create manifest: %w", err)
	}

	store := &Store{root: absolute}
	if err := store.validateManifest(); err != nil {
		return nil, err
	}
	return store, nil
}

func Open(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := ensureEvidenceRootOutsideGit(absolute); err != nil {
		return nil, err
	}
	store := &Store{root: absolute}
	if err := store.validateManifest(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) DeviceID() string {
	return s.deviceID
}

// ValidateLocation rechecks that an already opened evidence root has not moved
// into a Git worktree. Long-running local processes must call it before writing
// non-ledger state beneath the evidence root.
func (s *Store) ValidateLocation() error {
	if s == nil {
		return errors.New("store is required")
	}
	return ensureEvidenceRootOutsideGit(s.root)
}

func (s *Store) validateManifest() error {
	data, err := os.ReadFile(filepath.Join(s.root, "store.json"))
	if err != nil {
		return fmt.Errorf("read store manifest: %w", err)
	}
	var manifest storeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode store manifest: %w", err)
	}
	if manifest.SchemaVersion != storeSchema {
		return fmt.Errorf("unsupported store schema %q", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.DeviceID) == "" {
		return errors.New("store manifest has no device_id")
	}
	s.deviceID = manifest.DeviceID
	return nil
}

func newDeviceID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate device id: %w", err)
	}
	return "device-" + hex.EncodeToString(random[:]), nil
}

func NewEventID(now time.Time) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return fmt.Sprintf("%016x-%s", now.UTC().UnixMilli(), hex.EncodeToString(random[:])), nil
}

func InlinePayload(encoding, mediaType, content string) Payload {
	sum := sha256.Sum256([]byte(content))
	return Payload{
		Encoding:  encoding,
		MediaType: mediaType,
		Content:   &content,
		SHA256:    hex.EncodeToString(sum[:]),
		Bytes:     int64(len([]byte(content))),
	}
}

func (s *Store) PutBlob(reader io.Reader) (BlobRef, error) {
	if err := ensureEvidenceRootOutsideGit(s.root); err != nil {
		return BlobRef{}, err
	}
	tempRoot := filepath.Join(s.root, "evidence", "tmp")
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return BlobRef{}, fmt.Errorf("create blob temp directory: %w", err)
	}
	temp, err := os.CreateTemp(tempRoot, "blob-*")
	if err != nil {
		return BlobRef{}, fmt.Errorf("create blob temp file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hasher), reader)
	if err != nil {
		cleanup()
		return BlobRef{}, fmt.Errorf("write blob: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return BlobRef{}, fmt.Errorf("sync blob: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return BlobRef{}, fmt.Errorf("close blob: %w", err)
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	relative := filepath.ToSlash(filepath.Join("evidence", "blobs", "sha256", digest[:2], digest[2:]))
	target := filepath.Join(s.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		_ = os.Remove(tempPath)
		return BlobRef{}, fmt.Errorf("create blob directory: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(tempPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tempPath)
		return BlobRef{}, fmt.Errorf("inspect blob target: %w", err)
	} else if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(tempPath)
		return BlobRef{}, fmt.Errorf("commit blob: %w", err)
	}

	reference := BlobRef{SHA256: digest, Bytes: size, RelativePath: relative}
	if issue := s.verifyBlob(reference); issue != "" {
		return BlobRef{}, errors.New(issue)
	}
	return reference, nil
}

func (s *Store) OpenBlob(reference BlobRef) (*os.File, error) {
	expected := expectedBlobRelativePath(reference.SHA256)
	if reference.RelativePath != expected {
		return nil, fmt.Errorf("unsafe blob path %q; expected %q", reference.RelativePath, expected)
	}
	file, err := os.Open(filepath.Join(s.root, filepath.FromSlash(reference.RelativePath)))
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return file, nil
}

func (s *Store) Append(event Event) (Record, error) {
	records, err := s.AppendBatch([]Event{event})
	if err != nil {
		return Record{}, err
	}
	return records[0], nil
}

func (s *Store) AppendBatch(events []Event) ([]Record, error) {
	appender, err := s.NewAppender()
	if err != nil {
		return nil, err
	}
	records, appendErr := appender.AppendBatch(events)
	closeErr := appender.Close()
	if appendErr != nil && closeErr != nil {
		return nil, errors.Join(appendErr, closeErr)
	}
	if appendErr != nil {
		return nil, appendErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return records, nil
}

func (s *Store) NewAppender() (*Appender, error) {
	if err := ensureEvidenceRootOutsideGit(s.root); err != nil {
		return nil, err
	}
	lock, err := s.acquireWriterLock(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, releaseWriterLockAfterError(lock,
			fmt.Errorf("create ledger directory: %w", err))
	}
	previous, err := lastRecordHash(path)
	if err != nil {
		return nil, releaseWriterLockAfterError(lock, err)
	}
	return s.openAppenderAt(previous, lock)
}

// NewAppenderAfterVisit verifies and visits the existing chain once, then opens
// an appender at the verified tail. The caller must still enforce the
// single-writer contract between the scan and subsequent appends.
func (s *Store) NewAppenderAfterVisit(visitor func(Record) error) (*Appender, error) {
	if err := ensureEvidenceRootOutsideGit(s.root); err != nil {
		return nil, err
	}
	lock, err := s.acquireWriterLock(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	previous, err := s.visitRecords(visitor)
	if err != nil {
		return nil, releaseWriterLockAfterError(lock, err)
	}
	return s.openAppenderAt(previous, lock)
}

func (s *Store) openAppenderAt(previous string, lock *writerLock) (*Appender, error) {
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, releaseWriterLockAfterError(lock,
			fmt.Errorf("create ledger directory: %w", err))
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, releaseWriterLockAfterError(lock,
			fmt.Errorf("open ledger: %w", err))
	}
	return &Appender{store: s, file: file, writerLock: lock, previous: previous}, nil
}

func (a *Appender) AppendBatch(events []Event) ([]Record, error) {
	if a == nil || a.file == nil || a.closed {
		return nil, errors.New("ledger appender is closed")
	}
	if a.failed {
		return nil, errors.New("ledger appender is unusable after a failed write")
	}
	if len(events) == 0 {
		return []Record{}, nil
	}
	for _, event := range events {
		if err := validateEvent(event); err != nil {
			return nil, err
		}
		if event.Payload != nil && event.Payload.Blob != nil {
			if issue := a.store.verifyBlob(*event.Payload.Blob); issue != "" {
				return nil, errors.New(issue)
			}
		}
	}

	previous := a.previous
	records := make([]Record, 0, len(events))
	var buffer bytes.Buffer
	for _, event := range events {
		record, err := makeRecord(event, previous)
		if err != nil {
			return nil, err
		}
		line, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("encode record: %w", err)
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
		records = append(records, record)
		previous = record.RecordHash
	}
	written, err := a.file.Write(buffer.Bytes())
	if err != nil {
		a.failed = true
		return nil, fmt.Errorf("append ledger batch: %w", err)
	}
	if written != buffer.Len() {
		a.failed = true
		return nil, fmt.Errorf("append ledger batch: %w", io.ErrShortWrite)
	}
	if err := a.file.Sync(); err != nil {
		a.failed = true
		return nil, fmt.Errorf("sync ledger batch: %w", err)
	}
	a.previous = previous
	return records, nil
}

func (a *Appender) Close() error {
	if a == nil || a.closed {
		return nil
	}
	a.closed = true
	closeErr := a.file.Close()
	lockErr := a.writerLock.release()
	if closeErr != nil && lockErr != nil {
		return errors.Join(fmt.Errorf("close ledger appender: %w", closeErr),
			fmt.Errorf("release evidence writer lock: %w", lockErr))
	}
	if closeErr != nil {
		return fmt.Errorf("close ledger appender: %w", closeErr)
	}
	if lockErr != nil {
		return fmt.Errorf("release evidence writer lock: %w", lockErr)
	}
	return nil
}

func (s *Store) acquireWriterLock(now time.Time) (*writerLock, error) {
	path := filepath.Join(s.root, filepath.FromSlash(writerLockPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create evidence writer lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w; verify no writer is active before explicit stale-lock recovery", ErrWriterLocked)
	}
	if err != nil {
		return nil, fmt.Errorf("acquire evidence writer lock: %w", err)
	}
	metadata := writerLockMetadata{
		SchemaVersion: "evidence-writer-lock/v1alpha1",
		DeviceID:      s.DeviceID(),
		ProcessID:     os.Getpid(),
		CreatedAt:     now.UTC(),
	}
	encoder := json.NewEncoder(file)
	writeErr := encoder.Encode(metadata)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		var failures []error
		if writeErr != nil {
			failures = append(failures, fmt.Errorf("write evidence writer lock: %w", writeErr))
		}
		if closeErr != nil {
			failures = append(failures, fmt.Errorf("close evidence writer lock: %w", closeErr))
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			failures = append(failures,
				fmt.Errorf("remove incomplete evidence writer lock: %w", removeErr))
		}
		return nil, errors.Join(failures...)
	}
	return &writerLock{path: path}, nil
}

func (lock *writerLock) release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return err
	}
	lock.path = ""
	return nil
}

func releaseWriterLockAfterError(lock *writerLock, operationErr error) error {
	if lockErr := lock.release(); lockErr != nil {
		return errors.Join(operationErr,
			fmt.Errorf("release evidence writer lock: %w", lockErr))
	}
	return operationErr
}

// WriterLockPresent reports whether a ledger writer currently owns, or may
// have left behind, the exclusive writer lock. It does not guess whether the
// lock is stale.
func (s *Store) WriterLockPresent() (bool, error) {
	path := filepath.Join(s.root, filepath.FromSlash(writerLockPath))
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect evidence writer lock: %w", err)
	}
	return true, nil
}

// ClearStaleWriterLock removes the evidence writer lock only after the caller
// has independently verified that no ledger writer is active.
func (s *Store) ClearStaleWriterLock() (bool, error) {
	path := filepath.Join(s.root, filepath.FromSlash(writerLockPath))
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove evidence writer lock: %w", err)
	}
	return true, nil
}

func validateEvent(event Event) error {
	if !validEventKindForVersion(event.SchemaVersion, event.Kind) {
		return fmt.Errorf("unsupported event schema %q", event.SchemaVersion)
	}
	if event.EventID == "" || event.Kind == "" {
		return errors.New("event_id and kind are required")
	}
	if event.ObservedAt.IsZero() || event.RecordedAt.IsZero() {
		return errors.New("observed_at and recorded_at are required")
	}
	if event.Source.Agent == "" || event.Source.Adapter == "" ||
		event.Source.AdapterVersion == "" || event.Source.DeviceID == "" ||
		event.Source.ThreadID == "" {
		return errors.New("source agent, adapter, adapter_version, device_id, and thread_id are required")
	}
	if event.Source.SourcePathHash != "" && !validSHA256(event.Source.SourcePathHash) {
		return errors.New("source_path_hash must be a lowercase sha256 digest")
	}
	if event.Source.AcquisitionPathHash != "" && !validSHA256(event.Source.AcquisitionPathHash) {
		return errors.New("acquisition_path_hash must be a lowercase sha256 digest")
	}
	if event.Completeness.Status == "" {
		return errors.New("completeness status is required")
	}
	if event.Privacy.Classification != "local_only" {
		return errors.New("evidence privacy classification must be local_only")
	}
	if event.Kind == KindReasoning {
		if event.Reasoning == nil {
			return errors.New("reasoning event requires reasoning visibility")
		}
		switch event.Reasoning.Visibility {
		case ReasoningRawExposed, ReasoningSummaryOnly, ReasoningEncryptedOpaque,
			ReasoningNotExposed:
		default:
			return fmt.Errorf("invalid reasoning visibility %q", event.Reasoning.Visibility)
		}
	}
	if event.Payload != nil {
		if (event.Payload.Content == nil) == (event.Payload.Blob == nil) {
			return errors.New("payload requires exactly one of content or blob")
		}
		if event.Payload.Content != nil {
			sum := sha256.Sum256([]byte(*event.Payload.Content))
			if event.Payload.SHA256 != hex.EncodeToString(sum[:]) {
				return errors.New("inline payload sha256 mismatch")
			}
			if event.Payload.Bytes != int64(len([]byte(*event.Payload.Content))) {
				return errors.New("inline payload byte count mismatch")
			}
		}
		if event.Payload.Blob != nil &&
			(event.Payload.SHA256 != event.Payload.Blob.SHA256 ||
				event.Payload.Bytes != event.Payload.Blob.Bytes) {
			return errors.New("payload and blob metadata disagree")
		}
		if event.Payload.Blob != nil {
			expected := expectedBlobRelativePath(event.Payload.Blob.SHA256)
			if event.Payload.Blob.RelativePath != expected {
				return fmt.Errorf("blob path must be %q", expected)
			}
		}
	}
	if event.Completeness.Status == CompletenessComplete && event.Payload == nil {
		return errors.New("complete event requires a payload")
	}
	if event.Completeness.Status != CompletenessComplete &&
		strings.TrimSpace(event.Completeness.Reason) == "" {
		return errors.New("non-complete event requires a reason")
	}
	return nil
}

func validEventKindForVersion(version string, kind EventKind) bool {
	base := false
	switch kind {
	case KindUserMessage, KindAgentMessage, KindReasoning, KindToolCall, KindToolResult,
		KindApproval, KindFileChange, KindAttachment, KindSubagentEvent, KindCompaction,
		KindSystemEvent, KindSourceSnapshot, KindRetrieval, KindInjection, KindAdoption,
		KindEvaluationCorpus, KindEvaluationAttestation, KindTaskAttempt, KindEvaluationRun,
		KindGap, KindUnknown:
		base = true
	}
	if version == SchemaVersionV1Alpha1 {
		return base
	}
	if version != SchemaVersionV1Alpha2 {
		return false
	}
	if base {
		return true
	}
	switch kind {
	case KindCompactionGroundTruth, KindEvaluationTrialPlan, KindTaskExecutionStarted,
		KindTaskExecutionReceipt, KindEpisodeGenerationAttempt, KindEpisodeGeneration,
		KindTaskAttemptContract:
		return true
	default:
		return false
	}
}
func makeRecord(event Event, previous string) (Record, error) {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return Record{}, fmt.Errorf("encode event for hash: %w", err)
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(previous))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(eventJSON)
	return Record{
		Event:              event,
		PreviousRecordHash: previous,
		RecordHash:         hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func lastRecordHash(path string) (string, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open ledger for tail: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	last := ""
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				return "", fmt.Errorf("ledger ends with partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return "", fmt.Errorf("decode ledger line %d: %w", lineNumber, err)
			}
			if err := validateEvent(record.Event); err != nil {
				return "", fmt.Errorf("validate ledger line %d: %w", lineNumber, err)
			}
			expected, err := makeRecord(record.Event, last)
			if err != nil {
				return "", fmt.Errorf("hash ledger line %d: %w", lineNumber, err)
			}
			if record.PreviousRecordHash != last || record.RecordHash != expected.RecordHash {
				return "", fmt.Errorf("ledger integrity failure at line %d", lineNumber)
			}
			last = record.RecordHash
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("read ledger: %w", readErr)
		}
	}
	return last, nil
}

func (s *Store) Verify() VerificationReport {
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return VerificationReport{Issues: []string{}}
	}
	if err != nil {
		report := VerificationReport{Issues: []string{}}
		report.Issues = append(report.Issues, fmt.Sprintf("open ledger: %v", err))
		return report
	}
	defer file.Close()
	return VerifyReader(file, s.verifyBlob)
}

// VerifyReader checks an evidence ledger stream and delegates content-addressed
// blob validation to verifyBlob. It is used by encrypted-backup verification so
// an archive can be checked without writing plaintext evidence to temporary
// storage.
func VerifyReader(source io.Reader, verifyBlob func(BlobRef) string) VerificationReport {
	report := VerificationReport{Issues: []string{}}
	if source == nil {
		report.Issues = append(report.Issues, "ledger reader is required")
		return report
	}
	reader := bufio.NewReader(source)
	previous := ""
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d is an incomplete final record", lineNumber))
				break
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d cannot be decoded: %v", lineNumber, err))
				break
			}
			if err := validateEvent(record.Event); err != nil {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d event invalid: %v", lineNumber, err))
			}
			expected, err := makeRecord(record.Event, previous)
			if err != nil {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d cannot be hashed: %v", lineNumber, err))
				break
			}
			if record.PreviousRecordHash != previous {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d previous hash mismatch", lineNumber))
			}
			if record.RecordHash != expected.RecordHash {
				report.Issues = append(report.Issues,
					fmt.Sprintf("line %d record hash mismatch", lineNumber))
			}
			if record.Event.Payload != nil && record.Event.Payload.Blob != nil {
				report.BlobsChecked++
				issue := "blob verifier is required"
				if verifyBlob != nil {
					issue = verifyBlob(*record.Event.Payload.Blob)
				}
				if issue != "" {
					report.Issues = append(report.Issues,
						fmt.Sprintf("line %d %s", lineNumber, issue))
				}
			}
			report.RecordsChecked++
			previous = record.RecordHash
			report.LastRecordHash = record.RecordHash
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			report.Issues = append(report.Issues, fmt.Sprintf("read ledger: %v", readErr))
			break
		}
	}
	return report
}

// VisitRecords verifies the event and hash chain before delivering each record.
// Blob contents are not read; callers that need blob verification should run
// Verify first.
func (s *Store) VisitRecords(visitor func(Record) error) error {
	_, err := s.visitRecords(visitor)
	return err
}

func (s *Store) visitRecords(visitor func(Record) error) (string, error) {
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open ledger: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	previous := ""
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				return "", fmt.Errorf("ledger ends with partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return "", fmt.Errorf("decode ledger line %d: %w", lineNumber, err)
			}
			if err := validateEvent(record.Event); err != nil {
				return "", fmt.Errorf("validate ledger line %d: %w", lineNumber, err)
			}
			expected, err := makeRecord(record.Event, previous)
			if err != nil {
				return "", fmt.Errorf("hash ledger line %d: %w", lineNumber, err)
			}
			if record.PreviousRecordHash != previous || record.RecordHash != expected.RecordHash {
				return "", fmt.Errorf("ledger integrity failure at line %d", lineNumber)
			}
			if visitor != nil {
				if err := visitor(record); err != nil {
					return "", fmt.Errorf("visit ledger line %d: %w", lineNumber, err)
				}
			}
			previous = record.RecordHash
		}
		if errors.Is(readErr, io.EOF) {
			return previous, nil
		}
		if readErr != nil {
			return "", fmt.Errorf("read ledger: %w", readErr)
		}
	}
}

func (s *Store) verifyBlob(reference BlobRef) string {
	expected := expectedBlobRelativePath(reference.SHA256)
	if reference.RelativePath != expected {
		return fmt.Sprintf("unsafe blob path %q; expected %q", reference.RelativePath, expected)
	}
	path := filepath.Join(s.root, filepath.FromSlash(reference.RelativePath))
	file, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("blob unavailable: %v", err)
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return fmt.Sprintf("blob unreadable: %v", err)
	}
	if size != reference.Bytes {
		return fmt.Sprintf("blob size mismatch: got %d want %d", size, reference.Bytes)
	}
	if digest := hex.EncodeToString(hasher.Sum(nil)); digest != reference.SHA256 {
		return fmt.Sprintf("blob sha256 mismatch: got %s want %s", digest, reference.SHA256)
	}
	return ""
}

func expectedBlobRelativePath(digest string) string {
	if !validSHA256(digest) {
		return ""
	}
	return filepath.ToSlash(filepath.Join(
		"evidence", "blobs", "sha256", digest[:2], digest[2:],
	))
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
