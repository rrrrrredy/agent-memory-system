package promotion

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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const promotionLedgerPath = "learning/promotion-events.jsonl"

type replayState struct {
	current   map[string]Revision
	counts    map[string]int
	revisions map[string]struct{}
	history   []Record
	lastHash  string
	next      int64
	records   int
}

type promotionLock struct {
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
		report.MemoriesChecked = len(state.current)
		report.LastRecordSHA256 = state.lastHash
	}
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	for memoryID, revision := range state.current {
		if revision.Status != StatusActive || revision.Source == nil {
			continue
		}
		if _, err := review.ResolveCurrentValidation(
			store, revision.Source.CandidateGeneration, revision.Source.CandidateID,
			revision.Source.CandidateContentSHA256, revision.Source.ReviewRecordSHA256,
		); err != nil {
			report.Issues = append(report.Issues,
				fmt.Sprintf("active memory %s no longer has its bound current validation: %v", memoryID, err))
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
	if err := verifyPromotionSemantics(store, state.history); err != nil {
		return state, err
	}
	return state, nil
}

func replay(store *ledger.Store) (*replayState, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	state := &replayState{
		current: map[string]Revision{}, counts: map[string]int{}, revisions: map[string]struct{}{}, next: 1,
	}
	path := filepath.Join(store.Root(), filepath.FromSlash(promotionLedgerPath))
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open promotion ledger: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			if line[len(line)-1] != '\n' {
				return state, fmt.Errorf("promotion ledger ends with a partial record at line %d", lineNumber)
			}
			var record Record
			if err := json.Unmarshal(bytes.TrimSpace(line), &record); err != nil {
				return state, fmt.Errorf("decode promotion ledger line %d: %w", lineNumber, err)
			}
			if err := validateRecord(record, state.next, state.lastHash); err != nil {
				return state, fmt.Errorf("validate promotion ledger line %d: %w", lineNumber, err)
			}
			revision := record.Event.Revision
			if _, duplicate := state.revisions[revision.RevisionID]; duplicate {
				return state, fmt.Errorf("promotion ledger line %d repeats revision %s", lineNumber, revision.RevisionID)
			}
			current, exists := state.current[revision.MemoryID]
			switch revision.Action {
			case ActionPromote:
				if exists || revision.ParentRevisionID != "" || revision.Status != StatusActive {
					return state, fmt.Errorf("promotion ledger line %d has an invalid initial promotion", lineNumber)
				}
			case ActionSupersede:
				if !exists || current.Status != StatusActive ||
					revision.ParentRevisionID != current.RevisionID || revision.Status != StatusActive {
					return state, fmt.Errorf("promotion ledger line %d has a stale supersession parent", lineNumber)
				}
			case ActionRevoke:
				if !exists || current.Status != StatusActive ||
					revision.ParentRevisionID != current.RevisionID || revision.Status != StatusRevoked ||
					revision.Kind != current.Kind || revision.Scope != current.Scope ||
					revision.RequiresExplicitRuleChangeApproval != current.RequiresExplicitRuleChangeApproval {
					return state, fmt.Errorf("promotion ledger line %d has an invalid revocation", lineNumber)
				}
			default:
				return state, fmt.Errorf("promotion ledger line %d has an unsupported action", lineNumber)
			}
			state.current[revision.MemoryID] = revision
			state.counts[revision.MemoryID]++
			state.revisions[revision.RevisionID] = struct{}{}
			state.history = append(state.history, record)
			state.records++
			state.lastHash = record.RecordSHA256
			state.next++
		}
		if errors.Is(readErr, io.EOF) {
			return state, nil
		}
		if readErr != nil {
			return state, fmt.Errorf("read promotion ledger: %w", readErr)
		}
	}
}

func validateRecord(record Record, expectedSequence int64, previous string) error {
	if record.SchemaVersion != RecordSchemaVersion || record.Sequence != expectedSequence ||
		record.PreviousRecordSHA256 != previous || !validHash(record.RecordSHA256) {
		return errors.New("promotion record envelope is invalid")
	}
	if err := validateEvent(record.Event); err != nil {
		return err
	}
	expected, err := makeRecord(record.Event, record.Sequence, previous)
	if err != nil {
		return err
	}
	if expected.RecordSHA256 != record.RecordSHA256 {
		return errors.New("promotion record hash mismatch")
	}
	return nil
}

