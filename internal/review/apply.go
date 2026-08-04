package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeRequest(reader io.Reader) (Request, error) {
	if reader == nil {
		return Request{}, errors.New("review request reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode review request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("review request contains more than one JSON value")
		}
		return Request{}, fmt.Errorf("decode trailing review request data: %w", err)
	}
	return request, nil
}

func Apply(store *ledger.Store, generationPath string, request Request) (ApplyResult, error) {
	result := ApplyResult{SchemaVersion: ApplySchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := validateRequestEnvelope(request); err != nil {
		return result, err
	}
	generation, err := candidates.OpenGeneration(store, generationPath)
	if err != nil {
		return result, err
	}
	candidateIDs := make([]string, 0, len(request.Transitions))
	for _, transition := range request.Transitions {
		candidateIDs = append(candidateIDs, transition.CandidateID)
	}
	selection, err := generation.Select(candidateIDs)
	if err != nil {
		return result, err
	}
	if err := verifyCandidateEvidence(store, selection.Candidates); err != nil {
		return result, err
	}

	now := time.Now().UTC()
	lock, err := acquireReviewLock(store, now)
	if err != nil {
		return result, err
	}
	released := false
	defer func() {
		if !released {
			_ = lock.release()
		}
	}()

	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	transitions, err := prepareTransitions(
		request, generation.Manifest.CandidatesSHA256, selection, state,
	)
	if err != nil {
		return result, err
	}
	if err := verifyReferencedEvidence(store, transitions); err != nil {
		return result, err
	}
	requestDigest, err := hashRequest(generation, request)
	if err != nil {
		return result, err
	}
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	event := Event{
		SchemaVersion: EventSchemaVersion, EventID: "review-" + randomID,
		RecordedAt: now, DeviceID: store.DeviceID(), Reviewer: request.Reviewer,
		RequestSHA256: requestDigest, SourceCandidateGeneration: generation.Name,
		SourceCandidatesSHA256: generation.Manifest.CandidatesSHA256,
		ConflictGroupID:        request.ConflictGroupID, Transitions: transitions, Privacy: "local_only",
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
	result.Transitions = transitions
	if err := lock.release(); err != nil {
		return result, fmt.Errorf("review decision was appended but lock cleanup failed: %w", err)
	}
	released = true
	return result, nil
}

func GetStatus(
	store *ledger.Store, generationPath, candidateID string,
) (StatusResult, error) {
	result := StatusResult{SchemaVersion: StatusSchemaVersion, Privacy: "local_only"}
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
	if err := verifyCandidateEvidence(store, selection.Candidates); err != nil {
		return result, err
	}
	candidate := selection.Candidates[candidateID]
	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	key := candidateKey(
		generation.Manifest.CandidatesSHA256, candidate.CandidateID, candidate.ContentSHA256,
	)
	status := initialStatus(candidate)
	if reviewed, exists := state.statuses[key]; exists {
		status = reviewed
	}
	return StatusResult{
		SchemaVersion: StatusSchemaVersion, CandidateID: candidate.CandidateID,
		CandidateContentSHA256:   candidate.ContentSHA256,
		CandidateDerivationState: string(candidate.Validation.Status), ReviewStatus: status,
		ReviewEvents: state.events[key], LastReviewRecordSHA256: state.lastByKey[key],
		Privacy: "local_only",
	}, nil
}

func validateRequestEnvelope(request Request) error {
	if request.SchemaVersion != RequestSchemaVersion || request.Reviewer.Kind != "human" ||
		strings.TrimSpace(request.Reviewer.ID) == "" || request.Reviewer.ID != strings.TrimSpace(request.Reviewer.ID) ||
		len(request.Reviewer.ID) > 256 || len(request.Transitions) == 0 {
		return errors.New("review request envelope is invalid")
	}
	if request.ConflictGroupID != "" && !validPrefixedHash(request.ConflictGroupID, "candidate-conflict-") {
		return errors.New("review request conflict group is invalid")
	}
	previousID := ""
	for _, transition := range request.Transitions {
		if !validPrefixedHash(transition.CandidateID, "candidate-") ||
			!validHash(transition.CandidateContentSHA256) || !validStatus(transition.ExpectedStatus) ||
			strings.TrimSpace(transition.Reason) == "" || len(transition.Reason) > 16*1024 ||
			!sortedUniqueBasis(transition.Basis) || !sortedUniqueStrings(transition.EvidenceEventIDs) {
			return errors.New("review request transition is invalid")
		}
		if previousID != "" && transition.CandidateID <= previousID {
			return errors.New("review request transitions must be strictly sorted by candidate_id")
		}
		previousID = transition.CandidateID
		switch transition.Action {
		case ActionValidate:
			if transition.Scope == nil || !validScope(*transition.Scope) || len(transition.Basis) == 0 {
				return errors.New("validate request requires a scope and validation basis")
			}
		case ActionReject, ActionQuarantine, ActionReopen:
			if transition.Scope != nil || len(transition.Basis) > 0 || len(transition.EvidenceEventIDs) > 0 {
				return errors.New("non-validation request cannot carry scope, basis, or evidence ids")
			}
		default:
			return errors.New("review request action is invalid")
		}
	}
	if request.ConflictGroupID == "" && len(request.Transitions) != 1 {
		return errors.New("a non-conflict review request must contain exactly one transition")
	}
	return nil
}

func prepareTransitions(
	request Request, sourceCandidatesSHA256 string, selection candidates.Selection, state *replayState,
) ([]Transition, error) {
	if request.ConflictGroupID != "" {
		members, exists := selection.ConflictGroups[request.ConflictGroupID]
		if !exists {
			return nil, errors.New("review request conflict group is not present in the candidate generation")
		}
		requested := make([]string, 0, len(request.Transitions))
		for _, transition := range request.Transitions {
			requested = append(requested, transition.CandidateID)
		}
		if !equalStringSlices(members, requested) {
			return nil, errors.New("conflict resolution must include every candidate in the conflict group")
		}
	}

	result := make([]Transition, 0, len(request.Transitions))
	validated := 0
	for _, requested := range request.Transitions {
		candidate := selection.Candidates[requested.CandidateID]
		if requested.CandidateContentSHA256 != candidate.ContentSHA256 {
			return nil, fmt.Errorf("candidate %q content hash does not match the verified generation", candidate.CandidateID)
		}
		current := initialStatus(candidate)
		key := candidateKey(sourceCandidatesSHA256, candidate.CandidateID, candidate.ContentSHA256)
		if reviewed, exists := state.statuses[key]; exists {
			current = reviewed
		}
		if requested.ExpectedStatus != current {
			return nil, fmt.Errorf(
				"candidate %q status is %q, not expected %q",
				candidate.CandidateID, current, requested.ExpectedStatus,
			)
		}
		resulting, err := resultingStatus(requested.Action, current, initialStatus(candidate),
			request.ConflictGroupID != "")
		if err != nil {
			return nil, fmt.Errorf("candidate %q: %w", candidate.CandidateID, err)
		}
		if requested.Action == ActionValidate {
			validated++
			if candidate.ConflictGroupID != "" && request.ConflictGroupID != candidate.ConflictGroupID {
				return nil, errors.New("a quarantined conflict candidate can only be validated by full-group resolution")
			}
			if err := validateBasis(candidate, requested); err != nil {
				return nil, fmt.Errorf("candidate %q: %w", candidate.CandidateID, err)
			}
		}
		if request.ConflictGroupID != "" {
			if candidate.ConflictGroupID != request.ConflictGroupID || current != StatusQuarantined ||
				(requested.Action != ActionValidate && requested.Action != ActionReject) {
				return nil, errors.New("conflict resolution requires quarantined members and validate/reject actions")
			}
		}
		result = append(result, Transition{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: requested.ExpectedStatus, Action: requested.Action, ResultingStatus: resulting,
			Scope: requested.Scope, Basis: append([]Basis(nil), requested.Basis...),
			EvidenceEventIDs: append([]string(nil), requested.EvidenceEventIDs...), Reason: requested.Reason,
		})
	}
	if request.ConflictGroupID != "" && validated > 1 {
		return nil, errors.New("conflict resolution may validate at most one candidate")
	}
	return result, nil
}

func resultingStatus(action Action, current, base Status, conflictResolution bool) (Status, error) {
	switch action {
	case ActionValidate:
		if current != StatusPending && !(conflictResolution && current == StatusQuarantined) {
			return "", errors.New("validate requires pending status or a full conflict resolution")
		}
		return StatusValidated, nil
	case ActionReject:
		if current == StatusRejected {
			return "", errors.New("reject would not change status")
		}
		return StatusRejected, nil
	case ActionQuarantine:
		if current != StatusPending && current != StatusValidated {
			return "", errors.New("quarantine requires pending or validated status")
		}
		return StatusQuarantined, nil
	case ActionReopen:
		if current != StatusRejected && current != StatusQuarantined {
			return "", errors.New("reopen requires rejected or quarantined status")
		}
		if current == base {
			return "", errors.New("reopen would not change status")
		}
		return base, nil
	default:
		return "", errors.New("invalid review action")
	}
}

func validateBasis(candidate candidates.Candidate, requested TransitionRequest) error {
	strong := false
	for _, basis := range requested.Basis {
		switch basis {
		case BasisExplicitRemember:
			if !hasCandidateSupport(candidate.SupportTypes, candidates.SupportExplicitRemember) {
				return errors.New("explicit_remember basis is not present in candidate evidence")
			}
			strong = true
		case BasisUserCorrection:
			if !hasCandidateSupport(candidate.SupportTypes, candidates.SupportUserCorrection) {
				return errors.New("user_correction basis is not present in candidate evidence")
			}
			strong = true
		case BasisStableRepetition:
			if !hasCandidateSupport(candidate.SupportTypes, candidates.SupportStableRepetition) {
				return errors.New("stable_repetition basis is not present in candidate evidence")
			}
			strong = true
		case BasisOutcomeEvidence, BasisExplicitUserConfirmation:
			if len(requested.EvidenceEventIDs) == 0 {
				return fmt.Errorf("%s basis requires evidence_event_ids", basis)
			}
			strong = true
		}
	}
	if !strong {
		return errors.New("validation requires user-origin, repeated, or outcome evidence")
	}
	return nil
}

func verifyReferencedEvidence(store *ledger.Store, transitions []Transition) error {
	wanted := map[string]struct{}{}
	for _, transition := range transitions {
		for _, eventID := range transition.EvidenceEventIDs {
			wanted[eventID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	found := map[string]ledger.Event{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, exists := wanted[record.Event.EventID]; exists {
			found[record.Event.EventID] = record.Event
		}
		return nil
	}); err != nil {
		return fmt.Errorf("verify review evidence: %w", err)
	}
	for eventID := range wanted {
		event, exists := found[eventID]
		if !exists {
			return fmt.Errorf("review evidence event %q was not found in the verified ledger", eventID)
		}
		if err := verifyEvidencePayload(store, event); err != nil {
			return fmt.Errorf("review evidence event %q: %w", eventID, err)
		}
	}
	for _, transition := range transitions {
		if hasBasis(transition.Basis, BasisOutcomeEvidence) {
			matched := false
			for _, eventID := range transition.EvidenceEventIDs {
				switch found[eventID].Kind {
				case ledger.KindToolResult, ledger.KindFileChange:
					matched = true
				}
			}
			if !matched {
				return fmt.Errorf("candidate %q outcome evidence has no eligible result event", transition.CandidateID)
			}
		}
		if hasBasis(transition.Basis, BasisExplicitUserConfirmation) {
			matched := false
			for _, eventID := range transition.EvidenceEventIDs {
				matched = matched || found[eventID].Kind == ledger.KindUserMessage
			}
			if !matched {
				return fmt.Errorf("candidate %q has no user-message confirmation event", transition.CandidateID)
			}
		}
	}
	return nil
}

func verifyCandidateEvidence(store *ledger.Store, selected map[string]candidates.Candidate) error {
	wanted := map[string]struct{}{}
	for _, candidate := range selected {
		for _, observation := range candidate.Observations {
			for _, eventID := range observation.EvidenceEventIDs {
				wanted[eventID] = struct{}{}
			}
			for _, eventID := range observation.CorrectionEventIDs {
				wanted[eventID] = struct{}{}
			}
		}
	}
	if len(wanted) == 0 {
		return errors.New("selected candidate has no evidence event references")
	}
	found := map[string]ledger.Event{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, exists := wanted[record.Event.EventID]; !exists {
			return nil
		}
		if existing, duplicate := found[record.Event.EventID]; duplicate &&
			!reflect.DeepEqual(existing, record.Event) {
			return fmt.Errorf("candidate evidence event %q has conflicting ledger records", record.Event.EventID)
		}
		found[record.Event.EventID] = record.Event
		return nil
	}); err != nil {
		return fmt.Errorf("verify candidate evidence ledger: %w", err)
	}
	for eventID := range wanted {
		event, exists := found[eventID]
		if !exists {
			return fmt.Errorf("candidate evidence event %q was not found in the verified ledger", eventID)
		}
		if event.Kind != ledger.KindUserMessage {
			return fmt.Errorf("candidate evidence event %q is not a user message", eventID)
		}
		if err := verifyEvidencePayload(store, event); err != nil {
			return fmt.Errorf("candidate evidence event %q: %w", eventID, err)
		}
	}
	return nil
}

func verifyEvidencePayload(store *ledger.Store, event ledger.Event) error {
	if event.Completeness.Status != ledger.CompletenessComplete || event.Payload == nil {
		return errors.New("referenced review evidence is not complete")
	}
	if event.Payload.Blob == nil {
		return nil
	}
	file, err := store.OpenBlob(*event.Payload.Blob)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return fmt.Errorf("read review evidence blob: %w", err)
	}
	if size != event.Payload.Bytes || hex.EncodeToString(hasher.Sum(nil)) != event.Payload.SHA256 {
		return errors.New("review evidence blob failed content verification")
	}
	return nil
}

func verifyReviewSemantics(store *ledger.Store, records []Record) error {
	if len(records) == 0 {
		return nil
	}
	type wantedGeneration struct {
		sha256 string
		ids    map[string]struct{}
	}
	type verifiedGeneration struct {
		generation candidates.Generation
		selection  candidates.Selection
	}
	wanted := map[string]*wantedGeneration{}
	for _, record := range records {
		name := record.Event.SourceCandidateGeneration
		item, exists := wanted[name]
		if !exists {
			item = &wantedGeneration{sha256: record.Event.SourceCandidatesSHA256, ids: map[string]struct{}{}}
			wanted[name] = item
		} else if item.sha256 != record.Event.SourceCandidatesSHA256 {
			return fmt.Errorf("review generation %q is referenced with conflicting hashes", name)
		}
		for _, transition := range record.Event.Transitions {
			item.ids[transition.CandidateID] = struct{}{}
		}
	}
	verified := map[string]verifiedGeneration{}
	for name, item := range wanted {
		generation, err := candidates.OpenGeneration(store, name)
		if err != nil {
			return fmt.Errorf("verify review generation %q: %w", name, err)
		}
		if generation.Manifest.CandidatesSHA256 != item.sha256 {
			return fmt.Errorf("review generation %q content hash changed", name)
		}
		ids := make([]string, 0, len(item.ids))
		for candidateID := range item.ids {
			ids = append(ids, candidateID)
		}
		sort.Strings(ids)
		selection, err := generation.Select(ids)
		if err != nil {
			return fmt.Errorf("verify review generation %q: %w", name, err)
		}
		if err := verifyCandidateEvidence(store, selection.Candidates); err != nil {
			return fmt.Errorf("verify review generation %q: %w", name, err)
		}
		verified[name] = verifiedGeneration{generation: generation, selection: selection}
	}
	semanticState := &replayState{statuses: map[string]Status{}}
	for _, record := range records {
		item := verified[record.Event.SourceCandidateGeneration]
		request := Request{
			SchemaVersion: RequestSchemaVersion, Reviewer: record.Event.Reviewer,
			ConflictGroupID: record.Event.ConflictGroupID,
			Transitions:     make([]TransitionRequest, 0, len(record.Event.Transitions)),
		}
		for _, transition := range record.Event.Transitions {
			request.Transitions = append(request.Transitions, TransitionRequest{
				CandidateID: transition.CandidateID, CandidateContentSHA256: transition.CandidateContentSHA256,
				ExpectedStatus: transition.ExpectedStatus, Action: transition.Action, Scope: transition.Scope,
				Basis:            append([]Basis(nil), transition.Basis...),
				EvidenceEventIDs: append([]string(nil), transition.EvidenceEventIDs...), Reason: transition.Reason,
			})
		}
		if err := validateRequestEnvelope(request); err != nil {
			return fmt.Errorf("review record %d request is invalid: %w", record.Sequence, err)
		}
		prepared, err := prepareTransitions(
			request, item.generation.Manifest.CandidatesSHA256, item.selection, semanticState,
		)
		if err != nil {
			return fmt.Errorf("review record %d cannot be replayed: %w", record.Sequence, err)
		}
		if !reflect.DeepEqual(prepared, record.Event.Transitions) {
			return fmt.Errorf("review record %d transition result is not reproducible", record.Sequence)
		}
		if err := verifyReferencedEvidence(store, prepared); err != nil {
			return fmt.Errorf("review record %d evidence is invalid: %w", record.Sequence, err)
		}
		digest, err := hashRequest(item.generation, request)
		if err != nil {
			return err
		}
		if digest != record.Event.RequestSHA256 {
			return fmt.Errorf("review record %d request hash mismatch", record.Sequence)
		}
		for _, transition := range prepared {
			key := candidateKey(
				item.generation.Manifest.CandidatesSHA256,
				transition.CandidateID,
				transition.CandidateContentSHA256,
			)
			semanticState.statuses[key] = transition.ResultingStatus
		}
	}
	return nil
}

func hashRequest(generation candidates.Generation, request Request) (string, error) {
	envelope := struct {
		SourceCandidateGeneration string  `json:"source_candidate_generation"`
		SourceCandidatesSHA256    string  `json:"source_candidates_sha256"`
		Request                   Request `json:"request"`
	}{generation.Name, generation.Manifest.CandidatesSHA256, request}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode review request for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func initialStatus(candidate candidates.Candidate) Status {
	if candidate.Validation.Status == candidates.StatusQuarantined {
		return StatusQuarantined
	}
	return StatusPending
}

func hasCandidateSupport(values []candidates.SupportType, wanted candidates.SupportType) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= wanted })
	return index < len(values) && values[index] == wanted
}

func hasBasis(values []Basis, wanted Basis) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= wanted })
	return index < len(values) && values[index] == wanted
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
