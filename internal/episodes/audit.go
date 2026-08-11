package episodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const generationAuditAdapter = "episode-derivation-audit"

func recordGenerationAttempt(store *ledger.Store) (GenerationAttempt, error) {
	attempt := GenerationAttempt{SchemaVersion: GenerationAttemptSchema,
		DerivationVersion: DerivationVersion, Privacy: "local_only"}
	var previous string
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		attempt.SourceRecords++
		previous = record.RecordHash
		return nil
	})
	if err != nil {
		return attempt, fmt.Errorf("begin episode generation before derivation: %w", err)
	}
	attempt.SourceLastRecordHash = previous
	attempt.StartedAt = time.Now().UTC()
	attempt.AttemptID = generationAttemptID(attempt)
	data, err := json.Marshal(attempt)
	if err != nil {
		_ = appender.Close()
		return attempt, fmt.Errorf("encode episode generation attempt: %w", err)
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: attempt.AttemptID,
		Kind: ledger.KindEpisodeGenerationAttempt, ObservedAt: attempt.StartedAt, RecordedAt: attempt.StartedAt,
		Source: ledger.Source{Agent: ledger.AgentUnknown, Adapter: generationAuditAdapter,
			AdapterVersion: DerivationVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: attempt.AttemptID, SourceEventID: attempt.SourceLastRecordHash,
			SourceCursor: "ledger-prefix:" + strconv.Itoa(attempt.SourceRecords)},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}
	_, appendErr := appender.AppendBatch([]ledger.Event{event})
	closeErr := appender.Close()
	if appendErr != nil && closeErr != nil {
		return attempt, errors.Join(fmt.Errorf("append episode generation attempt: %w", appendErr), closeErr)
	}
	if appendErr != nil {
		return attempt, fmt.Errorf("append episode generation attempt: %w", appendErr)
	}
	if closeErr != nil {
		return attempt, closeErr
	}
	return attempt, nil
}

func generationAttemptID(attempt GenerationAttempt) string {
	return deterministicID("episode-generation-attempt", attempt.DerivationVersion,
		strconv.Itoa(attempt.SourceRecords), attempt.SourceLastRecordHash)
}

