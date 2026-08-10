package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	CompactionGroundTruthPackSchema        = "compaction-ground-truth-pack/v1alpha1"
	CompactionGroundTruthSealSchema        = "compaction-ground-truth-seal/v1alpha1"
	CompactionGroundTruthSealRequestSchema = "compaction-ground-truth-seal-request/v1alpha1"
)

type CompactionGroundTruthLabel struct {
	EventID  string     `json:"event_id"`
	Expected DriftLabel `json:"expected"`
}

type CompactionGroundTruthSealRequest struct {
	SchemaVersion string                       `json:"schema_version"`
	CorpusID      string                       `json:"corpus_id"`
	ReviewerID    string                       `json:"reviewer_id"`
	Reason        string                       `json:"reason"`
	Labels        []CompactionGroundTruthLabel `json:"labels"`
	Privacy       string                       `json:"privacy"`
}

type CompactionGroundTruthSubject struct {
	EventID    string       `json:"event_id"`
	RecordHash string       `json:"record_hash"`
	Agent      ledger.Agent `json:"agent"`
	Expected   DriftLabel   `json:"expected"`
}

type CompactionGroundTruthPack struct {
	SchemaVersion       string                         `json:"schema_version"`
	PackID              string                         `json:"pack_id"`
	PackSHA256          string                         `json:"pack_sha256"`
	CorpusID            string                         `json:"corpus_id"`
	CorpusContentSHA256 string                         `json:"corpus_content_sha256"`
	ReviewerID          string                         `json:"reviewer_id"`
	Reason              string                         `json:"reason"`
	SealedAt            time.Time                      `json:"sealed_at"`
	Subjects            []CompactionGroundTruthSubject `json:"subjects"`
	Privacy             string                         `json:"privacy"`
}

type SealCompactionGroundTruthOptions struct {
	CorpusID   string
	ReviewerID string
	Reason     string
	Labels     []CompactionGroundTruthLabel
	Now        func() time.Time
}

type CompactionGroundTruthSealResult struct {
	SchemaVersion string                    `json:"schema_version"`
	Pack          CompactionGroundTruthPack `json:"pack"`
	EventID       string                    `json:"event_id"`
	RecordHash    string                    `json:"record_hash"`
	Privacy       string                    `json:"privacy"`
}

