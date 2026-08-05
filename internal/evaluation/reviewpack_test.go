package evaluation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestPrepareLegacyReviewPackSamplesOnlyFrozenCorpusAndReusesIdentity(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	sessionsRoot := filepath.Join(base, "sessions")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rollouts := map[string]string{
		"legacy-session-one": reviewPackRolloutOne,
		"legacy-session-two": reviewPackRolloutTwo,
	}
	index := []byte{}
	for sessionID, content := range rollouts {
		rolloutPath := filepath.Join(sessionsRoot, sessionID+".jsonl")
		cardPath := filepath.Join(legacyRoot, "summaries", sessionID+".md")
		writeTestFile(t, rolloutPath, []byte(content))
		writeTestFile(t, cardPath, []byte("# frozen card\n"))
		if _, err := codex.ImportPath(store, rolloutPath, codex.Options{
			Now: func() time.Time { return now }, FullReconcile: true,
		}); err != nil {
			t.Fatal(err)
		}
		entry := legacyIndexEntry{CardPath: cardPath, SessionID: sessionID, TranscriptPath: rolloutPath}
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		index = append(index, append(line, '\n')...)
	}
	unrelatedPath := filepath.Join(sessionsRoot, "unrelated-session.jsonl")
	writeTestFile(t, unrelatedPath, []byte(reviewPackUnrelatedRollout))
	if _, err := codex.ImportPath(store, unrelatedPath, codex.Options{
		Now: func() time.Time { return now }, FullReconcile: true,
	}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), index)
	frozen, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-review", Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	episodeGeneration, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	candidateGeneration, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeGeneration.GenerationPath, ShardCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused || result.UniqueCandidates == 0 || result.CandidateSamples == 0 ||
		result.CompactionSamples == 0 || !strings.HasPrefix(result.PackID, "review-pack-") {
		t.Fatalf("unexpected review pack result: %+v", result)
	}
	for _, stratum := range candidateReviewStrata {
		if result.CandidatePopulation[stratum] == 0 {
			t.Fatalf("candidate stratum %q was not populated: %+v", stratum, result.CandidatePopulation)
		}
	}
	for _, stratum := range compactionReviewStrata {
		if result.CompactionPopulation[stratum] == 0 {
			t.Fatalf("compaction stratum %q was not populated: %+v", stratum, result.CompactionPopulation)
		}
	}
	data, err := os.ReadFile(result.PackPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), legacyRoot) || strings.Contains(string(data), sessionsRoot) {
		t.Fatal("review pack leaked an absolute source path")
	}
	if strings.Contains(string(data), "outside frozen corpus") {
		t.Fatal("review pack sampled an episode that was not part of the frozen corpus")
	}
	var pack LegacyReviewPack
	if err := json.Unmarshal(data, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.PackID != result.PackID || pack.CorpusID != frozen.CorpusID || pack.Privacy != "local_only" ||
		pack.SourceEvidencePrefix.Records == 0 {
		t.Fatalf("review pack lost its evidence bindings: %+v", pack)
	}
	for _, samples := range pack.CandidateSamples {
		for _, sample := range samples {
			if len(sample.CorpusObservations) == 0 || sample.Scope.Agent != ledger.AgentCodex ||
				sample.CandidateContentSHA256 == "" || sample.Privacy != "local_only" {
				t.Fatalf("candidate escaped the frozen Codex corpus: %+v", sample)
			}
		}
	}
	for _, samples := range pack.CompactionSamples {
		for _, sample := range samples {
			checkPopulation := 0
			for _, count := range sample.CheckPopulation {
				checkPopulation += count
			}
			if sample.TotalChecks < len(sample.Checks) || len(sample.Checks) > maximumCheckpointChecks ||
				checkPopulation != sample.TotalChecks || sample.Agent != ledger.AgentCodex ||
				sample.Privacy != "local_only" {
				t.Fatalf("compaction projection is not bounded or corpus-scoped: %+v", sample)
			}
		}
	}
	assertReviewQueueRejectsLinkedOutput(t, store, result.PackID, base)

	queueResult, err := PrepareLegacyReviewQueue(store, result.PackID, LegacyReviewQueueOptions{
		CandidateLimit: 3, CompactionLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queueResult.Reused || queueResult.CandidateItems != 3 || queueResult.CompactionItems != 2 ||
		!strings.HasPrefix(queueResult.QueueID, "review-queue-") || queueResult.Privacy != "local_only" {
		t.Fatalf("unexpected review queue result: %+v", queueResult)
	}
	queueData, err := os.ReadFile(queueResult.QueuePath)
	if err != nil {
		t.Fatal(err)
	}
	markdownData, err := os.ReadFile(queueResult.MarkdownPath)
	if err != nil {
		t.Fatal(err)
	}
	combined := string(queueData) + string(markdownData)
	if strings.Contains(combined, legacyRoot) || strings.Contains(combined, sessionsRoot) {
		t.Fatal("review queue leaked an absolute source path")
	}
	if !strings.Contains(string(markdownData), "untrusted evidence") ||
		!strings.Contains(string(markdownData), "immutable audit input") {
		t.Fatal("review queue Markdown omitted its safety or workflow instructions")
	}
	for _, forbidden := range []string{`"judgment"`, `"reason"`, `"reviewer_kind"`, `"decision_template"`} {
		if strings.Contains(string(queueData), forbidden) {
			t.Fatalf("review queue contains decision field %s", forbidden)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(queueResult.QueuePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "queue.json" || entries[1].Name() != "review.md" {
		t.Fatalf("review queue directory contains unexpected files: %+v", entries)
	}
	var queue LegacyReviewQueue
	if err := json.Unmarshal(queueData, &queue); err != nil {
		t.Fatal(err)
	}
	if queue.QueueID != queueResult.QueueID || queue.PackID != result.PackID ||
		queue.PackSHA256 != result.PackSHA256 || len(queue.CandidateItems) != 3 ||
		len(queue.CompactionItems) != 2 || queue.Privacy != "local_only" {
		t.Fatalf("review queue lost its evidence binding: %+v", queue)
	}
	verifiedSource, err := LoadVerifiedLegacyReviewSource(store, queueResult.QueueID)
	if err != nil {
		t.Fatal(err)
	}
	if verifiedSource.Queue.QueueID != queueResult.QueueID ||
		verifiedSource.Pack.PackID != result.PackID ||
		!bytes.Equal(verifiedSource.QueueBytes, queueData) ||
		!bytes.Equal(verifiedSource.PackBytes, data) {
		t.Fatalf("verified review source lost its immutable bindings: %+v", verifiedSource)
	}
	seenCandidateItems := map[string]struct{}{}
	for _, item := range queue.CandidateItems {
		if item.ItemID == "" {
			t.Fatalf("candidate review item has no identity: %+v", item)
		}
		if _, duplicate := seenCandidateItems[item.Sample.CandidateID]; duplicate {
			t.Fatalf("candidate review item was duplicated: %s", item.Sample.CandidateID)
		}
		seenCandidateItems[item.Sample.CandidateID] = struct{}{}
	}
	seenCompactionItems := map[string]struct{}{}
	for _, item := range queue.CompactionItems {
		if item.ItemID == "" {
			t.Fatalf("compaction review item has no identity: %+v", item)
		}
		key := item.Sample.EpisodeID + "\x00" + item.Sample.CheckpointID
		if _, duplicate := seenCompactionItems[key]; duplicate {
			t.Fatalf("compaction review item was duplicated: %s", item.Sample.CheckpointID)
		}
		seenCompactionItems[key] = struct{}{}
	}
	reusedQueue, err := PrepareLegacyReviewQueue(store, result.PackID, LegacyReviewQueueOptions{
		CandidateLimit: 3, CompactionLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reusedQueue.Reused || reusedQueue.QueueID != queueResult.QueueID {
		t.Fatalf("identical review queue was not reused: %+v", reusedQueue)
	}
	assertReviewQueueRejectsLinkedInput(t, store, result.PackID, result.PackPath)

	reused, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.PackID != result.PackID || reused.PackSHA256 != result.PackSHA256 {
		t.Fatalf("identical review material was not reused: %+v", reused)
	}
	invalidCandidate := cloneReviewPack(t, pack)
	for _, stratum := range candidateReviewStrata {
		if len(invalidCandidate.CandidateSamples[stratum]) > 0 {
			invalidCandidate.CandidateSamples[stratum][0].CorpusSupportTypes = nil
			break
		}
	}
	writeReviewPackForRejection(t, result.PackPath, invalidCandidate)
	if _, err := PrepareLegacyReviewQueue(store, result.PackID, LegacyReviewQueueOptions{}); err == nil ||
		!strings.Contains(err.Error(), "candidate sample") {
		t.Fatalf("schema-invalid candidate sample was accepted: %v", err)
	}
	if err := os.WriteFile(result.PackPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidCompaction := cloneReviewPack(t, pack)
	for _, stratum := range compactionReviewStrata {
		if len(invalidCompaction.CompactionSamples[stratum]) > 0 {
			invalidCompaction.CompactionSamples[stratum][0].CheckPopulation = nil
			break
		}
	}
	writeReviewPackForRejection(t, result.PackPath, invalidCompaction)
	if _, err := PrepareLegacyReviewQueue(store, result.PackID, LegacyReviewQueueOptions{}); err == nil ||
		!strings.Contains(err.Error(), "check population") {
		t.Fatalf("schema-invalid compaction sample was accepted: %v", err)
	}
	if err := os.WriteFile(result.PackPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.PackPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 10}); err == nil ||
		!strings.Contains(err.Error(), "does not match its deterministic identity") {
		t.Fatalf("tampered review pack was accepted: %v", err)
	}
	if _, err := PrepareLegacyReviewQueue(store, result.PackID, LegacyReviewQueueOptions{}); err == nil {
		t.Fatal("tampered review pack was accepted by review queue generation")
	}
}

func assertReviewQueueRejectsLinkedOutput(t *testing.T, store *ledger.Store, packID, base string) {
	t.Helper()
	queueRoot := filepath.Join(store.Root(), "derived", "evaluations", "review-queues")
	outside := filepath.Join(base, "outside-review-queues")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, queueRoot); err != nil {
		t.Logf("symbolic links are unavailable for review queue output boundary test: %v", err)
		return
	}
	_, queueErr := PrepareLegacyReviewQueue(store, packID, LegacyReviewQueueOptions{
		CandidateLimit: 1, CompactionLimit: 1,
	})
	if err := os.Remove(queueRoot); err != nil {
		t.Fatal(err)
	}
	if queueErr == nil || (!strings.Contains(queueErr.Error(), "links") &&
		!strings.Contains(queueErr.Error(), "reparse")) {
		t.Fatalf("linked review queue output was accepted: %v", queueErr)
	}
}

func assertReviewQueueRejectsLinkedInput(t *testing.T, store *ledger.Store, packID, packPath string) {
	t.Helper()
	realPath := packPath + ".real"
	if err := os.Rename(packPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, packPath); err != nil {
		if restoreErr := os.Rename(realPath, packPath); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		t.Logf("symbolic links are unavailable for review queue input boundary test: %v", err)
		return
	}
	_, queueErr := PrepareLegacyReviewQueue(store, packID, LegacyReviewQueueOptions{
		CandidateLimit: 1, CompactionLimit: 1,
	})
	if err := os.Remove(packPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(realPath, packPath); err != nil {
		t.Fatal(err)
	}
	if queueErr == nil || (!strings.Contains(queueErr.Error(), "links") &&
		!strings.Contains(queueErr.Error(), "reparse")) {
		t.Fatalf("linked review pack input was accepted: %v", queueErr)
	}
}

func cloneReviewPack(t *testing.T, source LegacyReviewPack) LegacyReviewPack {
	t.Helper()
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result LegacyReviewPack
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func writeReviewPackForRejection(t *testing.T, path string, pack LegacyReviewPack) {
	t.Helper()
	data, err := marshalIndented(pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareLegacyReviewQueueRejectsUnsafeInputsBeforeReading(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLegacyReviewQueue(store, "../review-pack-bad", LegacyReviewQueueOptions{}); err == nil ||
		!strings.Contains(err.Error(), "invalid review pack id") {
		t.Fatalf("unsafe review pack id was accepted: %v", err)
	}
	validPackID := "review-pack-" + strings.Repeat("a", 64)
	if _, err := PrepareLegacyReviewQueue(store, validPackID,
		LegacyReviewQueueOptions{CandidateLimit: maximumLegacyReviewQueueLimit + 1}); err == nil ||
		!strings.Contains(err.Error(), "candidate_limit") {
		t.Fatalf("invalid candidate limit was accepted: %v", err)
	}
}

func TestPrepareLegacyReviewPackRejectsStaleCandidates(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	rolloutPath := filepath.Join(base, "session.jsonl")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeTestFile(t, rolloutPath, []byte(reviewPackRolloutTwo))
	writeTestFile(t, cardPath, []byte("# card\n"))
	if _, err := codex.ImportPath(store, rolloutPath, codex.Options{FullReconcile: true}); err != nil {
		t.Fatal(err)
	}
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "legacy-session-two", TranscriptPath: rolloutPath}
	line, _ := json.Marshal(entry)
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	frozen, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{Name: "stale-review"})
	if err != nil {
		t.Fatal(err)
	}
	episodeGeneration, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidateGeneration, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeGeneration.GenerationPath, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := "later evidence"
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "later-evidence-event", Kind: ledger.KindAgentMessage,
		ObservedAt: time.Now().UTC(), RecordedAt: time.Now().UTC(),
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "review-pack-test",
			AdapterVersion: "review-pack-test/v1", DeviceID: store.DeviceID(), ThreadID: "later-thread"},
		Payload: &ledger.Payload{Encoding: "utf-8", MediaType: "text/plain",
			Content: &content, SHA256: sha256Hex([]byte(content)), Bytes: int64(len(content))},
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 1}); err == nil ||
		!strings.Contains(err.Error(), "candidate generation is not current") {
		t.Fatalf("stale candidate generation was accepted: %v", err)
	}
}

const reviewPackRolloutOne = `{"timestamp":"2026-08-05T00:00:00Z","type":"session_meta","payload":{"id":"legacy-session-one"}}
{"timestamp":"2026-08-05T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Please remember that raw evidence must stay local."}}
{"timestamp":"2026-08-05T00:00:02Z","type":"event_msg","payload":{"type":"user_message","message":"You must keep reports local."}}
{"timestamp":"2026-08-05T00:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"You must upload debug logs."}}
{"timestamp":"2026-08-05T00:00:04Z","type":"event_msg","payload":{"type":"user_message","message":"You must not upload debug logs."}}
{"timestamp":"2026-08-05T00:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"You must retain audit timestamps."}}
{"timestamp":"2026-08-05T00:00:06Z","type":"compacted","payload":{"window_id":"window-preserved","summary":"Raw evidence must stay local. Keep reports local. Upload debug logs. Retain audit timestamps."}}
{"timestamp":"2026-08-05T00:00:07Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-05T00:00:08Z","type":"event_msg","payload":{"type":"user_message","message":"You must support offline mode."}}
{"timestamp":"2026-08-05T00:00:09Z","type":"compacted","payload":{"window_id":"window-risk","summary":"Continue implementation."}}
{"timestamp":"2026-08-05T00:00:10Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-05T00:00:11Z","type":"compacted","payload":{"window_id":"window-insufficient"}}
{"timestamp":"2026-08-05T00:00:12Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-05T00:00:13Z","type":"event_msg","payload":{"type":"user_message","message":"You must archive raw evidence."}}
{"timestamp":"2026-08-05T00:00:14Z","type":"compacted","payload":{"window_id":"window-drift","summary":"Continue implementation."}}
{"timestamp":"2026-08-05T00:00:15Z","type":"event_msg","payload":{"type":"agent_message","message":"Continuing."}}
{"timestamp":"2026-08-05T00:00:16Z","type":"event_msg","payload":{"type":"user_message","message":"I said again: you must archive raw evidence."}}
{"timestamp":"2026-08-05T00:00:17Z","type":"compacted","payload":{"window_id":"window-close","summary":"Continue implementation."}}
`

const reviewPackRolloutTwo = `{"timestamp":"2026-08-05T01:00:00Z","type":"session_meta","payload":{"id":"legacy-session-two"}}
{"timestamp":"2026-08-05T01:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"You must keep reports local."}}
`

const reviewPackUnrelatedRollout = `{"timestamp":"2026-08-05T02:00:00Z","type":"session_meta","payload":{"id":"unrelated-session"}}
{"timestamp":"2026-08-05T02:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"You must retain this outside frozen corpus."}}
`