func validateEvent(event Event) error {
	if event.SchemaVersion != EventSchemaVersion || !strings.HasPrefix(event.EventID, "promotion-") ||
		event.RecordedAt.IsZero() || strings.TrimSpace(event.DeviceID) == "" ||
		!ValidApproverKind(event.Approver.Kind) || strings.TrimSpace(event.Approver.ID) == "" ||
		event.Approver.ID != strings.TrimSpace(event.Approver.ID) || len(event.Approver.ID) > 256 ||
		!validHash(event.RequestSHA256) || event.Privacy != "local_only" {
		return errors.New("promotion event envelope is invalid")
	}
	if event.Revision.RecordedAt != event.RecordedAt || event.Revision.OriginDeviceID != event.DeviceID ||
		event.Revision.ApproverID != event.Approver.ID {
		return errors.New("promotion event and revision attribution do not match")
	}
	return validateRevisionSyntax(event.Revision)
}

func validateRevisionSyntax(revision Revision) error {
	if revision.SchemaVersion != RevisionSchemaVersion ||
		!validPrefixedHash(revision.MemoryID, "memory-") ||
		!validPrefixedHash(revision.RevisionID, "memory-revision-") ||
		(revision.ParentRevisionID != "" && !validPrefixedHash(revision.ParentRevisionID, "memory-revision-")) ||
		revision.RecordedAt.IsZero() || strings.TrimSpace(revision.OriginDeviceID) == "" ||
		strings.TrimSpace(revision.ApproverID) == "" || revision.ApproverID != strings.TrimSpace(revision.ApproverID) ||
		strings.TrimSpace(revision.Reason) == "" || len(revision.Reason) > 16*1024 ||
		revision.RuleChangeAuthorization != "not_granted" || revision.Privacy != "local_only" ||
		!validKind(revision.Kind) || !validScope(revision.Scope) {
		return errors.New("promoted memory revision envelope is invalid")
	}
	if len(secretscan.Scan(revision.Reason).Findings) != 0 ||
		len(secretscan.Scan(revision.ApproverID).Findings) != 0 ||
		len(secretscan.Scan(revision.Scope.Value).Findings) != 0 {
		return errors.New("promoted memory revision metadata contains sensitive content")
	}
	switch revision.Action {
	case ActionPromote, ActionSupersede:
		if revision.Status != StatusActive || strings.TrimSpace(revision.Text) == "" ||
			!validHash(revision.TextSHA256) || revision.Source == nil || revision.Scan == nil {
			return errors.New("active promoted memory revision is incomplete")
		}
		if textDigest(revision.Text) != revision.TextSHA256 || len(secretscan.Scan(revision.Text).Findings) != 0 {
			return errors.New("promoted memory text is not clean or hash verified")
		}
		if err := validateSource(*revision.Source, revision.Scope); err != nil {
			return err
		}
		if err := validateScanAttestation(*revision.Scan, revision.TextSHA256); err != nil {
			return err
		}
	case ActionRevoke:
		if revision.Status != StatusRevoked || revision.Text != "" || revision.TextSHA256 != "" ||
			revision.Source != nil || revision.Scan != nil {
			return errors.New("revoked memory revision contains active content")
		}
	default:
		return errors.New("promoted memory action is invalid")
	}
	return nil
}

func validateSource(source Source, scope review.Scope) error {
	if strings.TrimSpace(source.CandidateGeneration) == "" ||
		filepath.Base(source.CandidateGeneration) != source.CandidateGeneration ||
		!validHash(source.CandidatesSHA256) || !validPrefixedHash(source.CandidateID, "candidate-") ||
		!validHash(source.CandidateContentSHA256) || !validHash(source.SemanticKeySHA256) ||
		!strings.HasPrefix(source.ReviewEventID, "review-") || !validHash(source.ReviewRecordSHA256) ||
		source.ReviewScope != scope || len(source.ReviewBasis) == 0 {
		return errors.New("promoted memory source is invalid")
	}
	for index, basis := range source.ReviewBasis {
		if !validReviewBasis(basis) || index > 0 && source.ReviewBasis[index-1] >= basis {
			return errors.New("promoted memory review basis is invalid")
		}
	}
	return nil
}

