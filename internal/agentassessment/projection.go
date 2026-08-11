package agentassessment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	maximumEvidenceTextBytes        = 64 << 10
	maximumProjectionBytes          = 96 << 20
	maximumBlindPayloadBytes        = 64 << 20
	maximumProjectionEvidenceBlocks = 20000
)

type evidenceRecord struct {
	record   ledger.Record
	sequence int
}

type extractedEvidence struct {
	block EvidenceBlock
	issue string
}

// PrepareProjection creates an immutable local binding manifest and a separate
// minimal blind payload. It does not call a model, append to the ledger, or
// confer review authority.
func PrepareProjection(store *ledger.Store, queueID string) (ProjectionResult, error) {
	result := ProjectionResult{SchemaVersion: ProjectionResultSchema, Privacy: "local_only"}
	projection, data, payload, payloadBytes, err := buildProjection(store, queueID)
	if err != nil {
		return result, err
	}
	payloadPath, payloadReused, err := writeImmutableArtifact(store, "agent-payloads", payload.PayloadID,
		"payload.json", payloadBytes, maximumBlindPayloadBytes)
	if err != nil {
		return result, err
	}
	path, reused, err := writeImmutableArtifact(store, "agent-projections", projection.ProjectionID,
		"projection.json", data, maximumProjectionBytes)
	if err != nil {
		return result, err
	}
	units := 0
	for _, item := range projection.CompactionItems {
		units += len(item.Units)
	}
	result.ProjectionID = projection.ProjectionID
	result.ProjectionPath = path
	result.PayloadID = payload.PayloadID
	result.PayloadPath = payloadPath
	result.QueueID = projection.QueueID
	result.CandidateItems = len(projection.CandidateItems)
	result.CompactionItems = len(projection.CompactionItems)
	result.CompactionUnits = units
	result.EvidenceBlocks = len(projection.EvidenceBlocks)
	result.Reused = reused && payloadReused
	return result, nil
}

func buildProjection(store *ledger.Store, queueID string) (Projection, []byte, BlindPayload, []byte, error) {
	source, err := evaluation.LoadVerifiedLegacyReviewSource(store, queueID)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	references := projectionEvidenceReferences(source.Queue)
	if len(references) > maximumProjectionEvidenceBlocks {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("Agent projection references %d evidence events; maximum is %d",
			len(references), maximumProjectionEvidenceBlocks)
	}
	extracted, err := extractEvidencePrefix(store, references,
		source.Pack.SourceEvidencePrefix.Records, source.Pack.SourceEvidencePrefix.LastRecordHash,
		source.Queue.QueueID)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}

	projection := Projection{
		SchemaVersion: ProjectionSchema,
		QueueID:       source.Queue.QueueID, QueueSHA256: hashBytes(source.QueueBytes),
		PackID: source.Pack.PackID, PackSHA256: hashBytes(source.PackBytes),
		CorpusID: source.Pack.CorpusID, CorpusContentSHA256: source.Pack.CorpusContentSHA256,
		CandidateManifestSHA256: source.Pack.CandidateManifestSHA256,
		CandidatesSHA256:        source.Pack.CandidatesSHA256,
		EpisodesSHA256:          source.Pack.EpisodesSHA256,
		SourceEvidencePrefix: SourcePrefix{
			Records:        source.Pack.SourceEvidencePrefix.Records,
			LastRecordHash: source.Pack.SourceEvidencePrefix.LastRecordHash,
		},
		ExtractionVersion: ExtractionVersion,
		Privacy:           "local_only",
	}
	projection.EvidenceBlocks = sortedEvidenceBlocks(extracted)
	projection.CandidateItems = projectCandidates(source.Queue, extracted)
	projection.CompactionItems = projectCompactions(source.Queue, extracted)
	permuteProjectionItems(&projection)
	payload, payloadBytes, err := buildBlindPayload(projection)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, err
	}
	projection.PayloadID = payload.PayloadID
	projection.PayloadContentSHA256 = payload.PayloadContentSHA256
	identityBytes, err := json.Marshal(projection)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("encode Agent projection identity: %w", err)
	}
	projection.ProjectionContentSHA256 = hashBytes(identityBytes)
	projection.ProjectionID = "agent-projection-" + projection.ProjectionContentSHA256
	data, err := marshalIndented(projection)
	if err != nil {
		return Projection{}, nil, BlindPayload{}, nil, fmt.Errorf("encode Agent projection: %w", err)
	}
	return projection, data, payload, payloadBytes, nil
}