// ListVerifiedGenerationAttempts replays immutable markers committed before
// any detector computation. A marker remains valid even when no generation was
// completed, so failed or crashed builds cannot erase prior detector access.
func ListVerifiedGenerationAttempts(store *ledger.Store) ([]VerifiedGenerationAttempt, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	results := []VerifiedGenerationAttempt{}
	index := 0
	previous := ""
	if err := store.VisitRecords(func(record ledger.Record) error {
		index++
		if record.Event.Kind == ledger.KindEpisodeGenerationAttempt {
			verified, err := verifyGenerationAttemptRecord(record, index, previous)
			if err != nil {
				return err
			}
			results = append(results, verified)
		}
		previous = record.RecordHash
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func verifyGenerationAttemptRecord(record ledger.Record, index int,
	previous string) (VerifiedGenerationAttempt, error) {
	result := VerifiedGenerationAttempt{Record: record, LedgerIndex: index}
	event := record.Event
	if event.SchemaVersion != ledger.SchemaVersionV1Alpha2 || event.Kind != ledger.KindEpisodeGenerationAttempt ||
		event.Source.Agent != ledger.AgentUnknown || event.Source.Adapter != generationAuditAdapter ||
		event.Source.AdapterVersion != DerivationVersion || event.Payload == nil || event.Payload.Content == nil ||
		event.Payload.Blob != nil || event.Payload.Encoding != "utf-8" || event.Payload.MediaType != "application/json" ||
		event.Completeness.Status != ledger.CompletenessComplete || event.Privacy.Classification != "local_only" {
		return result, errors.New("episode generation attempt event is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(*event.Payload.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Attempt); err != nil {
		return result, errors.New("episode generation attempt payload is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, errors.New("episode generation attempt payload has trailing data")
	}
	attempt := result.Attempt
	validPrefix := attempt.SourceRecords == index-1 && attempt.SourceLastRecordHash == previous
	if attempt.SourceRecords == 0 {
		validPrefix = validPrefix && attempt.SourceLastRecordHash == ""
	} else {
		validPrefix = validPrefix && validAuditSHA(attempt.SourceLastRecordHash)
	}
	if attempt.SchemaVersion != GenerationAttemptSchema || attempt.DerivationVersion != DerivationVersion ||
		attempt.SourceRecords < 0 || !validPrefix || attempt.Privacy != "local_only" ||
		attempt.StartedAt.IsZero() || !attempt.StartedAt.Equal(event.ObservedAt) ||
		!attempt.StartedAt.Equal(event.RecordedAt) || attempt.AttemptID != event.EventID ||
		attempt.AttemptID != generationAttemptID(attempt) || event.Source.ThreadID != attempt.AttemptID ||
		event.Source.SourceEventID != attempt.SourceLastRecordHash ||
		event.Source.SourceCursor != "ledger-prefix:"+strconv.Itoa(attempt.SourceRecords) {
		return result, errors.New("episode generation attempt envelope is invalid")
	}
	return result, nil
}

func recordGenerationAudit(store *ledger.Store, result BuildResult) error {
	manifestData, err := os.ReadFile(filepath.Join(result.GenerationPath, "manifest.json"))
	if err != nil {
		return fmt.Errorf("read episode generation manifest for audit: %w", err)
	}
	manifestDigest := sha256.Sum256(manifestData)
	audit := GenerationAudit{SchemaVersion: GenerationAuditSchema, DerivationVersion: result.DerivationVersion,
		SourceRecords: result.SourceRecords, SourceLastRecordHash: result.SourceLastRecordHash,
		GenerationName: filepath.Base(result.GenerationPath), ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		EpisodesSHA256: result.EpisodesSHA256, TimelineSHA256: result.TimelineSHA256,
		Episodes: result.Episodes, TimelineEntries: result.TimelineEntries, Compactions: result.Compactions,
		Privacy: "local_only"}
	audit.AuditID = generationAuditID(audit)
	data, err := json.Marshal(audit)
	if err != nil {
		return fmt.Errorf("encode episode generation audit: %w", err)
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	now := time.Now().UTC()
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: audit.AuditID,
		Kind: ledger.KindEpisodeGeneration, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentUnknown, Adapter: generationAuditAdapter,
			AdapterVersion: DerivationVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID:      audit.GenerationName,
			SourceEventID: audit.SourceLastRecordHash,
			SourceCursor:  "ledger-prefix:" + strconv.Itoa(audit.SourceRecords)},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}
	visited := 0
	previous := ""
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		visited++
		previous = record.RecordHash
		return nil
	})
	if err != nil {
		return fmt.Errorf("open episode generation audit append: %w", err)
	}
	if visited != result.SourceRecords || previous != result.SourceLastRecordHash {
		closeErr := appender.Close()
		staleErr := errors.New("episode generation became stale before its completion audit")
		if closeErr != nil {
			return errors.Join(staleErr, closeErr)
		}
		return staleErr
	}
	_, appendErr := appender.AppendBatch([]ledger.Event{event})
	closeErr := appender.Close()
	if appendErr != nil && closeErr != nil {
		return errors.Join(fmt.Errorf("append episode generation audit: %w", appendErr), closeErr)
	}
	if appendErr != nil {
		return fmt.Errorf("append episode generation audit: %w", appendErr)
	}
	return closeErr
}

func generationAuditID(audit GenerationAudit) string {
	return deterministicID("episode-generation-audit", audit.DerivationVersion,
		strconv.Itoa(audit.SourceRecords), audit.SourceLastRecordHash, audit.GenerationName,
		audit.ManifestSHA256, audit.EpisodesSHA256, audit.TimelineSHA256,
		strconv.Itoa(audit.Episodes), strconv.FormatInt(audit.TimelineEntries, 10), strconv.Itoa(audit.Compactions))
}

func loadLastAuditedGeneration(store *ledger.Store) (BuildResult, bool, error) {
	var last ledger.Record
	index := 0
	if err := store.VisitRecords(func(record ledger.Record) error {
		last = record
		index++
		return nil
	}); err != nil {
		return BuildResult{}, false, err
	}
	if index == 0 || last.Event.Kind != ledger.KindEpisodeGeneration {
		return BuildResult{}, false, nil
	}
	verified, err := verifyGenerationAuditRecord(store, last, index)
	if err != nil {
		return BuildResult{}, false, err
	}
	return verified.Generation, true, nil
}

func ListVerifiedGenerationAudits(store *ledger.Store) ([]VerifiedGenerationAudit, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	results := []VerifiedGenerationAudit{}
	index := 0
	if err := store.VisitRecords(func(record ledger.Record) error {
		index++
		if record.Event.Kind != ledger.KindEpisodeGeneration {
			return nil
		}
		verified, err := verifyGenerationAuditRecord(store, record, index)
		if err != nil {
			return err
		}
		results = append(results, verified)
		return nil
	}); err != nil {
		return nil, err
	}
	return results, nil
}

func verifyGenerationAuditRecord(store *ledger.Store, record ledger.Record,
	index int) (VerifiedGenerationAudit, error) {
	result := VerifiedGenerationAudit{Record: record, LedgerIndex: index}
	event := record.Event
	if event.SchemaVersion != ledger.SchemaVersionV1Alpha2 || event.Kind != ledger.KindEpisodeGeneration ||
		event.Source.Agent != ledger.AgentUnknown || event.Source.Adapter != generationAuditAdapter ||
		event.Source.AdapterVersion != DerivationVersion || event.Payload == nil || event.Payload.Content == nil ||
		event.Payload.Blob != nil || event.Payload.Encoding != "utf-8" || event.Payload.MediaType != "application/json" ||
		event.Completeness.Status != ledger.CompletenessComplete || event.Privacy.Classification != "local_only" {
		return result, errors.New("episode generation audit event is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(*event.Payload.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Audit); err != nil {
		return result, errors.New("episode generation audit payload is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, errors.New("episode generation audit payload has trailing data")
	}
	audit := result.Audit
	if audit.SchemaVersion != GenerationAuditSchema || audit.DerivationVersion != DerivationVersion ||
		audit.SourceRecords < 1 || !validAuditSHA(audit.SourceLastRecordHash) ||
		audit.SourceRecords != index-1 || audit.SourceLastRecordHash != record.PreviousRecordHash ||
		!validAuditSHA(audit.ManifestSHA256) || !validAuditSHA(audit.EpisodesSHA256) ||
		!validAuditSHA(audit.TimelineSHA256) || audit.GenerationName == "" || audit.Privacy != "local_only" ||
		audit.AuditID != event.EventID || audit.AuditID != generationAuditID(audit) {
		return result, errors.New("episode generation audit envelope is invalid")
	}
	generation, _, err := LoadVerifiedGeneration(store, audit.SourceRecords, audit.SourceLastRecordHash)
	if err != nil || filepath.Base(generation.GenerationPath) != audit.GenerationName ||
		generation.EpisodesSHA256 != audit.EpisodesSHA256 || generation.TimelineSHA256 != audit.TimelineSHA256 ||
		generation.Episodes != audit.Episodes || generation.TimelineEntries != audit.TimelineEntries ||
		generation.Compactions != audit.Compactions {
		return result, errors.New("episode generation audit differs from its retained generation")
	}
	manifestData, err := os.ReadFile(filepath.Join(generation.GenerationPath, "manifest.json"))
	manifestDigest := sha256.Sum256(manifestData)
	if err != nil || hex.EncodeToString(manifestDigest[:]) != audit.ManifestSHA256 {
		return result, errors.New("episode generation audit manifest hash is invalid")
	}
	result.Generation = generation
	return result, nil
}

func validAuditSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