func validateScanAttestation(scan ScanAttestation, textSHA256 string) error {
	if scan.ScannerVersion != secretscan.ScannerVersion || !validHash(scan.SourceTextSHA256) ||
		scan.RedactedTextSHA256 != textSHA256 || !validHash(scan.RedactedTextSHA256) {
		return errors.New("promotion scan attestation is invalid")
	}
	for index, findingID := range scan.FindingIDs {
		if !validPrefixedHash(findingID, "finding-") ||
			index > 0 && scan.FindingIDs[index-1] >= findingID {
			return errors.New("promotion scan finding ids are invalid")
		}
	}
	return nil
}

func verifyPromotionSemantics(store *ledger.Store, records []Record) error {
	state := &replayState{current: map[string]Revision{}}
	for _, record := range records {
		revision := record.Event.Revision
		request, err := requestFromRevision(record.Event)
		if err != nil {
			return fmt.Errorf("promotion record %d request cannot be reconstructed: %w", record.Sequence, err)
		}
		digest, err := hashRequest(request)
		if err != nil {
			return err
		}
		if digest != record.Event.RequestSHA256 {
			return fmt.Errorf("promotion record %d request hash mismatch", record.Sequence)
		}
		if revision.Action == ActionPromote || revision.Action == ActionSupersede {
			verified, err := review.VerifyValidationRecord(
				store, revision.Source.CandidateGeneration, revision.Source.CandidateID,
				revision.Source.CandidateContentSHA256, revision.Source.ReviewRecordSHA256,
			)
			if err != nil {
				return fmt.Errorf("promotion record %d validation proof is invalid: %w", record.Sequence, err)
			}
			if err := compareVerifiedSource(revision, verified); err != nil {
				return fmt.Errorf("promotion record %d: %w", record.Sequence, err)
			}
			report := secretscan.Scan(verified.Candidate.Text)
			redacted, err := secretscan.ApplyRedactions(verified.Candidate.Text, report, revision.Scan.Redactions)
			if err != nil {
				return fmt.Errorf("promotion record %d redaction is not reproducible: %w", record.Sequence, err)
			}
			findingIDs := findingIDs(report)
			if redacted != revision.Text || report.ContentSHA256 != revision.Scan.SourceTextSHA256 ||
				!reflect.DeepEqual(findingIDs, revision.Scan.FindingIDs) {
				return fmt.Errorf("promotion record %d scan result is not reproducible", record.Sequence)
			}
			if revision.Action == ActionPromote &&
				expectedMemoryID(revision.TextSHA256, revision.Scope) != revision.MemoryID {
				return fmt.Errorf("promotion record %d memory id is not derived from redacted text and scope", record.Sequence)
			}
			if revision.Action == ActionPromote {
				for _, existing := range state.current {
					if existing.Source != nil && existing.Scope == revision.Scope &&
						existing.Source.SemanticKeySHA256 == revision.Source.SemanticKeySHA256 {
						return fmt.Errorf("promotion record %d duplicates an existing semantic memory", record.Sequence)
					}
				}
			}
			if revision.Action == ActionSupersede {
				current, exists := state.current[revision.MemoryID]
				if !exists || current.Source == nil ||
					current.Source.SemanticKeySHA256 != revision.Source.SemanticKeySHA256 {
					return fmt.Errorf("promotion record %d supersession changes semantic identity", record.Sequence)
				}
			}
		}
		expectedRevisionID, err := revisionID(revision)
		if err != nil {
			return err
		}
		if expectedRevisionID != revision.RevisionID {
			return fmt.Errorf("promotion record %d revision id mismatch", record.Sequence)
		}
		state.current[revision.MemoryID] = revision
	}
	return nil
}

