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
	"path"
	"sort"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/capturesupervisor"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func buildCapturePopulation(store *ledger.Store, ordered []ledger.Record,
	snapshot capturesupervisor.EvaluationSnapshot, requiredAgents []ledger.Agent,
	trialCutoff time.Time) ([]EvaluationCase, bool, bool, []string) {
	if !trialCutoff.IsZero() && snapshot.CompletedAt.Before(trialCutoff.UTC()) {
		return nil, false, false, []string{"independent capture inventory predates the evaluated trial cutoff"}
	}
	cases := make([]EvaluationCase, 0, len(requiredAgents))
	issues := []string{}
	projectionComplete := true
	for _, agent := range requiredAgents {
		evaluationCase := EvaluationCase{
			CaseID:   populationCaseID(CategoryCaptureCoverage, string(agent)),
			Category: CategoryCaptureCoverage, Agent: agent,
			Evidence: []EvidenceReference{},
			Capture:  &CaptureMeasurement{Unit: CaptureUnitSourceInventory},
		}
		seenEvidence := map[string]struct{}{}
		for _, source := range snapshot.Sources {
			if source.Agent != agent {
				continue
			}
			for _, item := range source.Items {
				evaluationCase.Capture.Expected++
				itemRecords := captureItemRecords(ordered, agent, item)
				for _, record := range itemRecords {
					if record.Event.Kind != ledger.KindSourceSnapshot && record.Event.Kind != ledger.KindGap {
						continue
					}
					if _, duplicate := seenEvidence[record.Event.EventID]; duplicate {
						continue
					}
					seenEvidence[record.Event.EventID] = struct{}{}
					evaluationCase.Evidence = append(evaluationCase.Evidence, ledgerEvidence(record))
				}
				if item.Status != "available" {
					evaluationCase.Capture.Missing++
					if hasInventoryGap(itemRecords) {
						evaluationCase.Capture.AccountedMissing++
					} else {
						issues = append(issues, "capture inventory item has no explicit gap: "+item.IdentitySHA256)
					}
					projectionComplete = false
					continue
				}
				rawComplete, projected, auditErr := auditCapturedItem(store, item, itemRecords)
				if auditErr != nil {
					issues = append(issues, "capture inventory item audit failed: "+item.IdentitySHA256+": "+auditErr.Error())
				}
				switch {
				case rawComplete && projected:
					evaluationCase.Capture.Complete++
				case rawComplete:
					evaluationCase.Capture.Partial++
					projectionComplete = false
				default:
					evaluationCase.Capture.Missing++
					projectionComplete = false
				}
			}
		}
		if evaluationCase.Capture.Expected == 0 {
			issues = append(issues, "independent capture inventory is empty for "+string(agent))
			projectionComplete = false
			continue
		}
		if len(evaluationCase.Evidence) == 0 {
			issues = append(issues, "capture inventory has no ledger evidence for "+string(agent))
			projectionComplete = false
		}
		cases = append(cases, evaluationCase)
	}
	return cases, true, projectionComplete, uniqueSorted(issues)
}

func storeCapturePopulationSnapshot(store *ledger.Store,
	snapshot capturesupervisor.EvaluationSnapshot) (ledger.BlobRef, error) {
	if err := capturesupervisor.VerifyEvaluationSnapshot(store, snapshot,
		[]ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode}); err != nil {
		return ledger.BlobRef{}, fmt.Errorf("verify capture population snapshot: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return ledger.BlobRef{}, fmt.Errorf("encode capture population snapshot: %w", err)
	}
	return store.PutBlob(bytes.NewReader(data))
}

