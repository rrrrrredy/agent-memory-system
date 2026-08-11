package evaluation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const compactionTruthRollout = `{"timestamp":"2026-08-04T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555"}}
{"timestamp":"2026-08-04T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"You must preserve every tool call."}}
{"timestamp":"2026-08-04T00:00:02Z","type":"compacted","payload":{"window_id":"window-1","summary":"Preserve every tool call."}}
{"timestamp":"2026-08-04T00:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"Please support offline mode."}}
{"timestamp":"2026-08-04T00:00:04Z","type":"compacted","payload":{"window_id":"window-2","summary":"Continue implementation."}}
{"timestamp":"2026-08-04T00:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"I said again: support offline mode."}}
`

func TestCompactionGroundTruthMustBeSealedBeforeDetectorGeneration(t *testing.T) {
	t.Run("post-hoc attestation is not ground truth", func(t *testing.T) {
		store, manifest, compactionIDs := compactionTruthFixture(t)
		generation, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		_, episodeSet, err := episodes.LoadVerifiedGeneration(store,
			generation.SourceRecords, generation.SourceLastRecordHash)
		if err != nil {
			t.Fatal(err)
		}
		checkpointID := ""
		for _, episode := range episodeSet {
			for _, checkpoint := range episode.Compactions {
				if containsString(checkpoint.EventIDs, compactionIDs[0]) {
					checkpointID = checkpoint.CheckpointID
				}
			}
		}
		if checkpointID == "" {
			t.Fatal("detector checkpoint for the frozen compaction was unavailable")
		}
		_, err = RecordAttestation(store, EvaluationAttestation{
			SchemaVersion: EvaluationAttestationSchema, AttestationID: "posthoc-label",
			CaseID: "posthoc-compaction-case", Category: CategoryCompactionDrift, Agent: ledger.AgentCodex,
			Attestor:   Attestor{Kind: "human", ID: "reviewer"},
			AttestedAt: time.Unix(10_000, 0).UTC(), Reason: "label copied after detector output",
			Compaction: &CompactionExpectation{CheckpointID: checkpointID, Expected: DriftPreserved},
		}, func() time.Time { return time.Unix(10_001, 0).UTC() })
		if err != nil {
			t.Fatal(err)
		}
		records, ordered, err := loadIndexedRecords(store)
		if err != nil {
			t.Fatal(err)
		}
		subjects, groundTruthRecord, issues := loadSealedCompactionGroundTruth(store,
			records, ordered, manifest.CorpusID, manifest.CorpusContentSHA256)
		if len(subjects) != 0 || groundTruthRecord != nil ||
			!strings.Contains(strings.Join(issues, "; "), "sealed compaction ground truth is unavailable") {
			t.Fatalf("post-hoc attestation substituted for a sealed pack: subjects=%+v issues=%+v", subjects, issues)
		}
	})

	t.Run("detector generation before seal permanently disqualifies blind labels", func(t *testing.T) {
		store, manifest, compactionIDs := compactionTruthFixture(t)
		generation, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(generation.GenerationPath); err != nil {
			t.Fatal(err)
		}
		_, err = SealCompactionGroundTruth(store, SealCompactionGroundTruthOptions{
			CorpusID: manifest.CorpusID, ReviewerID: "reviewer", Reason: "labels entered after detector output",
			Labels: []CompactionGroundTruthLabel{
				{EventID: compactionIDs[0], Expected: DriftPreserved},
				{EventID: compactionIDs[1], Expected: DriftDetected},
			}, Now: func() time.Time { return time.Unix(10_500, 0).UTC() },
		})
		if err == nil {
			t.Fatal("ground truth was sealed after a retained detector generation exposed the labels")
		}
	})

	t.Run("sealed pack is bound into later detector generation", func(t *testing.T) {
		store, manifest, compactionIDs := compactionTruthFixture(t)
		seal, err := SealCompactionGroundTruth(store, SealCompactionGroundTruthOptions{
			CorpusID: manifest.CorpusID, ReviewerID: "reviewer", Reason: "blind review of frozen pre and post evidence",
			Labels: []CompactionGroundTruthLabel{
				{EventID: compactionIDs[0], Expected: DriftPreserved},
				{EventID: compactionIDs[1], Expected: DriftDetected},
			}, Now: func() time.Time { return time.Unix(11_000, 0).UTC() },
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
		if err != nil {
			t.Fatal(err)
		}
		records, ordered, err := loadIndexedRecords(store)
		if err != nil {
			t.Fatal(err)
		}
		subjects, groundTruthRecord, issues := loadSealedCompactionGroundTruth(store,
			records, ordered, manifest.CorpusID, manifest.CorpusContentSHA256)
		if len(issues) != 0 || len(subjects) != len(compactionIDs) || groundTruthRecord == nil ||
			groundTruthRecord.Record.Event.EventID != seal.EventID {
			t.Fatalf("sealed ground truth did not replay exactly: subjects=%+v issues=%+v", subjects, issues)
		}
		cases, _, detectorReady, detectorIssues := buildCompactionPopulation(store,
			len(ordered), ordered[len(ordered)-1].RecordHash, ordered, subjects, groundTruthRecord)
		if !detectorReady || len(detectorIssues) != 0 || len(cases) != len(compactionIDs) {
			t.Fatalf("later detector generation was not bound to the sealed pack: ready=%v cases=%+v issues=%+v",
				detectorReady, cases, detectorIssues)
		}
	})
}

func compactionTruthFixture(t *testing.T) (*ledger.Store, CorpusManifest, []string) {
	t.Helper()
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "rollout-2026-08-04T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(source, []byte(compactionTruthRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.ImportPath(store, source, codex.Options{
		Now: func() time.Time { return time.Unix(9_000, 0).UTC() }, FullReconcile: true,
	}); err != nil {
		t.Fatal(err)
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	_ = records
	compactionIDs := []string{}
	for _, record := range ordered {
		if record.Event.Kind == ledger.KindCompaction {
			compactionIDs = append(compactionIDs, record.Event.EventID)
		}
	}
	if len(compactionIDs) != 2 {
		t.Fatalf("fixture captured %d compactions, want 2", len(compactionIDs))
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeTestFile(t, cardPath, []byte("# frozen compaction control\n"))
	entry := legacyIndexEntry{CardPath: cardPath,
		SessionID: "11111111-2222-3333-4444-555555555555", TranscriptPath: source}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	frozen, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "compaction-truth-control", Now: func() time.Time { return time.Unix(9_100, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadCorpusManifest(store, frozen.CorpusID)
	if err != nil {
		t.Fatal(err)
	}
	return store, manifest, compactionIDs
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
