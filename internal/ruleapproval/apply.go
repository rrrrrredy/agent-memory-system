package ruleapproval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

func DecodeRequest(reader io.Reader) (Request, error) {
	if reader == nil {
		return Request{}, errors.New("rule approval request reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode rule approval request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Request{}, errors.New("rule approval request contains more than one JSON value")
		}
		return Request{}, fmt.Errorf("decode trailing rule approval request data: %w", err)
	}
	return request, nil
}

func Apply(store *ledger.Store, request Request) (ApplyResult, error) {
	result := ApplyResult{SchemaVersion: ApplyResultSchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := validateRequest(request); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	lock, err := acquireApprovalLock(store, now)
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
	key := approvalKey(request.MemoryID, request.RevisionID, request.Surface, request.Target)
	current := StatusNotAuthorized
	if value, exists := state.statuses[key]; exists {
		current = value
	}
	if current != request.ExpectedStatus {
		return result, fmt.Errorf("rule approval status is %q, not expected %q", current, request.ExpectedStatus)
	}
	if request.Action == ActionAuthorize {
		status, err := promotion.GetStatus(store, request.MemoryID)
		if err != nil {
			return result, err
		}
		if status.CurrentRevisionID != request.RevisionID || !status.ExportEligible {
			return result, errors.New("rule authorization requires the current eligible promoted revision")
		}
		currentAtDecision, err := promotion.RevisionWasCurrentAt(
			store, request.MemoryID, request.RevisionID, now,
		)
		if err != nil || !currentAtDecision {
			return result, errors.New("rule authorization time does not fall within the current revision")
		}
	} else {
		if _, err := promotion.GetRevision(store, request.MemoryID, request.RevisionID); err != nil {
			return result, err
		}
	}
	resulting := StatusAuthorized
	if request.Action == ActionRevoke {
		resulting = StatusNotAuthorized
	}
	requestDigest, err := hashRequest(request)
	if err != nil {
		return result, err
	}
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	event := Event{
		SchemaVersion: EventSchemaVersion, EventID: "rule-approval-" + randomID,
		RecordedAt: now, DeviceID: store.DeviceID(), Approver: request.Approver,
		RequestSHA256: requestDigest, MemoryID: request.MemoryID, RevisionID: request.RevisionID,
		Surface: request.Surface, Target: request.Target,
		ExpectedStatus: request.ExpectedStatus, Action: request.Action,
		ResultingStatus: resulting, Reason: request.Reason, Privacy: "local_only",
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
	result.MemoryID = request.MemoryID
	result.RevisionID = request.RevisionID
	result.Surface = request.Surface
	result.Target = request.Target
	result.Status = resulting
	if err := lock.release(); err != nil {
		return result, fmt.Errorf("rule approval was appended but lock cleanup failed: %w", err)
	}
	released = true
	return result, nil
}

func GetStatus(
	store *ledger.Store, memoryID, revisionID string, surface Surface, target string,
) (StatusResult, error) {
	result := StatusResult{
		SchemaVersion: StatusSchemaVersion, MemoryID: memoryID, RevisionID: revisionID,
		Surface: surface, Target: target,
		ApprovalStatus: StatusNotAuthorized, Privacy: "local_only",
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if !validPrefixedHash(memoryID, "memory-") ||
		!validPrefixedHash(revisionID, "memory-revision-") || !validSurface(surface) ||
		!validTarget(target) {
		return result, errors.New("rule approval status identity is invalid")
	}
	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	key := approvalKey(memoryID, revisionID, surface, target)
	if value, exists := state.statuses[key]; exists {
		result.ApprovalStatus = value
	}
	result.Events = state.counts[key]
	if result.ApprovalStatus == StatusAuthorized {
		memoryStatus, err := promotion.GetStatus(store, memoryID)
		result.Effective = err == nil && memoryStatus.CurrentRevisionID == revisionID &&
			memoryStatus.ExportEligible
	}
	return result, nil
}

func validateRequest(request Request) error {
	if request.SchemaVersion != RequestSchemaVersion || request.Approver.Kind != "human" ||
		strings.TrimSpace(request.Approver.ID) == "" || request.Approver.ID != strings.TrimSpace(request.Approver.ID) ||
		len(request.Approver.ID) > 256 || !validPrefixedHash(request.MemoryID, "memory-") ||
		!validPrefixedHash(request.RevisionID, "memory-revision-") || !validSurface(request.Surface) ||
		!validTarget(request.Target) ||
		strings.TrimSpace(request.Reason) == "" || request.Reason != strings.TrimSpace(request.Reason) ||
		len(request.Reason) > 16*1024 {
		return errors.New("rule approval request envelope is invalid")
	}
	if len(secretscan.Scan(request.Approver.ID).Findings) != 0 ||
		len(secretscan.Scan(request.Reason).Findings) != 0 ||
		len(secretscan.Scan(request.Target).Findings) != 0 {
		return errors.New("rule approval request metadata contains sensitive content")
	}
	switch request.Action {
	case ActionAuthorize:
		if request.ExpectedStatus != StatusNotAuthorized {
			return errors.New("rule authorization must expect not_authorized")
		}
	case ActionRevoke:
		if request.ExpectedStatus != StatusAuthorized {
			return errors.New("rule approval revocation must expect authorized")
		}
	default:
		return errors.New("rule approval action is invalid")
	}
	return nil
}

func hashRequest(request Request) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode rule approval request for hash: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
