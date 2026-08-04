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
	storeSchema = "agent-memory-store/v1alpha1"
	eventsPath  = "evidence/events.jsonl"
)

type Store struct {
	root     string
	deviceID string
}

type Appender struct {
	store    *Store
	file     *os.File
	previous string
	failed   bool
	closed   bool
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
	temp, err := os.CreateTemp(filepath.Join(s.root, "evidence", "tmp"), "blob-*")
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
	if appendErr != nil {
		return nil, appendErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return records, nil
}

func (s *Store) NewAppender() (*Appender, error) {
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	previous, err := lastRecordHash(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	return &Appender{store: s, file: file, previous: previous}, nil
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
	if err := a.file.Close(); err != nil {
		return fmt.Errorf("close ledger appender: %w", err)
	}
	return nil
}

func validateEvent(event Event) error {
	if event.SchemaVersion != SchemaVersion {
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
	report := VerificationReport{Issues: []string{}}
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return report
	}
	if err != nil {
		report.Issues = append(report.Issues, fmt.Sprintf("open ledger: %v", err))
		return report
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
				if issue := s.verifyBlob(*record.Event.Payload.Blob); issue != "" {
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
	path := filepath.Join(s.root, filepath.FromSlash(eventsPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open ledger: %w", err)
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
				return fmt.Errorf("ledger ends with partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return fmt.Errorf("decode ledger line %d: %w", lineNumber, err)
			}
			if err := validateEvent(record.Event); err != nil {
				return fmt.Errorf("validate ledger line %d: %w", lineNumber, err)
			}
			expected, err := makeRecord(record.Event, previous)
			if err != nil {
				return fmt.Errorf("hash ledger line %d: %w", lineNumber, err)
			}
			if record.PreviousRecordHash != previous || record.RecordHash != expected.RecordHash {
				return fmt.Errorf("ledger integrity failure at line %d", lineNumber)
			}
			if err := visitor(record); err != nil {
				return fmt.Errorf("visit ledger line %d: %w", lineNumber, err)
			}
			previous = record.RecordHash
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read ledger: %w", readErr)
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
	if len(digest) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join(
		"evidence", "blobs", "sha256", digest[:2], digest[2:],
	))
}