func buildBlindPayload(projection Projection) (BlindPayload, []byte, error) {
	payload := BlindPayload{
		SchemaVersion: BlindPayloadSchema, ArtifactStorage: "local_only",
		DataClassification: "selected_unredacted_evidence",
	}
	payload.EvidenceBlocks = make([]BlindEvidenceBlock, 0, len(projection.EvidenceBlocks))
	for index, block := range projection.EvidenceBlocks {
		payload.EvidenceBlocks = append(payload.EvidenceBlocks, BlindEvidenceBlock{
			BlockID: block.BlockID, Order: index + 1, Role: blindEvidenceRole(block.Kind),
			Text: block.Text, UntrustedContent: true,
		})
	}
	for _, item := range projection.CandidateItems {
		payload.CandidateItems = append(payload.CandidateItems, BlindCandidateItem{
			ItemID: item.ItemID, Text: item.Text, UntrustedContent: true,
			EvidenceBlockIDs: append([]string{}, item.EvidenceBlockIDs...),
		})
	}
	for _, item := range projection.CompactionItems {
		blind := BlindCompactionItem{
			ItemID:                     item.ItemID,
			CheckpointEvidenceBlockIDs: append([]string{}, item.CheckpointEvidenceBlockIDs...),
		}
		for _, unit := range item.Units {
			blind.Units = append(blind.Units, BlindCompactionUnit{
				UnitID: unit.UnitID, Statement: unit.Statement, UntrustedContent: true,
				SourceEvidenceBlockIDs:         append([]string{}, unit.SourceEvidenceBlockIDs...),
				RepresentationEvidenceBlockIDs: append([]string{}, unit.RepresentationEvidenceBlockIDs...),
				CorrectionEvidenceBlockIDs:     append([]string{}, unit.CorrectionEvidenceBlockIDs...),
			})
		}
		payload.CompactionItems = append(payload.CompactionItems, blind)
	}
	identityBytes, err := json.Marshal(payload)
	if err != nil {
		return BlindPayload{}, nil, fmt.Errorf("encode blind assessment payload identity: %w", err)
	}
	payload.PayloadContentSHA256 = hashBytes(identityBytes)
	payload.PayloadID = "agent-payload-" + payload.PayloadContentSHA256
	data, err := marshalIndented(payload)
	if err != nil {
		return BlindPayload{}, nil, fmt.Errorf("encode blind assessment payload: %w", err)
	}
	if int64(len(data)) > maximumBlindPayloadBytes {
		return BlindPayload{}, nil, errors.New("blind assessment payload exceeds the safety limit")
	}
	return payload, data, nil
}

func blindEvidenceRole(kind ledger.EventKind) string {
	switch kind {
	case ledger.KindUserMessage:
		return "user"
	case ledger.KindAgentMessage, ledger.KindReasoning, ledger.KindSubagentEvent:
		return "agent"
	case ledger.KindToolCall, ledger.KindToolResult, ledger.KindFileChange:
		return "tool"
	case ledger.KindCompaction:
		return "compaction"
	default:
		return "system"
	}
}

func projectionEvidenceReferences(queue evaluation.LegacyReviewQueue) map[string]struct{} {
	result := map[string]struct{}{}
	for _, item := range queue.CandidateItems {
		for _, observation := range item.Sample.CorpusObservations {
			addReferences(result, observation.EvidenceEventIDs)
			addReferences(result, observation.CorrectionEventIDs)
		}
	}
	for _, item := range queue.CompactionItems {
		addReferences(result, item.Sample.EventIDs)
		addReferences(result, item.Sample.RepresentationEventIDs)
		for _, check := range item.Sample.Checks {
			if check.Statement != nil {
				addReferences(result, check.Statement.EvidenceEventIDs)
			}
			addReferences(result, check.Check.RepresentationEventIDs)
			addReferences(result, check.Check.CorrectionEventIDs)
		}
	}
	return result
}

