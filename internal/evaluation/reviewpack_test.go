package evaluation

import (
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

	reused, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.PackID != result.PackID || reused.PackSHA256 != result.PackSHA256 {
		t.Fatalf("identical review material was not reused: %+v", reused)
	}
	if err := os.WriteFile(result.PackPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLegacyReviewPack(store, frozen.CorpusID, candidateGeneration.GenerationPath,
		LegacyReviewPackOptions{SamplePerStratum: 10}); err == nil ||
		!strings.Contains(err.Error(), "does not match its deterministic identity") {
		t.Fatalf("tampered review pack was accepted: %v", err)
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
