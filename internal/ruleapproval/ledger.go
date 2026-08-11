package ruleapproval

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const approvalLedgerPath = "learning/rule-change-approval-events.jsonl"

type replayState struct {
	statuses map[string]Status
	counts   map[string]int
	history  []Record
	lastHash string
	next     int64
	records  int
}

type approvalLock struct {
	path string
}

type lockMetadata struct {
	SchemaVersion string    `json:"schema_version"`
	DeviceID      string    `json:"device_id"`
	ProcessID     int       `json:"process_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func Verify(store *ledger.Store) VerificationReport {
	report := VerificationReport{SchemaVersion: VerificationSchemaVersion, Issues: []string{}}
	state, err := replayVerified(store)
	if state != nil {
		report.RecordsChecked = state.records
		report.ApprovalsChecked = len(state.statuses)
		report.LastRecordSHA256 = state.lastHash
	}
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	for key, status := range state.statuses {
		if status != StatusAuthorized {
			continue
		}
		memoryID, revisionID, surface, target, err := splitKey(key)
		if err != nil {
			report.Issues = append(report.Issues, err.Error())
			continue
		}
		memoryStatus, err := promotion.GetStatus(store, memoryID)
		if err != nil || memoryStatus.CurrentRevisionID != revisionID || !memoryStatus.ExportEligible {
			report.Issues = append(report.Issues, fmt.Sprintf(
				"rule approval for %s %s on %s target %s is no longer effective",
				memoryID, revisionID, surface, target,
			))
		}
	}
	sort.Strings(report.Issues)
	return report
}

func replayVerified(store *ledger.Store) (*replayState, error) {
	state, err := replay(store)
	if err != nil {
		return state, err
	}
	for _, record := range state.history {
		revision, err := promotion.GetRevision(store, record.Event.MemoryID, record.Event.RevisionID)
		if err != nil {
			return state, fmt.Errorf("rule approval record %d references an invalid revision: %w", record.Sequence, err)
		}
		if revision.Status != promotion.StatusActive || record.Event.RecordedAt.Before(revision.RecordedAt) {
			return state, fmt.Errorf("rule approval record %d predates or references an inactive revision", record.Sequence)
		}
		if record.Event.Action == ActionAuthorize {
			current, err := promotion.RevisionWasCurrentAt(
				store, record.Event.MemoryID, record.Event.RevisionID, record.Event.RecordedAt,
			)
			if err != nil || !current {
				return state, fmt.Errorf("rule approval record %d did not authorize the current revision", record.Sequence)
			}
		}
		request := Request{
			SchemaVersion: RequestSchemaVersion, Approver: record.Event.Approver,
			MemoryID: record.Event.MemoryID, RevisionID: record.Event.RevisionID,
			Surface: record.Event.Surface, Target: record.Event.Target,
			ExpectedStatus: record.Event.ExpectedStatus,
			Action:         record.Event.Action, Reason: record.Event.Reason,
		}
		if err := validateRequest(request); err != nil {
			return state, fmt.Errorf("rule approval record %d request is invalid: %w", record.Sequence, err)
		}
		digest, err := hashRequest(request)
		if err != nil {
			return state, err
		}
		if digest != record.Event.RequestSHA256 {
			return state, fmt.Errorf("rule approval record %d request hash mismatch", record.Sequence)
		}
	}
	return state, nil
}

func replay(store *ledger.Store) (*replayState, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	state := &replayState{statuses: map[string]Status{}, counts: map[string]int{}, next: 1}
	path := filepath.Join(store.Root(), filepath.FromSlash(approvalLedgerPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open rule approval ledger: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				return state, fmt.Errorf("rule approval ledger ends with a partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return state, fmt.Errorf("decode rule approval ledger line %d: %w", lineNumber, err)
			}
			if err := validateRecord(record, state.next, state.lastHash); err != nil {
				return state, fmt.Errorf("validate rule approval ledger line %d: %w", lineNumber, err)
			}
			key := approvalKey(
				record.Event.MemoryID, record.Event.RevisionID, record.Event.Surface, record.Event.Target,
			)
			current := StatusNotAuthorized
			if value, exists := state.statuses[key]; exists {
				current = value
			}
			if current != record.Event.ExpectedStatus {
				return state, fmt.Errorf("rule approval ledger line %d has stale expected status", lineNumber)
			}
			state.statuses[key] = record.Event.ResultingStatus
			state.counts[key]++
			state.history = append(state.history, record)
			state.lastHash = record.RecordSHA256
			state.next++
			state.records++
		}
		if errors.Is(readErr, io.EOF) {
			return state, nil
		}
		if readErr != nil {
			return state, fmt.Errorf("read rule approval ledger: %w", readErr)
		}
	}
}

func validateRecord(record Record, sequence int64, previous string) error {
	if record.SchemaVersion != RecordSchemaVersion || record.Sequence != sequence ||
		record.PreviousRecordSHA256 != previous || !validHash(record.RecordSHA256) {
		return errors.New("rule approval record envelope is invalid")
	}
	if err := validateEvent(record.Event); err != nil {
		return err
	}
	expected, err := makeRecord(record.Event, sequence, previous)
	if err != nil {
		return err
	}
	if expected.RecordSHA256 != record.RecordSHA256 {
		return errors.New("rule approval record hash mismatch")
	}
	return nil
}

func validateEvent(event Event) error {
	if event.SchemaVersion != EventSchemaVersion || !strings.HasPrefix(event.EventID, "rule-approval-") ||
		event.RecordedAt.IsZero() || strings.TrimSpace(event.DeviceID) == "" ||
		event.Approver.Kind != "human" || strings.TrimSpace(event.Approver.ID) == "" ||
		event.Approver.ID != strings.TrimSpace(event.Approver.ID) || len(event.Approver.ID) > 256 ||
		!validHash(event.RequestSHA256) || !validPrefixedHash(event.MemoryID, "memory-") ||
		!validPrefixedHash(event.RevisionID, "memory-revision-") || !validSurface(event.Surface) ||
		!validTarget(event.Target) ||
		strings.TrimSpace(event.Reason) == "" || len(event.Reason) > 16*1024 || event.Privacy != "local_only" {
		return errors.New("rule approval event envelope is invalid")
	}
	if len(secretscan.Scan(event.Reason).Findings) != 0 ||
		len(secretscan.Scan(event.Approver.ID).Findings) != 0 ||
		len(secretscan.Scan(event.Target).Findings) != 0 {
		return errors.New("rule approval event contains sensitive metadata")
	}
	switch event.Action {
	case ActionAuthorize:
		if event.ExpectedStatus != StatusNotAuthorized || event.ResultingStatus != StatusAuthorized {
			return errors.New("rule authorization transition is invalid")
		}
	case ActionRevoke:
		if event.ExpectedStatus != StatusAuthorized || event.ResultingStatus != StatusNotAuthorized {
			return errors.New("rule approval revocation transition is invalid")
		}
	default:
		return errors.New("rule approval action is invalid")
	}
	return nil
}

func makeRecord(event Event, sequence int64, previous string) (Record, error) {
	envelope := struct {
		SchemaVersion        string `json:"schema_version"`
		Sequence             int64  `json:"sequence"`
		Event                Event  `json:"event"`
		PreviousRecordSHA256 string `json:"previous_record_sha256"`
	}{RecordSchemaVersion, sequence, event, previous}
	data, err := json.Marshal(envelope)
	if err != nil {
		return Record{}, fmt.Errorf("encode rule approval record for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return Record{
		SchemaVersion: RecordSchemaVersion, Sequence: sequence, Event: event,
		PreviousRecordSHA256: previous, RecordSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func appendRecord(store *ledger.Store, record Record) error {
	path := filepath.Join(store.Root(), filepath.FromSlash(approvalLedgerPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create rule approval ledger directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open rule approval ledger: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encode rule approval record: %w", err)
	}
	data = append(data, '\n')
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append rule approval record: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		return fmt.Errorf("append rule approval record: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync rule approval record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close rule approval ledger: %w", err)
	}
	return nil
}

func acquireApprovalLock(store *ledger.Store, now time.Time) (*approvalLock, error) {
	path := filepath.Join(store.Root(), "state", "rule-change-approval.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create rule approval lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("rule approval ledger is locked; inspect the lock before explicit stale-lock recovery")
	}
	if err != nil {
		return nil, fmt.Errorf("acquire rule approval lock: %w", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(lockMetadata{
		SchemaVersion: "rule-change-approval-lock/v1alpha1", DeviceID: store.DeviceID(),
		ProcessID: os.Getpid(), CreatedAt: now.UTC(),
	}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write rule approval lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync rule approval lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close rule approval lock: %w", err)
	}
	return &approvalLock{path: path}, nil
}

func (lock *approvalLock) release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return fmt.Errorf("release rule approval lock: %w", err)
	}
	lock.path = ""
	return nil
}

func approvalKey(memoryID, revisionID string, surface Surface, target string) string {
	return memoryID + "\x00" + revisionID + "\x00" + string(surface) + "\x00" + target
}

func splitKey(key string) (string, string, Surface, string, error) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 4 {
		return "", "", "", "", errors.New("rule approval state key is invalid")
	}
	return parts[0], parts[1], Surface(parts[2]), parts[3], nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validPrefixedHash(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validHash(strings.TrimPrefix(value, prefix))
}

func validSurface(surface Surface) bool {
	switch surface {
	case SurfaceAgentsMD, SurfaceSkill, SurfaceHook, SurfacePlugin, SurfaceGlobalRule:
		return true
	default:
		return false
	}
}

func validTarget(target string) bool {
	return strings.TrimSpace(target) != "" && target == strings.TrimSpace(target) &&
		len(target) <= 4096 && !strings.ContainsAny(target, "\x00\r\n")
}
