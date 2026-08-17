package review

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
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const reviewLedgerPath = "learning/review-events.jsonl"

type replayState struct {
	statuses  map[string]Status
	events    map[string]int
	lastByKey map[string]string
	history   []Record
	lastHash  string
	next      int64
	records   int
}

type reviewLock struct {
	path string
}

type lockMetadata struct {
	SchemaVersion string    `json:"schema_version"`
	DeviceID      string    `json:"device_id"`
	ProcessID     int       `json:"process_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func Verify(store *ledger.Store) VerificationReport {
	report := VerificationReport{Issues: []string{}}
	state, err := replayVerified(store)
	if state != nil {
		report.RecordsChecked = state.records
		report.LastRecordSHA256 = state.lastHash
	}
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	return report
}

func replayVerified(store *ledger.Store) (*replayState, error) {
	state, err := replay(store)
	if err != nil {
		return state, err
	}
	if err := verifyReviewSemantics(store, state.history); err != nil {
		return state, err
	}
	return state, nil
}

func replay(store *ledger.Store) (*replayState, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	state := &replayState{
		statuses: map[string]Status{}, events: map[string]int{}, lastByKey: map[string]string{}, next: 1,
	}
	path := filepath.Join(store.Root(), filepath.FromSlash(reviewLedgerPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open review ledger: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				return state, fmt.Errorf("review ledger ends with a partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return state, fmt.Errorf("decode review ledger line %d: %w", lineNumber, err)
			}
			if err := validateRecord(record, state.next, state.lastHash); err != nil {
				return state, fmt.Errorf("validate review ledger line %d: %w", lineNumber, err)
			}
			for _, transition := range record.Event.Transitions {
				key := candidateKey(
					record.Event.SourceCandidatesSHA256,
					transition.CandidateID,
					transition.CandidateContentSHA256,
				)
				current, exists := state.statuses[key]
				if exists && transition.ExpectedStatus != current {
					return state, fmt.Errorf(
						"review ledger line %d expected %q for %q after %q",
						lineNumber, transition.ExpectedStatus, transition.CandidateID, current,
					)
				}
				if !exists && transition.ExpectedStatus != StatusPending &&
					transition.ExpectedStatus != StatusQuarantined {
					return state, fmt.Errorf(
						"review ledger line %d starts %q from invalid state %q",
						lineNumber, transition.CandidateID, transition.ExpectedStatus,
					)
				}
			}
			for _, transition := range record.Event.Transitions {
				key := candidateKey(
					record.Event.SourceCandidatesSHA256,
					transition.CandidateID,
					transition.CandidateContentSHA256,
				)
				state.statuses[key] = transition.ResultingStatus
				state.events[key]++
				state.lastByKey[key] = record.RecordSHA256
			}
			state.records++
			state.history = append(state.history, record)
			state.lastHash = record.RecordSHA256
			state.next++
		}
		if errors.Is(readErr, io.EOF) {
			return state, nil
		}
		if readErr != nil {
			return state, fmt.Errorf("read review ledger: %w", readErr)
		}
	}
}

func validateRecord(record Record, expectedSequence int64, previous string) error {
	if record.SchemaVersion != RecordSchemaVersion || record.Sequence != expectedSequence ||
		record.PreviousRecordSHA256 != previous || !validHash(record.RecordSHA256) {
		return errors.New("review record envelope is invalid")
	}
	if err := validateEvent(record.Event); err != nil {
		return err
	}
	expected, err := makeRecord(record.Event, record.Sequence, previous)
	if err != nil {
		return err
	}
	if expected.RecordSHA256 != record.RecordSHA256 {
		return errors.New("review record hash mismatch")
	}
	return nil
}

func validateEvent(event Event) error {
	if event.SchemaVersion != EventSchemaVersion || !strings.HasPrefix(event.EventID, "review-") ||
		event.RecordedAt.IsZero() || strings.TrimSpace(event.DeviceID) == "" ||
		!ValidReviewerKind(event.Reviewer.Kind) || strings.TrimSpace(event.Reviewer.ID) == "" ||
		event.Reviewer.ID != strings.TrimSpace(event.Reviewer.ID) || len(event.Reviewer.ID) > 256 ||
		!validHash(event.RequestSHA256) || strings.TrimSpace(event.SourceCandidateGeneration) == "" ||
		filepath.Base(event.SourceCandidateGeneration) != event.SourceCandidateGeneration ||
		!validHash(event.SourceCandidatesSHA256) || event.Privacy != "local_only" ||
		len(event.Transitions) == 0 {
		return errors.New("review event envelope is invalid")
	}
	if event.ConflictGroupID != "" && !validPrefixedHash(event.ConflictGroupID, "candidate-conflict-") {
		return errors.New("review event conflict group is invalid")
	}
	seen := map[string]struct{}{}
	previousID := ""
	validated := 0
	for _, transition := range event.Transitions {
		if err := validateTransitionSyntax(transition); err != nil {
			return err
		}
		if previousID != "" && transition.CandidateID <= previousID {
			return errors.New("review event transitions are not strictly sorted")
		}
		previousID = transition.CandidateID
		key := candidateKey(
			event.SourceCandidatesSHA256,
			transition.CandidateID,
			transition.CandidateContentSHA256,
		)
		if _, exists := seen[key]; exists {
			return errors.New("review event repeats a candidate transition")
		}
		seen[key] = struct{}{}
		if transition.Action == ActionValidate {
			validated++
		}
		if event.ConflictGroupID != "" &&
			(transition.ExpectedStatus != StatusQuarantined ||
				(transition.Action != ActionValidate && transition.Action != ActionReject)) {
			return errors.New("review conflict event has an invalid transition")
		}
	}
	if event.ConflictGroupID == "" {
		if len(event.Transitions) != 1 ||
			(event.Transitions[0].Action == ActionValidate &&
				event.Transitions[0].ExpectedStatus == StatusQuarantined) {
			return errors.New("non-conflict review event is not a single independent transition")
		}
	} else if len(event.Transitions) < 2 || validated > 1 {
		return errors.New("review conflict event must resolve a complete group with at most one validation")
	}
	return nil
}

func validateTransitionSyntax(transition Transition) error {
	if !validPrefixedHash(transition.CandidateID, "candidate-") ||
		!validHash(transition.CandidateContentSHA256) || !validStatus(transition.ExpectedStatus) ||
		!validStatus(transition.ResultingStatus) || strings.TrimSpace(transition.Reason) == "" ||
		len(transition.Reason) > 16*1024 {
		return errors.New("review transition identity or reason is invalid")
	}
	switch transition.Action {
	case ActionValidate:
		if transition.ResultingStatus != StatusValidated ||
			(transition.ExpectedStatus != StatusPending && transition.ExpectedStatus != StatusQuarantined) ||
			transition.Scope == nil || !validScope(*transition.Scope) || len(transition.Basis) == 0 {
			return errors.New("validate transition has invalid state, scope, or basis")
		}
	case ActionReject:
		if transition.ResultingStatus != StatusRejected || transition.ExpectedStatus == StatusRejected ||
			transition.Scope != nil || len(transition.Basis) > 0 || len(transition.EvidenceEventIDs) > 0 {
			return errors.New("reject transition has invalid state or validation fields")
		}
	case ActionQuarantine:
		if transition.ResultingStatus != StatusQuarantined ||
			(transition.ExpectedStatus != StatusPending && transition.ExpectedStatus != StatusValidated) ||
			transition.Scope != nil || len(transition.Basis) > 0 || len(transition.EvidenceEventIDs) > 0 {
			return errors.New("quarantine transition has invalid state or validation fields")
		}
	case ActionReopen:
		if (transition.ExpectedStatus != StatusRejected && transition.ExpectedStatus != StatusQuarantined) ||
			(transition.ResultingStatus != StatusPending && transition.ResultingStatus != StatusQuarantined) ||
			transition.ExpectedStatus == transition.ResultingStatus || transition.Scope != nil ||
			len(transition.Basis) > 0 || len(transition.EvidenceEventIDs) > 0 {
			return errors.New("reopen transition has invalid state or validation fields")
		}
	default:
		return errors.New("review transition action is invalid")
	}
	if !sortedUniqueBasis(transition.Basis) || !sortedUniqueStrings(transition.EvidenceEventIDs) {
		return errors.New("review transition basis or evidence ids are invalid")
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
		return Record{}, fmt.Errorf("encode review record for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return Record{
		SchemaVersion: RecordSchemaVersion, Sequence: sequence, Event: event,
		PreviousRecordSHA256: previous, RecordSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func appendRecord(store *ledger.Store, record Record) error {
	path := filepath.Join(store.Root(), filepath.FromSlash(reviewLedgerPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create review ledger directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open review ledger: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encode review record: %w", err)
	}
	data = append(data, '\n')
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append review record: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		return fmt.Errorf("append review record: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync review record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close review ledger: %w", err)
	}
	return nil
}

func acquireReviewLock(store *ledger.Store, now time.Time) (*reviewLock, error) {
	path := filepath.Join(store.Root(), "state", "candidate-review.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create review lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("candidate review ledger is locked; inspect the lock before explicit stale-lock recovery")
	}
	if err != nil {
		return nil, fmt.Errorf("acquire candidate review lock: %w", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(lockMetadata{
		SchemaVersion: "candidate-review-lock/v1alpha1", DeviceID: store.DeviceID(),
		ProcessID: os.Getpid(), CreatedAt: now.UTC(),
	}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write candidate review lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync candidate review lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close candidate review lock: %w", err)
	}
	return &reviewLock{path: path}, nil
}

func (lock *reviewLock) release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return fmt.Errorf("release candidate review lock: %w", err)
	}
	lock.path = ""
	return nil
}

func candidateKey(sourceCandidatesSHA256, candidateID, contentSHA256 string) string {
	return sourceCandidatesSHA256 + "\x00" + candidateID + "\x00" + contentSHA256
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

func validStatus(status Status) bool {
	return status == StatusPending || status == StatusValidated || status == StatusRejected ||
		status == StatusQuarantined
}

func validScope(scope Scope) bool {
	if strings.TrimSpace(scope.Value) == "" || len(scope.Value) > 4096 ||
		strings.ContainsAny(scope.Value, "\r\n") {
		return false
	}
	switch scope.Kind {
	case ScopeGlobal:
		return scope.Value == "*"
	case ScopeAgent:
		return scope.Value == string(ledger.AgentCodex) || scope.Value == string(ledger.AgentClaudeCode) ||
			scope.Value == string(ledger.AgentOpenCode) ||
			scope.Value == string(ledger.AgentDeepSeekHarness) ||
			scope.Value == string(ledger.AgentUnknown)
	case ScopeRepository, ScopeProject, ScopeTask:
		return true
	default:
		return false
	}
}

func sortedUniqueBasis(values []Basis) bool {
	for index, value := range values {
		if !validBasis(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validBasis(value Basis) bool {
	switch value {
	case BasisExplicitRemember, BasisUserCorrection, BasisStableRepetition,
		BasisOutcomeEvidence, BasisExplicitUserConfirmation:
		return true
	default:
		return false
	}
}

func sortedUniqueStrings(values []string) bool {
	for index, value := range values {
		if strings.TrimSpace(value) == "" || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}
