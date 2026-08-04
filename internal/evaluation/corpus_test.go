package evaluation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestFreezeLegacyCorpusPreservesCardsAndReusesCapturedRollouts(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "2026", "2026-08-05", "card.md")
	rolloutPath := filepath.Join(base, "sessions", "rollout-test.jsonl")
	writeTestFile(t, cardPath, []byte("# card\n\nlocal-only content\n"))
	rollout := strings.Join([]string{
		`{"timestamp":"2026-08-05T00:00:00Z","type":"session_meta","payload":{"id":"session-test"}}`,
		`{"timestamp":"2026-08-05T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":"remember this"}}`,
		`{not-json}`,
	}, "\n") + "\n"
	writeTestFile(t, rolloutPath, []byte(rollout))
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "session-test",
		TranscriptPath: rolloutPath, LatestUser: "remember this", LatestAssistant: "ack"}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	now := time.Unix(300, 0).UTC()
	if _, err := codex.ImportPath(store, rolloutPath, codex.Options{
		Now: func() time.Time { return now }, FullReconcile: true,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-test", Now: func() time.Time { return now.Add(time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.Cards != 1 || result.Counts.IndexEntries != 1 ||
		result.Counts.CapturedRollouts != 1 || result.Counts.MissingRollouts != 0 ||
		result.Counts.CardsWithLatestUser != 1 || result.Counts.CardsWithLatestAssistant != 1 {
		t.Fatalf("unexpected corpus counts: %+v", result.Counts)
	}
	if result.SnapshotsAppended != 2 {
		t.Fatalf("index and card snapshots were not appended: %+v", result)
	}
	verification := VerifyCorpus(store, result.CorpusID)
	if len(verification.Issues) != 0 {
		t.Fatalf("corpus verification failed: %+v", verification)
	}
	manifest, err := LoadCorpusManifest(store, result.CorpusID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Rollouts) != 1 || manifest.Rollouts[0].Status != "captured" ||
		manifest.Rollouts[0].GapEvents != 1 || result.Counts.ProjectionGapEvents != 1 {
		t.Fatalf("projection gap was confused with raw-byte loss: %+v %+v",
			manifest.Rollouts, result.Counts)
	}
	baseline, err := BuildLegacyCaptureInput(store, result.CorpusID, LegacyCaptureInputOptions{
		SuiteID: "legacy-capture", RunID: "run-1", SystemVersion: "test-v1",
		MinimumCoverage: 1, Now: func() time.Time { return now.Add(2 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	assessed, err := Run(store, baseline,
		RunOptions{Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !assessed.Report.ReleaseReady || assessed.Report.Capture.Unit != CaptureUnitLegacyRollouts ||
		assessed.Report.Capture.Complete != 1 {
		t.Fatalf("legacy corpus baseline was not evidence-bound: %+v", assessed.Report)
	}
	baseline.RunID = "run-2"
	baseline.Cases[0].Capture = &CaptureMeasurement{
		Unit: CaptureUnitLegacyRollouts, Expected: 1, Partial: 1,
	}
	mismatched, err := Run(store, baseline,
		RunOptions{Now: func() time.Time { return now.Add(4 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if mismatched.Report.ReleaseReady ||
		!containsIssue(mismatched.Report.Issues, "do not match the corpus manifest") {
		t.Fatalf("fabricated legacy rollout counts were accepted: %+v", mismatched.Report)
	}
	manifestBytes, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifestBytes), legacyRoot) || strings.Contains(string(manifestBytes), rolloutPath) {
		t.Fatal("manifest leaked an absolute source path")
	}
	reused, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-test", Now: func() time.Time { return now.Add(5 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.CorpusID != result.CorpusID {
		t.Fatalf("identical corpus was not reused: %+v", reused)
	}
}

func TestFreezeLegacyCorpusReportsUncapturedRolloutWithoutInventingEvidence(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "2026", "card.md")
	writeTestFile(t, cardPath, []byte("# card\n"))
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "session-missing",
		TranscriptPath: filepath.Join(base, "sessions", "missing.jsonl")}
	line, _ := json.Marshal(entry)
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	result, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-missing", Now: func() time.Time { return time.Unix(400, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Counts.MissingRollouts != 1 || result.Counts.CapturedRollouts != 0 {
		t.Fatalf("missing rollout was not explicit: %+v", result.Counts)
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Code == "rollout_not_captured" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing rollout issue was not reported: %+v", result.Issues)
	}
	if verification := VerifyCorpus(store, result.CorpusID); len(verification.Issues) != 0 {
		t.Fatalf("honestly incomplete corpus failed integrity verification: %+v", verification)
	}
}

func TestVerifyCorpusChecksManifestBlobContent(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeTestFile(t, cardPath, []byte("# card\n"))
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "session-test"}
	line, _ := json.Marshal(entry)
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	result, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "manifest-blob", Now: func() time.Time { return time.Unix(450, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(store.Root(), filepath.FromSlash(expectedBlobPath(result.ManifestSHA256)))
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	verification := VerifyCorpus(store, result.CorpusID)
	if !containsIssue(verification.Issues, "manifest blob is invalid") {
		t.Fatalf("tampered manifest blob was accepted: %+v", verification)
	}
}

func TestFreezeLegacyCorpusRejectsCardLinkOutsideRoot(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	outside := filepath.Join(base, "outside.md")
	writeTestFile(t, outside, []byte("outside\n"))
	cardPath := filepath.Join(legacyRoot, "summaries", "linked.md")
	if err := os.MkdirAll(filepath.Dir(cardPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cardPath); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "session-test"}
	line, _ := json.Marshal(entry)
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	_, err = FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{Name: "linked-card"})
	if err == nil || !strings.Contains(err.Error(), "escapes legacy root") {
		t.Fatalf("external card link was accepted: %v", err)
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