func addReferences(target map[string]struct{}, values []string) {
	for _, value := range values {
		target[value] = struct{}{}
	}
}

func extractEvidencePrefix(
	store *ledger.Store, wanted map[string]struct{}, prefixRecords int, prefixHash, queueID string,
) (map[string]extractedEvidence, error) {
	if prefixRecords < 1 || !validSHA256(prefixHash) {
		return nil, errors.New("Agent projection evidence prefix is invalid")
	}
	records := make(map[string]evidenceRecord, len(wanted))
	sequence := 0
	prefixSeen := false
	err := store.VisitRecords(func(record ledger.Record) error {
		sequence++
		if sequence == prefixRecords {
			prefixSeen = true
			if record.RecordHash != prefixHash {
				return errors.New("Agent projection evidence prefix hash does not match the ledger")
			}
		}
		if sequence > prefixRecords {
			return nil
		}
		if _, needed := wanted[record.Event.EventID]; !needed {
			return nil
		}
		if _, duplicate := records[record.Event.EventID]; duplicate {
			return fmt.Errorf("duplicate evidence event id %q in ledger prefix", record.Event.EventID)
		}
		records[record.Event.EventID] = evidenceRecord{record: record, sequence: sequence}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !prefixSeen {
		return nil, errors.New("Agent projection evidence prefix is longer than the ledger")
	}
	result := make(map[string]extractedEvidence, len(wanted))
	for eventID := range wanted {
		record, exists := records[eventID]
		if !exists {
			result[eventID] = extractedEvidence{issue: "missing_event"}
			continue
		}
		result[eventID] = extractEvidence(store, queueID, record)
	}
	return result, nil
}

func extractEvidence(store *ledger.Store, queueID string, source evidenceRecord) extractedEvidence {
	event := source.record.Event
	block := EvidenceBlock{
		BlockID:      "blind-evidence-" + hashStrings(queueID, event.EventID),
		RecordSHA256: source.record.RecordHash,
		Sequence:     source.sequence, Kind: event.Kind, Agent: event.Source.Agent,
		Completeness: event.Completeness.Status, UntrustedContent: true,
	}
	issue := ""
	if event.Completeness.Status != ledger.CompletenessComplete {
		issue = "incomplete_event"
	}
	if event.Payload == nil {
		return extractedEvidence{block: block, issue: firstIssue(issue, "missing_payload")}
	}
	block.PayloadSHA256 = event.Payload.SHA256
	block.PayloadBytes = event.Payload.Bytes
	if !textPayloadEncoding(event.Payload.Encoding, event.Payload.MediaType) {
		return extractedEvidence{block: block, issue: firstIssue(issue, "non_text_payload")}
	}
	if event.Payload.Bytes > maximumEvidenceTextBytes {
		return extractedEvidence{block: block, issue: firstIssue(issue, "oversized_payload")}
	}
	var data []byte
	if event.Payload.Content != nil {
		data = []byte(*event.Payload.Content)
	} else if event.Payload.Blob != nil {
		file, err := store.OpenBlob(*event.Payload.Blob)
		if err != nil {
			return extractedEvidence{block: block, issue: firstIssue(issue, "blob_unavailable")}
		}
		data, err = io.ReadAll(io.LimitReader(file, maximumEvidenceTextBytes+1))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return extractedEvidence{block: block, issue: firstIssue(issue, "blob_unavailable")}
		}
	} else {
		return extractedEvidence{block: block, issue: firstIssue(issue, "missing_payload")}
	}
	if int64(len(data)) != event.Payload.Bytes || hashBytes(data) != event.Payload.SHA256 {
		return extractedEvidence{block: block, issue: firstIssue(issue, "payload_mismatch")}
	}
	if !utf8.Valid(data) {
		return extractedEvidence{block: block, issue: firstIssue(issue, "non_text_payload")}
	}
	block.Text = string(data)
	block.ExtractedSHA256 = hashBytes(data)
	return extractedEvidence{block: block, issue: issue}
}

func textMediaType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return value == "" || strings.HasPrefix(value, "text/") || value == "application/json" ||
		value == "application/jsonl" || value == "application/x-ndjson"
}