func DecodeCompactionGroundTruthSealRequest(reader io.Reader) (CompactionGroundTruthSealRequest, error) {
	if reader == nil {
		return CompactionGroundTruthSealRequest{}, errors.New("compaction ground-truth seal request reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request CompactionGroundTruthSealRequest
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("decode compaction ground-truth seal request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return request, err
	}
	if request.SchemaVersion != CompactionGroundTruthSealRequestSchema ||
		!validCorpusID(request.CorpusID) || !safeIdentifier(request.ReviewerID) ||
		strings.TrimSpace(request.Reason) == "" || len(request.Labels) == 0 ||
		request.Privacy != "local_only" {
		return request, errors.New("compaction ground-truth seal request is invalid")
	}
	for _, label := range request.Labels {
		if strings.TrimSpace(label.EventID) == "" ||
			(label.Expected != DriftDetected && label.Expected != DriftPreserved) {
			return request, errors.New("compaction ground-truth seal request label is invalid")
		}
	}
	return request, nil
}

func SealCompactionGroundTruth(store *ledger.Store, options SealCompactionGroundTruthOptions) (
	CompactionGroundTruthSealResult, error,
) {
	result := CompactionGroundTruthSealResult{SchemaVersion: CompactionGroundTruthSealSchema,
		Privacy: "local_only"}
	if store == nil || !validCorpusID(options.CorpusID) || !safeIdentifier(options.ReviewerID) ||
		strings.TrimSpace(options.Reason) == "" || len(options.Labels) == 0 {
		return result, errors.New("store, corpus, human reviewer, reason, and labels are required")
	}
	verification := VerifyCorpus(store, options.CorpusID)
	if len(verification.Issues) != 0 {
		return result, errors.New("frozen corpus verification failed")
	}
	manifest, err := LoadCorpusManifest(store, options.CorpusID)
	if err != nil {
		return result, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	labels := append([]CompactionGroundTruthLabel{}, options.Labels...)
	sort.Slice(labels, func(i, j int) bool { return labels[i].EventID < labels[j].EventID })
	labelByEvent := map[string]DriftLabel{}
	for index, label := range labels {
		if strings.TrimSpace(label.EventID) == "" ||
			(label.Expected != DriftDetected && label.Expected != DriftPreserved) ||
			(index > 0 && labels[index-1].EventID >= label.EventID) {
			return result, errors.New("compaction ground-truth labels must be sorted unique binary expectations")
		}
		labelByEvent[label.EventID] = label.Expected
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return result, err
	}
	frozenSubjects, err := frozenCorpusCompactionRecords(manifest, records, ordered)
	if err != nil {
		return result, err
	}
	subjects := []CompactionGroundTruthSubject{}
	for _, indexed := range frozenSubjects {
		record := indexed.Record
		expected, exists := labelByEvent[record.Event.EventID]
		if !exists {
			return result, fmt.Errorf("compaction ground-truth label is missing: %s", record.Event.EventID)
		}
		delete(labelByEvent, record.Event.EventID)
		subjects = append(subjects, CompactionGroundTruthSubject{EventID: record.Event.EventID,
			RecordHash: record.RecordHash, Agent: record.Event.Source.Agent, Expected: expected})
	}
	if len(labelByEvent) != 0 || len(subjects) == 0 {
		return result, errors.New("compaction ground-truth labels include unavailable subjects")
	}
	attempts, err := episodes.ListVerifiedGenerationAttempts(store)
	if err != nil {
		return result, err
	}
	for _, attempted := range attempts {
		for _, indexed := range frozenSubjects {
			if indexed.Index <= attempted.Attempt.SourceRecords {
				return result, errors.New("compaction ground truth cannot be sealed after detector generation was attempted over the frozen subjects")
			}
		}
	}
	audits, err := episodes.ListVerifiedGenerationAudits(store)
	if err != nil {
		return result, err
	}
	for _, audited := range audits {
		for _, indexed := range frozenSubjects {
			if indexed.Index <= audited.Audit.SourceRecords {
				return result, errors.New("compaction ground truth cannot be sealed after detector output already covered the frozen subjects")
			}
		}
	}
	sort.Slice(subjects, func(i, j int) bool {
		return subjects[i].EventID < subjects[j].EventID
	})
	pack := CompactionGroundTruthPack{SchemaVersion: CompactionGroundTruthPackSchema,
		CorpusID: options.CorpusID, CorpusContentSHA256: manifest.CorpusContentSHA256,
		ReviewerID: options.ReviewerID, Reason: strings.TrimSpace(options.Reason),
		SealedAt: options.Now().UTC(), Subjects: subjects, Privacy: "local_only"}
	pack.PackSHA256 = compactionGroundTruthPackSHA(pack)
	pack.PackID = adapterjsonl.DeterministicID("compaction-ground-truth", pack.CorpusID, pack.PackSHA256)
	if err := validateCompactionGroundTruthPack(pack); err != nil {
		return result, err
	}
	data, err := json.Marshal(pack)
	if err != nil {
		return result, err
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(data))
	parents := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		parents = append(parents, subject.EventID)
	}
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: pack.PackID,
		Kind: ledger.KindCompactionGroundTruth, ObservedAt: pack.SealedAt, RecordedAt: pack.SealedAt,
		Source: ledger.Source{Agent: ledger.AgentUnknown, Adapter: "human-compaction-review",
			AdapterVersion: CompactionGroundTruthPackSchema, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: pack.PackID, SessionID: pack.PackID, SourceEventID: pack.PackID,
			SourceCursor: "compaction-ground-truth:" + pack.PackID},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: parents},
		Privacy:   ledger.Privacy{Classification: "local_only"}}
	record, err := store.Append(event)
	if err != nil {
		return result, err
	}
	result.Pack, result.EventID, result.RecordHash = pack, pack.PackID, record.RecordHash
	return result, nil
}

func compactionGroundTruthPackSHA(pack CompactionGroundTruthPack) string {
	copy := pack
	copy.PackID, copy.PackSHA256 = "", ""
	data, _ := json.Marshal(copy)
	return sha256Hex(data)
}

func validateCompactionGroundTruthPack(pack CompactionGroundTruthPack) error {
	if pack.SchemaVersion != CompactionGroundTruthPackSchema || !safeIdentifier(pack.PackID) ||
		!validSHA256(pack.PackSHA256) || !validCorpusID(pack.CorpusID) ||
		!validSHA256(pack.CorpusContentSHA256) || !safeIdentifier(pack.ReviewerID) ||
		strings.TrimSpace(pack.Reason) == "" || pack.SealedAt.IsZero() || len(pack.Subjects) == 0 ||
		pack.Privacy != "local_only" || compactionGroundTruthPackSHA(pack) != pack.PackSHA256 ||
		adapterjsonl.DeterministicID("compaction-ground-truth", pack.CorpusID, pack.PackSHA256) != pack.PackID {
		return errors.New("compaction ground-truth pack envelope is invalid")
	}
	for index, subject := range pack.Subjects {
		if strings.TrimSpace(subject.EventID) == "" || !validSHA256(subject.RecordHash) ||
			!validAgent(subject.Agent) || subject.Agent == ledger.AgentUnknown ||
			(subject.Expected != DriftDetected && subject.Expected != DriftPreserved) ||
			(index > 0 && pack.Subjects[index-1].EventID >= subject.EventID) {
			return errors.New("compaction ground-truth subjects are invalid")
		}
	}
	return nil
}