func compareVerifiedSource(revision Revision, verified review.ValidatedCandidate) error {
	proof, candidate, source := verified.Proof, verified.Candidate, revision.Source
	if source == nil || source.CandidateGeneration != proof.SourceCandidateGeneration ||
		source.CandidatesSHA256 != proof.SourceCandidatesSHA256 || source.CandidateID != proof.CandidateID ||
		source.CandidateContentSHA256 != proof.CandidateContentSHA256 ||
		source.SemanticKeySHA256 != candidate.SemanticKeySHA256 || source.ReviewEventID != proof.ReviewEventID ||
		source.ReviewRecordSHA256 != proof.ReviewRecordSHA256 || source.ReviewScope != proof.Scope ||
		!reflect.DeepEqual(source.ReviewBasis, proof.Basis) || revision.Kind != candidate.Kind ||
		revision.Scope != proof.Scope ||
		revision.RequiresExplicitRuleChangeApproval != candidate.RequiresExplicitRuleChangeApproval {
		return errors.New("promoted memory source does not match the verified candidate and review")
	}
	if revision.RecordedAt.Before(proof.ReviewedAt) {
		return errors.New("promoted memory revision predates its validation record")
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
		return Record{}, fmt.Errorf("encode promotion record for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return Record{
		SchemaVersion: RecordSchemaVersion, Sequence: sequence, Event: event,
		PreviousRecordSHA256: previous, RecordSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func appendRecord(store *ledger.Store, record Record) error {
	path := filepath.Join(store.Root(), filepath.FromSlash(promotionLedgerPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create promotion ledger directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open promotion ledger: %w", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("encode promotion record: %w", err)
	}
	data = append(data, '\n')
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append promotion record: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		return fmt.Errorf("append promotion record: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync promotion record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close promotion ledger: %w", err)
	}
	return nil
}

func acquirePromotionLock(store *ledger.Store, now time.Time) (*promotionLock, error) {
	path := filepath.Join(store.Root(), "state", "memory-promotion.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create promotion lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("memory promotion ledger is locked; inspect the lock before explicit stale-lock recovery")
	}
	if err != nil {
		return nil, fmt.Errorf("acquire memory promotion lock: %w", err)
	}
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(lockMetadata{
		SchemaVersion: "memory-promotion-lock/v1alpha1", DeviceID: store.DeviceID(),
		ProcessID: os.Getpid(), CreatedAt: now.UTC(),
	}); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write memory promotion lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync memory promotion lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close memory promotion lock: %w", err)
	}
	return &promotionLock{path: path}, nil
}

func (lock *promotionLock) release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return fmt.Errorf("release memory promotion lock: %w", err)
	}
	lock.path = ""
	return nil
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

func validKind(kind candidates.CandidateKind) bool {
	return kind == candidates.KindConstraint || kind == candidates.KindCorrection ||
		kind == candidates.KindDirective
}

func validScope(scope review.Scope) bool {
	if strings.TrimSpace(scope.Value) == "" || scope.Value != strings.TrimSpace(scope.Value) ||
		len(scope.Value) > 4096 || strings.ContainsAny(scope.Value, "\r\n") {
		return false
	}
	switch scope.Kind {
	case review.ScopeGlobal:
		return scope.Value == "*"
	case review.ScopeAgent:
		return scope.Value == string(ledger.AgentCodex) || scope.Value == string(ledger.AgentClaudeCode) ||
			scope.Value == string(ledger.AgentOpenCode) ||
			scope.Value == string(ledger.AgentDeepSeekHarness) ||
			scope.Value == string(ledger.AgentUnknown)
	case review.ScopeRepository, review.ScopeProject, review.ScopeTask:
		return true
	default:
		return false
	}
}

func validReviewBasis(value review.Basis) bool {
	switch value {
	case review.BasisExplicitRemember, review.BasisUserCorrection, review.BasisStableRepetition,
		review.BasisOutcomeEvidence, review.BasisExplicitUserConfirmation:
		return true
	default:
		return false
	}
}

func textDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func expectedMemoryID(redactedTextSHA256 string, scope review.Scope) string {
	envelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", redactedTextSHA256, scope}
	data, _ := json.Marshal(envelope)
	digest := sha256.Sum256(data)
	return "memory-" + hex.EncodeToString(digest[:])
}

func findingIDs(report secretscan.Report) []string {
	values := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		values = append(values, finding.FindingID)
	}
	sort.Strings(values)
	return values
}

func revisionID(revision Revision) (string, error) {
	copy := revision
	copy.RevisionID = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode promoted memory revision for id: %w", err)
	}
	digest := sha256.Sum256(data)
	return "memory-revision-" + hex.EncodeToString(digest[:]), nil
}
