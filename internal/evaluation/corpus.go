package evaluation

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	legacyAdapterName    = "legacy-context-journal"
	legacyAdapterVersion = "legacy-context-journal/v1alpha1"
	maxLegacyIndexBytes  = 64 << 20
)

type legacyIndexEntry struct {
	CardPath        string `json:"card_path"`
	SessionID       string `json:"session_id"`
	TranscriptPath  string `json:"transcript_path"`
	LatestUser      string `json:"latest_user"`
	LatestAssistant string `json:"latest_assistant"`
}

type snapshotPlan struct {
	artifact CorpusArtifact
	event    ledger.Event
}

type rolloutState struct {
	reference    RolloutReference
	partial      bool
	snapshotSeen map[string]struct{}
	blobSeen     map[string]struct{}
}

type freezeScanState struct {
	lastRecordHash string
	existingEvents map[string]ledger.Event
	evaluationIDs  map[string]struct{}
	rollouts       map[string]*rolloutState
}

func FreezeLegacyCorpus(store *ledger.Store, legacyRoot string, options FreezeOptions) (result FreezeResult, returnedErr error) {
	result = FreezeResult{SchemaVersion: CorpusFreezeResultSchema, Issues: []CorpusIssue{}, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(legacyRoot) == "" {
		return result, errors.New("legacy root is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if strings.TrimSpace(options.Name) == "" {
		options.Name = "legacy-context-journal"
	}
	if !safeIdentifier(options.Name) {
		return result, errors.New("legacy corpus name must be a safe identifier")
	}
	absoluteRoot, err := filepath.Abs(legacyRoot)
	if err != nil {
		return result, fmt.Errorf("resolve legacy root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return result, fmt.Errorf("inspect legacy root: %w", err)
	}
	if !info.IsDir() {
		return result, errors.New("legacy root is not a directory")
	}
	absoluteRoot, err = filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return result, fmt.Errorf("resolve legacy root links: %w", err)
	}
	if pathsOverlap(absoluteRoot, store.Root()) {
		return result, errors.New("legacy source and evidence root must be separate")
	}
	sourceRootHash, err := adapterjsonl.HashSourcePath(absoluteRoot)
	if err != nil {
		return result, err
	}

	indexPath := filepath.Join(absoluteRoot, "data", "index.jsonl")
	if err := requireResolvedRegularFile(absoluteRoot, indexPath); err != nil {
		return result, fmt.Errorf("inspect legacy index: %w", err)
	}
	indexData, err := readBoundedFile(indexPath, maxLegacyIndexBytes)
	if err != nil {
		return result, fmt.Errorf("read legacy index: %w", err)
	}
	entries, indexIssues, err := parseLegacyIndex(indexData)
	if err != nil {
		return result, err
	}
	result.Issues = append(result.Issues, indexIssues...)
	result.Counts.IndexEntries = len(entries)

	cardFiles, err := collectLegacyCards(absoluteRoot)
	if err != nil {
		return result, err
	}
	cardSessions := map[string]map[string]struct{}{}
	indexedCards := map[string]struct{}{}
	rolloutReferences := map[string]*rolloutState{}
	sessions := map[string]struct{}{}
	for index, entry := range entries {
		if strings.TrimSpace(entry.LatestUser) != "" {
			result.Counts.CardsWithLatestUser++
		}
		if strings.TrimSpace(entry.LatestAssistant) != "" {
			result.Counts.CardsWithLatestAssistant++
		}
		if strings.TrimSpace(entry.SessionID) != "" {
			sessions[entry.SessionID] = struct{}{}
		}
		cardPath, cardErr := trustedLegacyCardPath(absoluteRoot, entry.CardPath)
		if cardErr != nil {
			result.Issues = append(result.Issues, CorpusIssue{
				Code: "unsafe_index_card_path", ArtifactID: fmt.Sprintf("index-line-%d", index+1),
			})
		} else {
			key := normalizePathKey(cardPath)
			indexedCards[key] = struct{}{}
			if _, exists := cardSessions[key]; !exists {
				cardSessions[key] = map[string]struct{}{}
			}
			if strings.TrimSpace(entry.SessionID) != "" {
				cardSessions[key][entry.SessionID] = struct{}{}
			}
			if _, statErr := os.Stat(cardPath); statErr != nil {
				result.Counts.MissingCards++
				pathHash, _ := adapterjsonl.HashSourcePath(cardPath)
				result.Issues = append(result.Issues, CorpusIssue{
					Code: "indexed_card_missing", SourcePathSHA256: pathHash,
				})
			}
		}

		if strings.TrimSpace(entry.TranscriptPath) == "" {
			continue
		}
		transcriptPath := normalizeExtendedPath(entry.TranscriptPath)
		pathHash, hashErr := adapterjsonl.HashSourcePath(transcriptPath)
		if hashErr != nil {
			result.Issues = append(result.Issues, CorpusIssue{
				Code: "transcript_path_unresolvable", ArtifactID: fmt.Sprintf("index-line-%d", index+1),
			})
			continue
		}
		state, exists := rolloutReferences[pathHash]
		if !exists {
			state = &rolloutState{
				reference:    RolloutReference{SourcePathSHA256: pathHash, Status: "missing"},
				snapshotSeen: map[string]struct{}{}, blobSeen: map[string]struct{}{},
			}
			rolloutReferences[pathHash] = state
		}
		state.reference.IndexReferences++
		if strings.TrimSpace(entry.SessionID) != "" {
			state.reference.SessionIDs = appendUniqueStrings(state.reference.SessionIDs, entry.SessionID)
		}
	}
	result.Counts.UniqueSessions = len(sessions)
	result.Counts.RolloutReferences = len(rolloutReferences)

	plans := make([]snapshotPlan, 0, len(cardFiles)+1)
	indexPlan, indexChanged, err := planSnapshot(store, absoluteRoot, indexPath, indexData,
		RoleLegacyIndex, []string{"legacy-context-journal"}, options.Now())
	if err != nil {
		return result, err
	}
	if indexChanged {
		result.Issues = append(result.Issues, CorpusIssue{Code: "source_changed_during_freeze", ArtifactID: indexPlan.artifact.ArtifactID})
	}
	plans = append(plans, indexPlan)
	for _, cardPath := range cardFiles {
		if err := requireResolvedRegularFile(absoluteRoot, cardPath); err != nil {
			return result, fmt.Errorf("inspect legacy card: %w", err)
		}
		role := RoleLegacyCard
		if normalizePathKey(cardPath) == normalizePathKey(filepath.Join(absoluteRoot, "latest-summary-card.md")) {
			role = RoleLegacyLatest
		}
		data, err := os.ReadFile(cardPath)
		if err != nil {
			return result, fmt.Errorf("read legacy card: %w", err)
		}
		sessionIDs := mapKeys(cardSessions[normalizePathKey(cardPath)])
		plan, changed, err := planSnapshot(store, absoluteRoot, cardPath, data, role, sessionIDs, options.Now())
		if err != nil {
			return result, err
		}
		if changed {
			result.Issues = append(result.Issues, CorpusIssue{Code: "source_changed_during_freeze", ArtifactID: plan.artifact.ArtifactID})
		}
		plans = append(plans, plan)
		if role == RoleLegacyCard {
			result.Counts.Cards++
			result.Counts.CardBytes += plan.artifact.Blob.Bytes
			if _, exists := indexedCards[normalizePathKey(cardPath)]; exists {
				result.Counts.IndexedCards++
			} else {
				result.Counts.UnindexedCards++
				result.Issues = append(result.Issues, CorpusIssue{
					Code: "unindexed_card", ArtifactID: plan.artifact.ArtifactID,
				})
			}
		}
	}
	sort.Slice(plans, func(left, right int) bool {
		return plans[left].artifact.RelativePath < plans[right].artifact.RelativePath
	})

	planByEventID := map[string]snapshotPlan{}
	for _, plan := range plans {
		planByEventID[plan.event.EventID] = plan
	}
	scan := freezeScanState{
		existingEvents: map[string]ledger.Event{}, evaluationIDs: map[string]struct{}{},
		rollouts: rolloutReferences,
	}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		scan.lastRecordHash = record.RecordHash
		event := record.Event
		if _, wanted := planByEventID[event.EventID]; wanted {
			scan.existingEvents[event.EventID] = event
		}
		if event.Kind == ledger.KindEvaluationCorpus {
			scan.evaluationIDs[event.EventID] = struct{}{}
		}
		state, wanted := scan.rollouts[event.Source.SourcePathHash]
		if !wanted || event.Source.Agent != ledger.AgentCodex || event.Source.Adapter != "codex-jsonl" {
			return nil
		}
		if event.Kind == ledger.KindSourceSnapshot {
			if event.Payload == nil || event.Payload.Blob == nil || event.Source.ByteStart == nil || event.Source.ByteEnd == nil {
				state.partial = true
				return nil
			}
			if _, exists := state.snapshotSeen[event.EventID]; !exists {
				state.snapshotSeen[event.EventID] = struct{}{}
				state.reference.SnapshotEventIDs = append(state.reference.SnapshotEventIDs, event.EventID)
				state.reference.CoveredRanges = append(state.reference.CoveredRanges, ByteRange{
					Start: *event.Source.ByteStart, End: *event.Source.ByteEnd,
				})
			}
			blob := *event.Payload.Blob
			if _, exists := state.blobSeen[blob.SHA256]; !exists {
				state.blobSeen[blob.SHA256] = struct{}{}
				state.reference.SnapshotBlobs = append(state.reference.SnapshotBlobs, blob)
				result.Counts.RolloutSnapshotBytes += blob.Bytes
			}
			if event.Completeness.Status != ledger.CompletenessComplete {
				state.partial = true
			}
		} else if event.Kind == ledger.KindGap {
			state.reference.GapEvents++
			result.Counts.ProjectionGapEvents++
		} else {
			state.reference.ProjectedEvents++
			if event.Completeness.Status != ledger.CompletenessComplete {
				state.reference.IncompleteProjectedEvents++
				result.Counts.IncompleteProjectedEvents++
			}
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan evidence for corpus freeze: %w", err)
	}
	defer func() {
		if closeErr := appender.Close(); returnedErr == nil && closeErr != nil {
			returnedErr = closeErr
		}
	}()

	pending := []ledger.Event{}
	for _, plan := range plans {
		if existing, exists := scan.existingEvents[plan.event.EventID]; exists {
			if !sameSnapshot(existing, plan.event) {
				return result, fmt.Errorf("snapshot event id collision %q", plan.event.EventID)
			}
			result.SnapshotsReused++
			continue
		}
		pending = append(pending, plan.event)
	}
	if len(pending) > 0 {
		records, appendErr := appender.AppendBatch(pending)
		if appendErr != nil {
			return result, fmt.Errorf("append legacy snapshots: %w", appendErr)
		}
		result.SnapshotsAppended = len(records)
		if len(records) > 0 {
			scan.lastRecordHash = records[len(records)-1].RecordHash
		}
	}

	artifacts := make([]CorpusArtifact, 0, len(plans))
	for _, plan := range plans {
		artifacts = append(artifacts, plan.artifact)
	}
	rollouts := make([]RolloutReference, 0, len(rolloutReferences))
	for _, state := range rolloutReferences {
		sort.Strings(state.reference.SessionIDs)
		sort.Strings(state.reference.SnapshotEventIDs)
		sort.Slice(state.reference.SnapshotBlobs, func(left, right int) bool {
			return state.reference.SnapshotBlobs[left].SHA256 < state.reference.SnapshotBlobs[right].SHA256
		})
		state.reference.CoveredRanges = mergeRanges(state.reference.CoveredRanges)
		switch {
		case len(state.reference.SnapshotEventIDs) == 0:
			state.reference.Status = "missing"
			result.Counts.MissingRollouts++
			result.Issues = append(result.Issues, CorpusIssue{
				Code: "rollout_not_captured", SourcePathSHA256: state.reference.SourcePathSHA256,
			})
		case state.partial || !rangesBeginAtZeroAndContiguous(state.reference.CoveredRanges):
			state.reference.Status = "partial"
			result.Counts.PartialRollouts++
			result.Issues = append(result.Issues, CorpusIssue{
				Code: "rollout_capture_partial", SourcePathSHA256: state.reference.SourcePathSHA256,
			})
		default:
			state.reference.Status = "captured"
			result.Counts.CapturedRollouts++
		}
		rollouts = append(rollouts, state.reference)
	}
	sort.Slice(rollouts, func(left, right int) bool {
		return rollouts[left].SourcePathSHA256 < rollouts[right].SourcePathSHA256
	})
	result.Issues = aggregateCorpusIssues(result.Issues)
	contentHash, err := corpusContentHash(sourceRootHash, artifacts, rollouts)
	if err != nil {
		return result, err
	}
	corpusID := "corpus-" + contentHash
	manifestPath := corpusManifestPath(store.Root(), corpusID)
	if existing, loadErr := loadCorpusManifestPath(manifestPath); loadErr == nil {
		if existing.CorpusContentSHA256 != contentHash {
			return result, errors.New("existing corpus manifest content hash mismatch")
		}
		verification := VerifyCorpus(store, corpusID)
		if len(verification.Issues) != 0 {
			return result, errors.New("existing corpus manifest failed verification")
		}
		manifestBytes, _ := os.ReadFile(manifestPath)
		manifestDigest := sha256.Sum256(manifestBytes)
		result.CorpusID = corpusID
		result.ManifestPath = manifestPath
		result.ManifestSHA256 = hex.EncodeToString(manifestDigest[:])
		result.FreezeEventID = freezeEventID(corpusID, result.ManifestSHA256)
		result.Counts = existing.Counts
		result.Issues = existing.Issues
		result.Reused = true
		if err := appender.Close(); err != nil {
			return result, err
		}
		return result, nil
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return result, loadErr
	}

	manifest := CorpusManifest{
		SchemaVersion: CorpusManifestSchemaVersion, CorpusID: corpusID,
		CorpusContentSHA256: contentHash, Name: options.Name, CreatedAt: options.Now().UTC(),
		SourceRootSHA256: sourceRootHash, SourceLedgerLastRecordHash: scan.lastRecordHash,
		Artifacts: artifacts, Rollouts: rollouts, Counts: result.Counts,
		Issues: result.Issues, Privacy: "local_only",
	}
	manifestBytes, err := marshalIndented(manifest)
	if err != nil {
		return result, err
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(manifestDigest[:])
	manifestBlob, err := store.PutBlob(bytes.NewReader(manifestBytes))
	if err != nil {
		return result, fmt.Errorf("preserve corpus manifest: %w", err)
	}
	if manifestBlob.SHA256 != manifestSHA {
		return result, errors.New("corpus manifest blob hash mismatch")
	}
	if err := writeCorpusManifest(store.Root(), corpusID, manifestBytes); err != nil {
		return result, err
	}
	freezeID := freezeEventID(corpusID, manifestSHA)
	if _, exists := scan.evaluationIDs[freezeID]; !exists {
		event := ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: freezeID, Kind: ledger.KindEvaluationCorpus,
			ObservedAt: options.Now().UTC(), RecordedAt: options.Now().UTC(),
			Source: ledger.Source{
				Agent: ledger.AgentCodex, Adapter: legacyAdapterName,
				AdapterVersion: legacyAdapterVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
				ThreadID: "legacy-context-journal", SourcePathHash: sourceRootHash,
				SourceCursor: "corpus:" + corpusID,
			},
			Payload: &ledger.Payload{Encoding: "json", MediaType: "application/json", Blob: &manifestBlob,
				SHA256: manifestBlob.SHA256, Bytes: manifestBlob.Bytes},
			Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Privacy:      ledger.Privacy{Classification: "local_only"},
		}
		if _, err := appender.AppendBatch([]ledger.Event{event}); err != nil {
			return result, fmt.Errorf("append corpus freeze event: %w", err)
		}
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	result.CorpusID = corpusID
	result.ManifestPath = manifestPath
	result.ManifestSHA256 = manifestSHA
	result.FreezeEventID = freezeID
	return result, nil
}

func VerifyCorpus(store *ledger.Store, corpusID string) CorpusVerificationReport {
	report := CorpusVerificationReport{
		SchemaVersion: CorpusVerifySchemaVersion, CorpusID: corpusID,
		Issues: []string{}, Privacy: "local_only",
	}
	if store == nil {
		report.Issues = append(report.Issues, "store is required")
		return report
	}
	if !validCorpusID(corpusID) {
		report.Issues = append(report.Issues, "invalid corpus_id")
		return report
	}
	path := corpusManifestPath(store.Root(), corpusID)
	manifestBytes, err := os.ReadFile(path)
	if err != nil {
		report.Issues = append(report.Issues, "read manifest: "+errorClass(err))
		return report
	}
	manifest, err := decodeCorpusManifest(manifestBytes)
	if err != nil {
		report.Issues = append(report.Issues, err.Error())
		return report
	}
	if err := validateCorpusManifest(manifest); err != nil {
		report.Issues = append(report.Issues, err.Error())
	}
	if manifest.CorpusID != corpusID {
		report.Issues = append(report.Issues, "manifest corpus_id does not match path")
	}
	contentHash, hashErr := corpusContentHash(manifest.SourceRootSHA256, manifest.Artifacts, manifest.Rollouts)
	if hashErr != nil || contentHash != manifest.CorpusContentSHA256 || corpusID != "corpus-"+contentHash {
		report.Issues = append(report.Issues, "manifest corpus content hash mismatch")
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	manifestSHA := hex.EncodeToString(manifestDigest[:])
	wantedEvents := map[string]struct{}{}
	for _, artifact := range manifest.Artifacts {
		wantedEvents[artifact.SnapshotEventID] = struct{}{}
	}
	for _, rollout := range manifest.Rollouts {
		for _, eventID := range rollout.SnapshotEventIDs {
			wantedEvents[eventID] = struct{}{}
		}
	}
	wantedEvents[freezeEventID(corpusID, manifestSHA)] = struct{}{}
	found := map[string]ledger.Record{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, wanted := wantedEvents[record.Event.EventID]; wanted {
			found[record.Event.EventID] = record
		}
		if record.RecordHash == manifest.SourceLedgerLastRecordHash {
			found[manifest.SourceLedgerLastRecordHash] = record
		}
		return nil
	}); err != nil {
		report.Issues = append(report.Issues, "visit evidence ledger: "+err.Error())
		return report
	}
	for _, artifact := range manifest.Artifacts {
		report.ArtifactsChecked++
		record, exists := found[artifact.SnapshotEventID]
		if !exists || record.Event.Kind != ledger.KindSourceSnapshot ||
			!eventMatchesBlob(record.Event, artifact.Blob) ||
			record.Event.Source.SourcePathHash != artifact.SourcePathSHA256 {
			report.Issues = append(report.Issues, "artifact snapshot mismatch: "+artifact.ArtifactID)
			continue
		}
		report.SnapshotEventsChecked++
		if err := verifyBlob(store, artifact.Blob); err != nil {
			report.Issues = append(report.Issues, "artifact blob invalid: "+artifact.ArtifactID)
		}
	}
	for _, rollout := range manifest.Rollouts {
		report.RolloutsChecked++
		blobBySHA := map[string]ledger.BlobRef{}
		for _, blob := range rollout.SnapshotBlobs {
			blobBySHA[blob.SHA256] = blob
		}
		for _, eventID := range rollout.SnapshotEventIDs {
			record, exists := found[eventID]
			if !exists || record.Event.Kind != ledger.KindSourceSnapshot ||
				record.Event.Source.SourcePathHash != rollout.SourcePathSHA256 ||
				record.Event.Payload == nil || record.Event.Payload.Blob == nil {
				report.Issues = append(report.Issues, "rollout snapshot mismatch: "+eventID)
				continue
			}
			blob, listed := blobBySHA[record.Event.Payload.Blob.SHA256]
			if !listed || !eventMatchesBlob(record.Event, blob) {
				report.Issues = append(report.Issues, "rollout blob not bound by manifest: "+eventID)
				continue
			}
			report.SnapshotEventsChecked++
		}
		for _, blob := range rollout.SnapshotBlobs {
			if err := verifyBlob(store, blob); err != nil {
				report.Issues = append(report.Issues, "rollout blob invalid: "+blob.SHA256)
			}
		}
	}
	freezeID := freezeEventID(corpusID, manifestSHA)
	manifestBlob := ledger.BlobRef{SHA256: manifestSHA, Bytes: int64(len(manifestBytes)),
		RelativePath: expectedBlobPath(manifestSHA)}
	if record, exists := found[freezeID]; !exists || record.Event.Kind != ledger.KindEvaluationCorpus ||
		!eventMatchesBlob(record.Event, manifestBlob) {
		report.Issues = append(report.Issues, "corpus freeze event is missing or invalid")
	} else if err := verifyBlob(store, manifestBlob); err != nil {
		report.Issues = append(report.Issues, "corpus manifest blob is invalid")
	}
	if manifest.SourceLedgerLastRecordHash != "" {
		if _, exists := found[manifest.SourceLedgerLastRecordHash]; !exists {
			report.Issues = append(report.Issues, "source ledger anchor is missing")
		}
	}
	report.Issues = uniqueSorted(report.Issues)
	return report
}

func LoadCorpusManifest(store *ledger.Store, corpusID string) (CorpusManifest, error) {
	if store == nil {
		return CorpusManifest{}, errors.New("store is required")
	}
	if !validCorpusID(corpusID) {
		return CorpusManifest{}, errors.New("invalid corpus_id")
	}
	return loadCorpusManifestPath(corpusManifestPath(store.Root(), corpusID))
}

func planSnapshot(store *ledger.Store, root, path string, data []byte, role ArtifactRole,
	sessionIDs []string, now time.Time) (snapshotPlan, bool, error) {
	before, err := os.Stat(path)
	if err != nil {
		return snapshotPlan{}, false, err
	}
	blob, err := store.PutBlob(bytes.NewReader(data))
	if err != nil {
		return snapshotPlan{}, false, fmt.Errorf("preserve legacy artifact: %w", err)
	}
	after, statErr := os.Stat(path)
	changed := statErr != nil || before.Size() != int64(len(data)) ||
		(after != nil && (after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime())))
	pathHash, err := adapterjsonl.HashSourcePath(path)
	if err != nil {
		return snapshotPlan{}, false, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return snapshotPlan{}, false, errors.New("legacy artifact escaped source root")
	}
	relative = filepath.ToSlash(relative)
	eventID := adapterjsonl.DeterministicID("legacy-context-journal-snapshot", pathHash, blob.SHA256,
		fmt.Sprint(blob.Bytes))
	artifactID := adapterjsonl.DeterministicID("legacy-artifact", string(role), relative, blob.SHA256)
	threadID := "legacy-context-journal"
	if len(sessionIDs) == 1 && strings.TrimSpace(sessionIDs[0]) != "" {
		threadID = sessionIDs[0]
	}
	start, end := int64(0), blob.Bytes
	completeness := ledger.Completeness{Status: ledger.CompletenessComplete}
	if changed {
		expected, captured := before.Size(), blob.Bytes
		completeness = ledger.Completeness{
			Status:        ledger.CompletenessPartial,
			Reason:        "source_changed_during_snapshot: captured bytes retained; reconciliation required",
			ExpectedBytes: &expected, CapturedBytes: &captured,
		}
	}
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindSourceSnapshot,
		ObservedAt: before.ModTime().UTC(), RecordedAt: now.UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: legacyAdapterName, AdapterVersion: legacyAdapterVersion,
			DeviceID: store.DeviceID(), OS: runtime.GOOS, ThreadID: threadID,
			SourcePathHash: pathHash, SourceCursor: "file:" + relative, ByteStart: &start, ByteEnd: &end,
		},
		Payload: &ledger.Payload{Encoding: "binary", MediaType: mediaTypeFor(role), Blob: &blob,
			SHA256: blob.SHA256, Bytes: blob.Bytes},
		Completeness: completeness, Privacy: ledger.Privacy{Classification: "local_only"},
	}
	return snapshotPlan{artifact: CorpusArtifact{
		ArtifactID: artifactID, Role: role, RelativePath: relative,
		SourcePathSHA256: pathHash, SnapshotEventID: eventID, Blob: blob,
	}, event: event}, changed, nil
}