func textPayloadEncoding(encoding, mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(strings.Split(mediaType, ";")[0]))
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "utf-8":
		return textMediaType(mediaType)
	case "json":
		return mediaType == "application/json"
	case "jsonl", "ndjson":
		return mediaType == "application/jsonl" || mediaType == "application/x-ndjson"
	default:
		return false
	}
}

func firstIssue(existing, next string) string {
	if existing != "" {
		return existing
	}
	return next
}

func sortedEvidenceBlocks(values map[string]extractedEvidence) []EvidenceBlock {
	result := make([]EvidenceBlock, 0, len(values))
	for _, value := range values {
		if value.block.Text == "" && value.block.ExtractedSHA256 == "" {
			continue
		}
		result = append(result, value.block)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Sequence != result[j].Sequence {
			return result[i].Sequence < result[j].Sequence
		}
		return result[i].BlockID < result[j].BlockID
	})
	return result
}

func projectCandidates(
	queue evaluation.LegacyReviewQueue, evidence map[string]extractedEvidence,
) []CandidateItem {
	result := make([]CandidateItem, 0, len(queue.CandidateItems))
	for index, item := range queue.CandidateItems {
		references := map[string]struct{}{}
		for _, observation := range item.Sample.CorpusObservations {
			addReferences(references, observation.EvidenceEventIDs)
			addReferences(references, observation.CorrectionEventIDs)
		}
		blocks, complete, gaps := projectedReferences(references, evidence)
		result = append(result, CandidateItem{
			ItemID:        blindItemID(queue.QueueID, "candidate", index),
			SubjectSHA256: hashBytes([]byte(item.Sample.Text)), Text: item.Sample.Text,
			UntrustedContent: true, EvidenceBlockIDs: blocks, EvidenceComplete: complete,
			EvidenceGapCodes: gaps,
		})
	}
	return result
}

func projectCompactions(
	queue evaluation.LegacyReviewQueue, evidence map[string]extractedEvidence,
) []CompactionItem {
	result := make([]CompactionItem, 0, len(queue.CompactionItems))
	for itemIndex, item := range queue.CompactionItems {
		checkpointRefs := stringSet(item.Sample.EventIDs)
		checkpointBlocks, checkpointComplete, checkpointGaps := projectedReferences(checkpointRefs, evidence)
		projected := CompactionItem{
			ItemID:                     blindItemID(queue.QueueID, "compaction", itemIndex),
			CheckpointEvidenceBlockIDs: checkpointBlocks,
			CheckpointEvidenceComplete: checkpointComplete,
			AllUnitsProjected:          item.Sample.TotalChecks == len(item.Sample.Checks),
			ProjectionGapCodes:         checkpointGaps,
		}
		if !projected.AllUnitsProjected {
			projected.ProjectionGapCodes = uniqueSortedStrings(append(
				projected.ProjectionGapCodes, "checks_not_fully_projected"))
		}
		for checkIndex, check := range item.Sample.Checks {
			if check.Statement == nil {
				continue
			}
			sourceRefs := stringSet(check.Statement.EvidenceEventIDs)
			representationIDs := check.Check.RepresentationEventIDs
			if len(representationIDs) == 0 {
				representationIDs = item.Sample.RepresentationEventIDs
			}
			representationRefs := stringSet(representationIDs)
			correctionRefs := stringSet(check.Check.CorrectionEventIDs)
			sourceBlocks, sourceComplete, sourceGaps := projectedReferences(sourceRefs, evidence)
			representationBlocks, representationComplete, representationGaps := projectedReferences(representationRefs, evidence)
			correctionBlocks, correctionComplete, correctionGaps := projectedReferences(correctionRefs, evidence)
			if len(correctionRefs) == 0 {
				correctionComplete = true
			}
			gaps := uniqueSortedStrings(append(append(append(
				append([]string{}, checkpointGaps...), sourceGaps...), representationGaps...), correctionGaps...))
			if len(representationRefs) == 0 {
				gaps = uniqueSortedStrings(append(gaps, "missing_representation"))
				representationComplete = false
			}
			projected.Units = append(projected.Units, CompactionUnit{
				UnitID:        hashID("assessment-unit-", queue.QueueID, "compaction-unit", strconv.Itoa(itemIndex), strconv.Itoa(checkIndex)),
				SubjectSHA256: hashBytes([]byte(check.Statement.Text)), Statement: check.Statement.Text,
				UntrustedContent: true, SourceEvidenceBlockIDs: sourceBlocks,
				RepresentationEvidenceBlockIDs: representationBlocks,
				CorrectionEvidenceBlockIDs:     correctionBlocks,
				EvidenceComplete:               checkpointComplete && sourceComplete && representationComplete && correctionComplete,
				EvidenceGapCodes:               gaps,
			})
		}
		result = append(result, projected)
	}
	return result
}