func loadCapturePopulationSnapshot(store *ledger.Store, reference ledger.BlobRef,
	requiredAgents []ledger.Agent) (capturesupervisor.EvaluationSnapshot, error) {
	data, err := readAttemptBlob(store, reference)
	if err != nil {
		return capturesupervisor.EvaluationSnapshot{}, fmt.Errorf("read capture population snapshot: %w", err)
	}
	var snapshot capturesupervisor.EvaluationSnapshot
	if decodeStrictEvaluationJSON(data, &snapshot) != nil {
		return capturesupervisor.EvaluationSnapshot{}, errors.New("capture population snapshot is invalid")
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(canonical, data) {
		return capturesupervisor.EvaluationSnapshot{}, errors.New("capture population snapshot is not canonical")
	}
	if err := capturesupervisor.VerifyEvaluationSnapshot(store, snapshot, requiredAgents); err != nil {
		return capturesupervisor.EvaluationSnapshot{}, fmt.Errorf("verify capture population snapshot: %w", err)
	}
	return snapshot, nil
}

func captureItemRecords(ordered []ledger.Record, agent ledger.Agent,
	item capturesupervisor.InventoryItem) []ledger.Record {
	result := []ledger.Record{}
	for _, record := range ordered {
		if record.Event.Source.Agent != agent {
			continue
		}
		matches := record.Event.Source.SourcePathHash == item.IdentitySHA256
		if item.Type == "session" && record.Event.Source.SessionID != "" {
			digest := sha256.Sum256([]byte(record.Event.Source.SessionID))
			matches = hex.EncodeToString(digest[:]) == item.IdentitySHA256
		}
		if matches {
			result = append(result, record)
		}
	}
	return result
}

func hasInventoryGap(records []ledger.Record) bool {
	for _, record := range records {
		if record.Event.Kind == ledger.KindGap && record.Event.Completeness.Status == ledger.CompletenessMissing {
			return true
		}
	}
	return false
}

func auditCapturedItem(store *ledger.Store, item capturesupervisor.InventoryItem,
	records []ledger.Record) (bool, bool, error) {
	if item.Type == "session" {
		return auditOpenCodeSession(store, records)
	}
	if item.Type != "file" {
		return false, false, errors.New("unsupported available inventory item type")
	}
	full, err := openInventoryBlob(store, item)
	if err != nil {
		return false, false, err
	}
	defer full.Close()
	validParents, mediaType, rawComplete, err := verifyCurrentSnapshotSegments(store, full, item.Bytes, records)
	if err != nil || !rawComplete {
		return rawComplete, false, err
	}
	if item.Bytes == 0 {
		return true, true, nil
	}
	switch mediaType {
	case "application/x-ndjson", "application/jsonl", "application/ndjson":
		if _, err := full.Seek(0, io.SeekStart); err != nil {
			return true, false, err
		}
		return true, auditJSONLProjection(full, records, validParents), nil
	case "application/json":
		if _, err := full.Seek(0, io.SeekStart); err != nil {
			return true, false, err
		}
		return true, auditOpenCodeProjection(full, records, validParents), nil
	default:
		return true, true, nil
	}
}

func openInventoryBlob(store *ledger.Store, item capturesupervisor.InventoryItem) (*ledgerBlobFile, error) {
	reference := ledger.BlobRef{SHA256: item.ContentSHA256, Bytes: item.Bytes,
		RelativePath: path.Join("evidence", "blobs", "sha256", item.ContentSHA256[:2], item.ContentSHA256[2:])}
	file, err := store.OpenBlob(reference)
	if err != nil {
		return nil, err
	}
	return &ledgerBlobFile{File: file}, nil
}

type ledgerBlobFile struct {
	File interface {
		io.Reader
		io.ReaderAt
		io.Seeker
		io.Closer
	}
}

func (file *ledgerBlobFile) Read(buffer []byte) (int, error) { return file.File.Read(buffer) }
func (file *ledgerBlobFile) ReadAt(buffer []byte, offset int64) (int, error) {
	return file.File.ReadAt(buffer, offset)
}
func (file *ledgerBlobFile) Seek(offset int64, whence int) (int64, error) {
	return file.File.Seek(offset, whence)
}
func (file *ledgerBlobFile) Close() error { return file.File.Close() }

type captureSegment struct {
	start, end int64
	record     ledger.Record
}

func verifyCurrentSnapshotSegments(store *ledger.Store, full io.ReaderAt, expectedBytes int64,
	records []ledger.Record) (map[string]struct{}, string, bool, error) {
	segments := []captureSegment{}
	for _, record := range records {
		event := record.Event
		if event.Kind != ledger.KindSourceSnapshot || event.Payload == nil || event.Payload.Blob == nil ||
			event.Source.ByteStart == nil || event.Source.ByteEnd == nil {
			continue
		}
		segments = append(segments, captureSegment{start: *event.Source.ByteStart,
			end: *event.Source.ByteEnd, record: record})
	}
	sort.Slice(segments, func(left, right int) bool {
		if segments[left].start != segments[right].start {
			return segments[left].start < segments[right].start
		}
		return segments[left].end > segments[right].end
	})
	validParents := map[string]struct{}{}
	mediaType := ""
	ranges := [][2]int64{}
	for _, segment := range segments {
		if segment.start < 0 || segment.end < segment.start || segment.end > expectedBytes {
			continue
		}
		if !segmentMatchesFull(store, full, segment) {
			continue
		}
		validParents[segment.record.Event.EventID] = struct{}{}
		mediaType = segment.record.Event.Payload.MediaType
		ranges = append(ranges, [2]int64{segment.start, segment.end})
	}
	if expectedBytes == 0 {
		return validParents, mediaType, true, nil
	}
	cursor := int64(0)
	for _, interval := range ranges {
		if interval[0] > cursor {
			break
		}
		if interval[1] > cursor {
			cursor = interval[1]
		}
	}
	return validParents, mediaType, cursor == expectedBytes, nil
}

func segmentMatchesFull(store *ledger.Store, full io.ReaderAt, segment captureSegment) bool {
	reference := *segment.record.Event.Payload.Blob
	file, err := store.OpenBlob(reference)
	if err != nil {
		return false
	}
	defer file.Close()
	expected := segment.end - segment.start
	if reference.Bytes != expected {
		return false
	}
	buffer, wanted := make([]byte, 64*1024), make([]byte, 64*1024)
	offset := int64(0)
	for offset < expected {
		length := int64(len(buffer))
		if remaining := expected - offset; remaining < length {
			length = remaining
		}
		left, leftErr := io.ReadFull(file, buffer[:length])
		right, rightErr := full.ReadAt(wanted[:length], segment.start+offset)
		if left != int(length) || right != int(length) || leftErr != nil || (rightErr != nil && rightErr != io.EOF) {
			return false
		}
		for index := 0; index < int(length); index++ {
			if buffer[index] != wanted[index] {
				return false
			}
		}
		offset += length
	}
	return true
}

func auditJSONLProjection(reader io.Reader, records []ledger.Record, validParents map[string]struct{}) bool {
	buffered := bufio.NewReader(reader)
	offset := int64(0)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) != 0 {
			start, end := offset, offset+int64(len(line))
			offset = end
			if !hasRangeProjection(records, validParents, start, end) {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

func hasRangeProjection(records []ledger.Record, validParents map[string]struct{}, start, end int64) bool {
	for _, record := range records {
		event := record.Event
		if !isNormalizedCaptureEvent(event) || event.Source.ByteStart == nil || event.Source.ByteEnd == nil ||
			*event.Source.ByteStart != start || *event.Source.ByteEnd != end ||
			event.Completeness.Status != ledger.CompletenessComplete || event.Payload == nil {
			continue
		}
		if hasValidParent(event, validParents) {
			return true
		}
	}
	return false
}

func isNormalizedCaptureEvent(event ledger.Event) bool {
	switch event.Kind {
	case ledger.KindUserMessage, ledger.KindAgentMessage, ledger.KindReasoning,
		ledger.KindToolCall, ledger.KindToolResult, ledger.KindApproval,
		ledger.KindFileChange, ledger.KindAttachment, ledger.KindSubagentEvent,
		ledger.KindCompaction, ledger.KindSystemEvent, ledger.KindUnknown:
		return true
	default:
		return false
	}
}

func auditOpenCodeSession(store *ledger.Store, records []ledger.Record) (bool, bool, error) {
	for index := len(records) - 1; index >= 0; index-- {
		event := records[index].Event
		if event.Kind != ledger.KindSourceSnapshot || event.Payload == nil || event.Payload.Blob == nil {
			continue
		}
		data, err := readAttemptBlob(store, *event.Payload.Blob)
		if err != nil {
			return false, false, err
		}
		parents := map[string]struct{}{event.EventID: {}}
		projected := auditOpenCodeProjection(bytes.NewReader(data), records, parents)
		return event.Completeness.Status == ledger.CompletenessComplete, projected, nil
	}
	return false, false, errors.New("session has no preserved source snapshot")
}

func auditOpenCodeProjection(reader io.Reader, records []ledger.Record, validParents map[string]struct{}) bool {
	var document struct {
		Info     json.RawMessage `json:"info"`
		Messages []struct {
			Info  json.RawMessage   `json:"info"`
			Parts []json.RawMessage `json:"parts"`
		} `json:"messages"`
	}
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&document); err != nil || len(document.Info) == 0 {
		return false
	}
	pointers := []string{"/info"}
	for messageIndex, message := range document.Messages {
		if len(message.Info) == 0 {
			return false
		}
		pointers = append(pointers, fmt.Sprintf("/messages/%d/info", messageIndex))
		for partIndex := range message.Parts {
			pointers = append(pointers, fmt.Sprintf("/messages/%d/parts/%d", messageIndex, partIndex))
		}
	}
	for _, pointer := range pointers {
		found := false
		for _, record := range records {
			event := record.Event
			if isNormalizedCaptureEvent(event) && event.Source.SourceCursor == "json:"+pointer &&
				event.Completeness.Status == ledger.CompletenessComplete && event.Payload != nil &&
				hasValidParent(event, validParents) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasValidParent(event ledger.Event, validParents map[string]struct{}) bool {
	if event.Causality == nil {
		return false
	}
	for _, parent := range event.Causality.ParentEventIDs {
		if _, valid := validParents[parent]; valid {
			return true
		}
	}
	return false
}

func efficacyPrerequisiteIssues(prerequisites EfficacyPrerequisites) []string {
	checks := []struct {
		ok   bool
		name string
	}{
		{prerequisites.FrozenCorpusVerified, "verified frozen regression corpus"},
		{prerequisites.IndependentCaptureInventory, "independent capture inventory"},
		{prerequisites.NormalizedProjectionCoverage, "normalized projection coverage"},
		{prerequisites.CompletePortablePopulation, "complete portable population"},
		{prerequisites.PreregisteredAttemptUniverse, "preregistered attempt universe"},
		{prerequisites.PairedTrialPlanSealed, "paired trial plan sealed before results"},
		{prerequisites.ExecutionSupervisorReceipts, "execution supervisor receipts"},
		{prerequisites.VerifiedAgentExecution, "verified native Agent execution provenance"},
		{prerequisites.SystemArtifactManifest, "system artifact manifest"},
		{prerequisites.IndependentCompactionDetector, "independent compaction detector"},
		{prerequisites.SealedCompactionGroundTruth, "sealed compaction ground truth"},
		{prerequisites.FrozenCompactionDriftControl, "frozen compaction drift control"},
		{prerequisites.FrozenCompactionPreserveControl, "frozen compaction preservation control"},
		{prerequisites.BlindOracleProtocol, "blind oracle protocol"},
		{prerequisites.HermeticOracleExecution, "hermetic oracle execution"},
	}
	issues := []string{}
	for _, check := range checks {
		if !check.ok {
			issues = append(issues, "efficacy prerequisite missing: "+check.name)
		}
	}
	return issues
}