func parseLegacyIndex(data []byte) ([]legacyIndexEntry, []CorpusIssue, error) {
	entries := []legacyIndexEntry{}
	issues := []CorpusIssue{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	line := 0
	for scanner.Scan() {
		line++
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		var entry legacyIndexEntry
		if err := json.Unmarshal(trimmed, &entry); err != nil {
			issues = append(issues, CorpusIssue{Code: "invalid_index_record", ArtifactID: fmt.Sprintf("index-line-%d", line)})
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan legacy index: %w", err)
	}
	return entries, issues, nil
}

func collectLegacyCards(root string) ([]string, error) {
	files := []string{}
	summaries := filepath.Join(root, "summaries")
	if err := filepath.WalkDir(summaries, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk legacy cards: %w", err)
	}
	latest := filepath.Join(root, "latest-summary-card.md")
	if info, err := os.Stat(latest); err == nil && info.Mode().IsRegular() {
		files = append(files, latest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect latest legacy card: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func trustedLegacyCardPath(root, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("empty card path")
	}
	path := normalizeExtendedPath(value)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return "", errors.New("card path escapes legacy root")
	}
	relative = filepath.ToSlash(relative)
	if !strings.HasPrefix(strings.ToLower(relative), "summaries/") ||
		!strings.EqualFold(filepath.Ext(relative), ".md") {
		return "", errors.New("card path is outside summaries")
	}
	if _, err := os.Lstat(absolute); err == nil {
		if err := requireResolvedRegularFile(root, absolute); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return absolute, nil
}

func requireResolvedRegularFile(root, path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("resolved path escapes legacy root")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("legacy artifact is not a regular file")
	}
	return nil
}

func sameSnapshot(left, right ledger.Event) bool {
	return left.Kind == ledger.KindSourceSnapshot && left.Source.SourcePathHash == right.Source.SourcePathHash &&
		left.Payload != nil && right.Payload != nil && left.Payload.Blob != nil && right.Payload.Blob != nil &&
		*left.Payload.Blob == *right.Payload.Blob
}

func corpusContentHash(sourceRootHash string, artifacts []CorpusArtifact, rollouts []RolloutReference) (string, error) {
	content := struct {
		SchemaVersion    string             `json:"schema_version"`
		SourceRootSHA256 string             `json:"source_root_sha256"`
		Artifacts        []CorpusArtifact   `json:"artifacts"`
		Rollouts         []RolloutReference `json:"rollouts"`
	}{CorpusManifestSchemaVersion, sourceRootHash, artifacts, rollouts}
	data, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("encode corpus content: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validateCorpusManifest(manifest CorpusManifest) error {
	if manifest.SchemaVersion != CorpusManifestSchemaVersion || !validCorpusID(manifest.CorpusID) ||
		!validSHA256(manifest.CorpusContentSHA256) || !validSHA256(manifest.SourceRootSHA256) ||
		manifest.CreatedAt.IsZero() || manifest.Privacy != "local_only" || !safeIdentifier(manifest.Name) {
		return errors.New("corpus manifest header is invalid")
	}
	if manifest.SourceLedgerLastRecordHash != "" && !validSHA256(manifest.SourceLedgerLastRecordHash) {
		return errors.New("corpus manifest ledger anchor is invalid")
	}
	previous := ""
	seenArtifacts := map[string]struct{}{}
	for _, artifact := range manifest.Artifacts {
		if artifact.RelativePath <= previous || !safeRelativePath(artifact.RelativePath) ||
			!validSHA256(artifact.SourcePathSHA256) || !validBlobRef(artifact.Blob) ||
			strings.TrimSpace(artifact.SnapshotEventID) == "" || strings.TrimSpace(artifact.ArtifactID) == "" {
			return errors.New("corpus artifact ordering or content is invalid")
		}
		if _, exists := seenArtifacts[artifact.ArtifactID]; exists {
			return errors.New("corpus artifact ids are not unique")
		}
		seenArtifacts[artifact.ArtifactID] = struct{}{}
		previous = artifact.RelativePath
	}
	previous = ""
	for _, rollout := range manifest.Rollouts {
		if rollout.SourcePathSHA256 <= previous || !validSHA256(rollout.SourcePathSHA256) ||
			(rollout.Status != "captured" && rollout.Status != "partial" && rollout.Status != "missing") {
			return errors.New("rollout ordering or status is invalid")
		}
		if rollout.Status == "missing" && len(rollout.SnapshotEventIDs) != 0 {
			return errors.New("missing rollout unexpectedly has snapshots")
		}
		previous = rollout.SourcePathSHA256
	}
	return nil
}

func loadCorpusManifestPath(path string) (CorpusManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CorpusManifest{}, err
	}
	return decodeCorpusManifest(data)
}

func decodeCorpusManifest(data []byte) (CorpusManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest CorpusManifest
	if err := decoder.Decode(&manifest); err != nil {
		return CorpusManifest{}, fmt.Errorf("decode corpus manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return CorpusManifest{}, err
	}
	return manifest, nil
}

func writeCorpusManifest(root, corpusID string, data []byte) error {
	base := filepath.Join(root, "derived", "evaluations", "corpora")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create corpus directory: %w", err)
	}
	destination := filepath.Join(base, corpusID)
	if _, err := os.Stat(destination); err == nil {
		return errors.New("corpus destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.MkdirTemp(base, ".corpus-tmp-")
	if err != nil {
		return fmt.Errorf("create corpus temp directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	path := filepath.Join(temporary, "manifest.json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create corpus manifest: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write corpus manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync corpus manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close corpus manifest: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("commit corpus manifest: %w", err)
	}
	return nil
}

func corpusManifestPath(root, corpusID string) string {
	return filepath.Join(root, "derived", "evaluations", "corpora", corpusID, "manifest.json")
}

func freezeEventID(corpusID, manifestSHA string) string {
	return adapterjsonl.DeterministicID("evaluation-corpus", corpusID, manifestSHA)
}

func eventMatchesBlob(event ledger.Event, blob ledger.BlobRef) bool {
	return event.Payload != nil && event.Payload.Blob != nil && *event.Payload.Blob == blob &&
		event.Payload.SHA256 == blob.SHA256 && event.Payload.Bytes == blob.Bytes
}

func verifyBlob(store *ledger.Store, blob ledger.BlobRef) error {
	if !validBlobRef(blob) {
		return errors.New("invalid blob reference")
	}
	file, err := store.OpenBlob(blob)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return err
	}
	if size != blob.Bytes || hex.EncodeToString(hasher.Sum(nil)) != blob.SHA256 {
		return errors.New("blob content mismatch")
	}
	return nil
}

func mergeRanges(ranges []ByteRange) []ByteRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(left, right int) bool {
		if ranges[left].Start != ranges[right].Start {
			return ranges[left].Start < ranges[right].Start
		}
		return ranges[left].End < ranges[right].End
	})
	merged := []ByteRange{}
	for _, current := range ranges {
		if current.Start < 0 || current.End < current.Start {
			continue
		}
		if len(merged) == 0 || current.Start > merged[len(merged)-1].End {
			merged = append(merged, current)
			continue
		}
		if current.End > merged[len(merged)-1].End {
			merged[len(merged)-1].End = current.End
		}
	}
	return merged
}

func rangesBeginAtZeroAndContiguous(ranges []ByteRange) bool {
	return len(ranges) == 1 && ranges[0].Start == 0 && ranges[0].End > 0
}

func aggregateCorpusIssues(issues []CorpusIssue) []CorpusIssue {
	type key struct{ code, artifact, path string }
	counts := map[key]int{}
	for _, issue := range issues {
		count := issue.Count
		if count <= 0 {
			count = 1
		}
		counts[key{issue.Code, issue.ArtifactID, issue.SourcePathSHA256}] += count
	}
	result := make([]CorpusIssue, 0, len(counts))
	for item, count := range counts {
		result = append(result, CorpusIssue{
			Code: item.code, ArtifactID: item.artifact, SourcePathSHA256: item.path, Count: count,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Code != result[right].Code {
			return result[left].Code < result[right].Code
		}
		if result[left].ArtifactID != result[right].ArtifactID {
			return result[left].ArtifactID < result[right].ArtifactID
		}
		return result[left].SourcePathSHA256 < result[right].SourcePathSHA256
	})
	return result
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds safety limit")
	}
	return data, nil
}

func marshalIndented(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validCorpusID(value string) bool {
	return strings.HasPrefix(value, "corpus-") && validSHA256(strings.TrimPrefix(value, "corpus-"))
}

func validBlobRef(blob ledger.BlobRef) bool {
	return validSHA256(blob.SHA256) && blob.Bytes >= 0 && blob.RelativePath == expectedBlobPath(blob.SHA256)
}

func expectedBlobPath(digest string) string {
	if !validSHA256(digest) {
		return ""
	}
	return filepath.ToSlash(filepath.Join("evidence", "blobs", "sha256", digest[:2], digest[2:]))
}

func mediaTypeFor(role ArtifactRole) string {
	if role == RoleLegacyIndex {
		return "application/x-ndjson"
	}
	return "text/markdown; charset=utf-8"
}

func normalizeExtendedPath(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), `\\?\unc\`) {
		return `\\` + value[len(`\\?\UNC\`):]
	}
	if strings.HasPrefix(value, `\\?\`) {
		return value[len(`\\?\`):]
	}
	return value
}

func normalizePathKey(value string) string {
	cleaned := filepath.Clean(normalizeExtendedPath(value))
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(cleaned)
	}
	return cleaned
}

func pathsOverlap(left, right string) bool {
	leftKey, rightKey := normalizePathKey(left), normalizePathKey(right)
	separator := string(filepath.Separator)
	return leftKey == rightKey || strings.HasPrefix(leftKey, rightKey+separator) ||
		strings.HasPrefix(rightKey, leftKey+separator)
}

func safeRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return cleaned == value && cleaned != "." && !strings.HasPrefix(cleaned, "../")
}

func appendUniqueStrings(values []string, added ...string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			seen[value] = struct{}{}
		}
	}
	for _, value := range added {
		if strings.TrimSpace(value) != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func mapKeys(values map[string]struct{}) []string {
	result := []string{}
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func errorClass(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		digest := sha256.Sum256([]byte(err.Error()))
		return "other-" + hex.EncodeToString(digest[:8])
	}
}