func blindItemID(queueID, kind string, index int) string {
	return hashID("agent-item-", queueID, kind, strconv.Itoa(index))
}

// permuteProjectionItems removes the review queue's label-correlated ordering
// before either artifact is encoded. The ordering key is derived from local
// bindings that are deliberately absent from the blind payload.
func permuteProjectionItems(projection *Projection) {
	key := projectionPermutationKey(*projection)
	sort.Slice(projection.CandidateItems, func(i, j int) bool {
		left := hashStrings(key, "candidate", projection.CandidateItems[i].ItemID)
		right := hashStrings(key, "candidate", projection.CandidateItems[j].ItemID)
		if left != right {
			return left < right
		}
		return projection.CandidateItems[i].ItemID < projection.CandidateItems[j].ItemID
	})
	sort.Slice(projection.CompactionItems, func(i, j int) bool {
		left := hashStrings(key, "compaction", projection.CompactionItems[i].ItemID)
		right := hashStrings(key, "compaction", projection.CompactionItems[j].ItemID)
		if left != right {
			return left < right
		}
		return projection.CompactionItems[i].ItemID < projection.CompactionItems[j].ItemID
	})
	for itemIndex := range projection.CompactionItems {
		item := &projection.CompactionItems[itemIndex]
		sort.Slice(item.Units, func(i, j int) bool {
			left := hashStrings(key, "compaction-unit", item.Units[i].UnitID)
			right := hashStrings(key, "compaction-unit", item.Units[j].UnitID)
			if left != right {
				return left < right
			}
			return item.Units[i].UnitID < item.Units[j].UnitID
		})
	}
}

func projectionPermutationKey(projection Projection) string {
	return hashStrings(projection.QueueSHA256, projection.PackSHA256,
		projection.SourceEvidencePrefix.LastRecordHash, "blind-order")
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	addReferences(result, values)
	return result
}

func projectedReferences(
	references map[string]struct{}, evidence map[string]extractedEvidence,
) ([]string, bool, []string) {
	blocks := make([]string, 0, len(references))
	gaps := make([]string, 0)
	complete := len(references) > 0
	for eventID := range references {
		value, exists := evidence[eventID]
		if !exists || value.issue != "" {
			complete = false
			if !exists {
				gaps = append(gaps, "missing_event")
			} else {
				gaps = append(gaps, value.issue)
			}
		}
		if exists && value.block.ExtractedSHA256 != "" {
			blocks = append(blocks, value.block.BlockID)
		}
	}
	sort.Strings(blocks)
	return blocks, complete, uniqueSortedStrings(gaps)
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func hashID(prefix string, values ...string) string {
	return prefix + hashStrings(values...)
}

func hashStrings(values ...string) string {
	return hashBytes([]byte(strings.Join(values, "\x00")))
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func marshalIndented(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
