package promotion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

func DecodeRequest(reader io.Reader) (Request, error) {
	if reader == nil {
		return Request{}, errors.New("promotion request reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode promotion request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("promotion request contains more than one JSON value")
		}
		return Request{}, fmt.Errorf("decode trailing promotion request data: %w", err)
	}
	return request, nil
}

func ScanCandidate(store *ledger.Store, generationPath, candidateID string) (ScanResult, error) {
	result := ScanResult{SchemaVersion: ScanResultSchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	generation, err := candidates.OpenGeneration(store, generationPath)
	if err != nil {
		return result, err
	}
	selection, err := generation.Select([]string{candidateID})
	if err != nil {
		return result, err
	}
	candidate := selection.Candidates[candidateID]
	return ScanResult{
		SchemaVersion: ScanResultSchemaVersion, CandidateGeneration: generation.Name,
		CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
		RequiresExplicitRuleChangeApproval: candidate.RequiresExplicitRuleChangeApproval,
		Report:                             secretscan.Scan(candidate.Text), Privacy: "local_only",
	}, nil
}

type applyHooks struct {
	afterPrepare func()
}

func Apply(store *ledger.Store, request Request) (ApplyResult, error) {
	return applyWithHooks(store, request, applyHooks{})
}

func applyWithHooks(store *ledger.Store, request Request, hooks applyHooks) (ApplyResult, error) {
	result := ApplyResult{SchemaVersion: ApplyResultSchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := validateRequest(request); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	lock, err := acquirePromotionLock(store, now)
	if err != nil {
		return result, err
	}
	released := false
	defer func() {
		if !released {
			_ = lock.release()
		}
	}()
	evidenceGuard, err := store.NewAppenderAfterVisit(nil)
	if err != nil {
		return result, fmt.Errorf("lock current evidence prefix: %w", err)
	}
	evidenceReleased := false
	defer func() {
		if !evidenceReleased {
			_ = evidenceGuard.Close()
		}
	}()
	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	eventID := "promotion-" + randomID
	revision, err := prepareRevision(store, request, state, now)
	if err != nil {
		return result, err
	}
	if hooks.afterPrepare != nil {
		hooks.afterPrepare()
	}
	requestDigest, err := hashRequest(request)
	if err != nil {
		return result, err
	}
	event := Event{
		SchemaVersion: EventSchemaVersion, EventID: eventID, RecordedAt: now,
		DeviceID: store.DeviceID(), Approver: request.Approver, RequestSHA256: requestDigest,
		Revision: revision, Privacy: "local_only",
	}
	if err := validateEvent(event); err != nil {
		return result, err
	}
	record, err := makeRecord(event, state.next, state.lastHash)
	if err != nil {
		return result, err
	}
	if err := appendRecord(store, record); err != nil {
		return result, err
	}
	result.EventID = event.EventID
	result.Sequence = record.Sequence
	result.RecordSHA256 = record.RecordSHA256
	result.Revision = revision
	if err := evidenceGuard.Close(); err != nil {
		return result, fmt.Errorf("memory promotion was appended but evidence lock cleanup failed: %w", err)
	}
	evidenceReleased = true
	if err := lock.release(); err != nil {
		return result, fmt.Errorf("memory promotion was appended but lock cleanup failed: %w", err)
	}
	released = true
	return result, nil
}

func GetStatus(store *ledger.Store, memoryID string) (StatusResult, error) {
	result := StatusResult{SchemaVersion: StatusSchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	if !validPrefixedHash(memoryID, "memory-") {
		return result, errors.New("memory id is invalid")
	}
	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	revision, exists := state.current[memoryID]
	if !exists {
		return result, errors.New("promoted memory was not found")
	}
	current := false
	if revision.Status == StatusActive && revision.Source != nil {
		_, err = review.ResolveCurrentValidation(
			store, revision.Source.CandidateGeneration, revision.Source.CandidateID,
			revision.Source.CandidateContentSHA256, revision.Source.ReviewRecordSHA256,
		)
		current = err == nil
	}
	return StatusResult{
		SchemaVersion: StatusSchemaVersion, MemoryID: memoryID,
		CurrentRevisionID: revision.RevisionID, Status: revision.Status,
		RevisionCount: state.counts[memoryID], SourceReviewCurrent: current,
		ExportEligible: revision.Status == StatusActive && current,
		Revision:       revision, Privacy: "local_only",
	}, nil
}

func GetRevision(store *ledger.Store, memoryID, revisionIDValue string) (Revision, error) {
	if store == nil {
		return Revision{}, errors.New("store is required")
	}
	if !validPrefixedHash(memoryID, "memory-") ||
		!validPrefixedHash(revisionIDValue, "memory-revision-") {
		return Revision{}, errors.New("memory or revision id is invalid")
	}
	state, err := replayVerified(store)
	if err != nil {
		return Revision{}, err
	}
	for _, record := range state.history {
		revision := record.Event.Revision
		if revision.MemoryID == memoryID && revision.RevisionID == revisionIDValue {
			return revision, nil
		}
	}
	return Revision{}, errors.New("promoted memory revision was not found")
}

func ListHistories(store *ledger.Store) ([]History, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	state, err := replayVerified(store)
	if err != nil {
		return nil, err
	}
	byMemory := make(map[string][]Revision, len(state.current))
	for _, record := range state.history {
		revision := cloneRevision(record.Event.Revision)
		byMemory[revision.MemoryID] = append(byMemory[revision.MemoryID], revision)
	}
	memoryIDs := make([]string, 0, len(byMemory))
	for memoryID := range byMemory {
		memoryIDs = append(memoryIDs, memoryID)
	}
	sort.Strings(memoryIDs)
	result := make([]History, 0, len(memoryIDs))
	for _, memoryID := range memoryIDs {
		result = append(result, History{MemoryID: memoryID, Revisions: byMemory[memoryID]})
	}
	return result, nil
}

func cloneRevision(revision Revision) Revision {
	if revision.Source != nil {
		source := *revision.Source
		source.ReviewBasis = append([]review.Basis(nil), source.ReviewBasis...)
		revision.Source = &source
	}
	if revision.Scan != nil {
		scan := *revision.Scan
		scan.FindingIDs = append([]string(nil), scan.FindingIDs...)
		scan.Redactions = append([]secretscan.Redaction(nil), scan.Redactions...)
		revision.Scan = &scan
	}
	return revision
}

func RevisionWasCurrentAt(
	store *ledger.Store, memoryID, revisionIDValue string, at time.Time,
) (bool, error) {
	if at.IsZero() {
		return false, errors.New("revision observation time is required")
	}
	if store == nil {
		return false, errors.New("store is required")
	}
	if !validPrefixedHash(memoryID, "memory-") ||
		!validPrefixedHash(revisionIDValue, "memory-revision-") {
		return false, errors.New("memory or revision id is invalid")
	}
	state, err := replayVerified(store)
	if err != nil {
		return false, err
	}
	var target *Revision
	var endedAt *time.Time
	for _, record := range state.history {
		revision := record.Event.Revision
		if revision.MemoryID != memoryID {
			continue
		}
		if target == nil {
			if revision.RevisionID == revisionIDValue {
				copy := revision
				target = &copy
			}
			continue
		}
		end := revision.RecordedAt
		endedAt = &end
		break
	}
	if target == nil {
		return false, errors.New("promoted memory revision was not found")
	}
	if target.Status != StatusActive || at.Before(target.RecordedAt) {
		return false, nil
	}
	return endedAt == nil || at.Before(*endedAt), nil
}

func prepareRevision(
	store *ledger.Store, request Request, state *replayState, now time.Time,
) (Revision, error) {
	base := Revision{
		SchemaVersion: RevisionSchemaVersion, Action: request.Action,
		RuleChangeAuthorization: "not_granted", RecordedAt: now,
		OriginDeviceID: store.DeviceID(), ApproverID: request.Approver.ID,
		Reason: request.Reason, Privacy: "local_only",
	}
	if request.Action == ActionRevoke {
		current, exists := state.current[request.MemoryID]
		if !exists || current.Status != StatusActive {
			return Revision{}, errors.New("only an active promoted memory can be revoked")
		}
		if current.RevisionID != request.ExpectedRevisionID {
			return Revision{}, errors.New("promotion request expected revision is stale")
		}
		base.MemoryID = current.MemoryID
		base.ParentRevisionID = current.RevisionID
		base.Status = StatusRevoked
		base.Kind = current.Kind
		base.Scope = current.Scope
		base.RequiresExplicitRuleChangeApproval = current.RequiresExplicitRuleChangeApproval
		id, err := revisionID(base)
		if err != nil {
			return Revision{}, err
		}
		base.RevisionID = id
		return base, nil
	}

	generation, err := candidates.OpenGeneration(store, request.Candidate.Generation)
	if err != nil {
		return Revision{}, err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return Revision{}, err
	}
	validated, err := review.ResolveCurrentValidation(
		store, request.Candidate.Generation, request.Candidate.CandidateID,
		request.Candidate.CandidateContentSHA256, request.Candidate.ExpectedReviewRecordSHA,
	)
	if err != nil {
		return Revision{}, fmt.Errorf("candidate is not currently validated: %w", err)
	}
	proof, candidate := validated.Proof, validated.Candidate
	report := secretscan.Scan(candidate.Text)
	redacted, err := secretscan.ApplyRedactions(candidate.Text, report, request.Redactions)
	if err != nil {
		return Revision{}, err
	}
	redactedSHA := textDigest(redacted)
	if request.ExpectedTextSHA256 != redactedSHA {
		return Revision{}, errors.New("redacted memory text does not match expected_text_sha256")
	}
	memoryID := ""
	switch request.Action {
	case ActionPromote:
		memoryID = expectedMemoryID(redactedSHA, proof.Scope)
		if request.MemoryID != "" || request.ExpectedRevisionID != "" {
			return Revision{}, errors.New("initial promotion request contains a parent identity")
		}
		if _, exists := state.current[memoryID]; exists {
			return Revision{}, errors.New("initial promotion conflicts with an existing memory")
		}
		for _, existing := range state.current {
			if existing.Source != nil && existing.Scope == proof.Scope &&
				existing.Source.SemanticKeySHA256 == candidate.SemanticKeySHA256 {
				return Revision{}, errors.New("a semantically matching memory already exists; supersede it instead")
			}
		}
	case ActionSupersede:
		memoryID = request.MemoryID
		current, exists := state.current[memoryID]
		if !exists || current.Status != StatusActive || current.Source == nil ||
			current.Scope != proof.Scope ||
			current.Source.SemanticKeySHA256 != candidate.SemanticKeySHA256 {
			return Revision{}, errors.New("supersession does not identify an active semantically matching memory")
		}
		if request.ExpectedRevisionID != current.RevisionID {
			return Revision{}, errors.New("promotion request expected revision is stale")
		}
		base.ParentRevisionID = current.RevisionID
	default:
		return Revision{}, errors.New("promotion action is invalid")
	}
	base.MemoryID = memoryID
	base.Status = StatusActive
	base.Kind = candidate.Kind
	base.Text = redacted
	base.TextSHA256 = redactedSHA
	base.Scope = proof.Scope
	base.Source = &Source{
		CandidateGeneration: proof.SourceCandidateGeneration,
		CandidatesSHA256:    proof.SourceCandidatesSHA256,
		CandidateID:         proof.CandidateID, CandidateContentSHA256: proof.CandidateContentSHA256,
		SemanticKeySHA256: candidate.SemanticKeySHA256, ReviewEventID: proof.ReviewEventID,
		ReviewRecordSHA256: proof.ReviewRecordSHA256, ReviewScope: proof.Scope,
		ReviewBasis: append([]review.Basis(nil), proof.Basis...),
	}
	base.Scan = &ScanAttestation{
		ScannerVersion: secretscan.ScannerVersion, SourceTextSHA256: report.ContentSHA256,
		FindingIDs: findingIDs(report), Redactions: append([]secretscan.Redaction(nil), request.Redactions...),
		RedactedTextSHA256: redactedSHA,
	}
	base.RequiresExplicitRuleChangeApproval = candidate.RequiresExplicitRuleChangeApproval
	id, err := revisionID(base)
	if err != nil {
		return Revision{}, err
	}
	base.RevisionID = id
	return base, nil
}

func validateRequest(request Request) error {
	if request.SchemaVersion != RequestSchemaVersion || !ValidApproverKind(request.Approver.Kind) ||
		strings.TrimSpace(request.Approver.ID) == "" || request.Approver.ID != strings.TrimSpace(request.Approver.ID) ||
		len(request.Approver.ID) > 256 || strings.TrimSpace(request.Reason) == "" ||
		request.Reason != strings.TrimSpace(request.Reason) || len(request.Reason) > 16*1024 {
		return errors.New("promotion request envelope is invalid")
	}
	if len(secretscan.Scan(request.Approver.ID).Findings) != 0 ||
		len(secretscan.Scan(request.Reason).Findings) != 0 {
		return errors.New("promotion request metadata contains sensitive content")
	}
	switch request.Action {
	case ActionPromote:
		if request.MemoryID != "" || request.ExpectedRevisionID != "" ||
			request.Candidate == nil || !validHash(request.ExpectedTextSHA256) {
			return errors.New("initial promotion request fields are invalid")
		}
	case ActionSupersede:
		if !validPrefixedHash(request.MemoryID, "memory-") ||
			!validPrefixedHash(request.ExpectedRevisionID, "memory-revision-") ||
			request.Candidate == nil || !validHash(request.ExpectedTextSHA256) {
			return errors.New("supersession request fields are invalid")
		}
	case ActionRevoke:
		if !validPrefixedHash(request.MemoryID, "memory-") ||
			!validPrefixedHash(request.ExpectedRevisionID, "memory-revision-") ||
			request.Candidate != nil || len(request.Redactions) != 0 || request.ExpectedTextSHA256 != "" {
			return errors.New("revocation request fields are invalid")
		}
	default:
		return errors.New("promotion request action is invalid")
	}
	if request.Candidate != nil {
		candidate := request.Candidate
		if strings.TrimSpace(candidate.Generation) == "" || filepath.Base(candidate.Generation) != candidate.Generation ||
			!validPrefixedHash(candidate.CandidateID, "candidate-") ||
			!validHash(candidate.CandidateContentSHA256) || !validHash(candidate.ExpectedReviewRecordSHA) {
			return errors.New("promotion candidate reference is invalid")
		}
	}
	return nil
}

func requestFromRevision(event Event) (Request, error) {
	revision := event.Revision
	request := Request{
		SchemaVersion: RequestSchemaVersion, Approver: event.Approver,
		Action: revision.Action, Reason: revision.Reason,
	}
	switch revision.Action {
	case ActionPromote, ActionSupersede:
		if revision.Source == nil || revision.Scan == nil {
			return Request{}, errors.New("active revision source is missing")
		}
		request.Candidate = &CandidateReference{
			Generation: revision.Source.CandidateGeneration, CandidateID: revision.Source.CandidateID,
			CandidateContentSHA256:  revision.Source.CandidateContentSHA256,
			ExpectedReviewRecordSHA: revision.Source.ReviewRecordSHA256,
		}
		request.Redactions = append([]secretscan.Redaction(nil), revision.Scan.Redactions...)
		request.ExpectedTextSHA256 = revision.TextSHA256
		if revision.Action == ActionSupersede {
			request.MemoryID = revision.MemoryID
			request.ExpectedRevisionID = revision.ParentRevisionID
		}
	case ActionRevoke:
		request.MemoryID = revision.MemoryID
		request.ExpectedRevisionID = revision.ParentRevisionID
	default:
		return Request{}, errors.New("revision action is invalid")
	}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func hashRequest(request Request) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode promotion request for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