func frozenCorpusCompactionRecords(manifest CorpusManifest, records map[string]indexedRecord,
	ordered []ledger.Record) ([]indexedRecord, error) {
	if manifest.SourceLedgerLastRecordHash == "" {
		return nil, errors.New("frozen corpus has no source-ledger boundary")
	}
	boundary := -1
	for index, record := range ordered {
		if record.RecordHash == manifest.SourceLedgerLastRecordHash {
			boundary = index
			break
		}
	}
	if boundary < 0 {
		return nil, errors.New("frozen corpus source-ledger boundary is unavailable")
	}
	rollouts := map[string]RolloutReference{}
	for _, rollout := range manifest.Rollouts {
		if rollout.Status == "captured" {
			rollouts[rollout.SourcePathSHA256] = rollout
		}
	}
	result := []indexedRecord{}
	for index := 0; index <= boundary; index++ {
		record := ordered[index]
		if record.Event.Kind != ledger.KindCompaction {
			continue
		}
		rollout, exists := rollouts[record.Event.Source.SourcePathHash]
		if !exists || record.Event.Source.ByteStart == nil || record.Event.Source.ByteEnd == nil ||
			!rangeCovered(*record.Event.Source.ByteStart, *record.Event.Source.ByteEnd, rollout.CoveredRanges) {
			continue
		}
		indexed, exists := records[record.Event.EventID]
		if !exists || indexed.Index != index+1 {
			return nil, errors.New("frozen compaction subject index is inconsistent")
		}
		result = append(result, indexed)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Record.Event.EventID < result[j].Record.Event.EventID
	})
	return result, nil
}

func rangeCovered(start, end int64, ranges []ByteRange) bool {
	if start < 0 || end <= start {
		return false
	}
	for _, covered := range ranges {
		if start >= covered.Start && end <= covered.End {
			return true
		}
	}
	return false
}

func loadSealedCompactionGroundTruth(store *ledger.Store, records map[string]indexedRecord,
	ordered []ledger.Record, corpusID, corpusContentSHA string) (
	map[string]CompactionGroundTruthSubject, *indexedRecord, []string,
) {
	subjects := map[string]CompactionGroundTruthSubject{}
	issues := []string{}
	var selected *indexedRecord
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindCompactionGroundTruth {
			continue
		}
		data, err := eventPayload(store, record.Event)
		var pack CompactionGroundTruthPack
		if err != nil || decodeStrictEvaluationJSON(data, &pack) != nil ||
			validateCompactionGroundTruthPack(pack) != nil || pack.PackID != record.Event.EventID ||
			pack.CorpusID != corpusID || pack.CorpusContentSHA256 != corpusContentSHA {
			continue
		}
		if selected != nil {
			issues = append(issues, "compaction ground truth is ambiguous for the frozen corpus")
			continue
		}
		indexed := records[record.Event.EventID]
		selected = &indexed
		manifest, loadErr := LoadCorpusManifest(store, corpusID)
		expectedSubjects, universeErr := frozenCorpusCompactionRecords(manifest, records, ordered)
		if loadErr != nil || universeErr != nil {
			issues = append(issues, "frozen compaction ground-truth universe is unavailable")
			continue
		}
		expectedByID := map[string]indexedRecord{}
		for _, expected := range expectedSubjects {
			expectedByID[expected.Record.Event.EventID] = expected
		}
		parents := []string{}
		for _, subject := range pack.Subjects {
			observed, exists := records[subject.EventID]
			if !exists || observed.Index >= indexed.Index || observed.Record.RecordHash != subject.RecordHash ||
				observed.Record.Event.Kind != ledger.KindCompaction || observed.Record.Event.Source.Agent != subject.Agent {
				issues = append(issues, "compaction ground truth subject evidence is invalid: "+subject.EventID)
				continue
			}
			expected, inUniverse := expectedByID[subject.EventID]
			if !inUniverse || expected.Record.RecordHash != subject.RecordHash {
				issues = append(issues, "compaction ground truth includes a subject outside the frozen corpus: "+subject.EventID)
				continue
			}
			delete(expectedByID, subject.EventID)
			subjects[subject.EventID] = subject
			parents = append(parents, subject.EventID)
		}
		if record.Event.Causality == nil || !sameStrings(record.Event.Causality.ParentEventIDs, parents) {
			issues = append(issues, "compaction ground truth causality is invalid")
		}
		for missing := range expectedByID {
			issues = append(issues, "compaction ground truth omits a frozen corpus subject: "+missing)
		}
	}
	if selected == nil {
		issues = append(issues, "sealed compaction ground truth is unavailable")
	}
	return subjects, selected, uniqueSorted(issues)
}
