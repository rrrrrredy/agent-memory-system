package evaluation

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
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
	if result.Counts.UnaccountedMissingRollouts != 1 || result.Counts.AccountedMissingRollouts != 0 {
		t.Fatalf("silent and accounted missing sources were conflated: %+v", result.Counts)
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

	pathHash, err := adapterjsonl.HashSourcePath(entry.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedGap := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "unrelated-missing-gap-test", Kind: ledger.KindGap,
		ObservedAt: time.Unix(400, 0).UTC(), RecordedAt: time.Unix(400, 0).UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "codex-jsonl", AdapterVersion: "codex-jsonl/v1alpha1",
			DeviceID: store.DeviceID(), ThreadID: entry.SessionID, SourcePathHash: pathHash,
			SourceCursor: "recovery:missing",
		},
		Completeness: ledger.Completeness{
			Status: ledger.CompletenessMissing, Reason: "unrelated_missing_projection",
		},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(unrelatedGap); err != nil {
		t.Fatal(err)
	}
	stillUnaccounted, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-unrelated-gap", Now: func() time.Time { return time.Unix(401, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if stillUnaccounted.Counts.AccountedMissingRollouts != 0 ||
		stillUnaccounted.Counts.UnaccountedMissingRollouts != 1 {
		t.Fatalf("unrelated missing gap was treated as source recovery evidence: %+v", stillUnaccounted.Counts)
	}
	gap := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "source-recovery-gap-test", Kind: ledger.KindGap,
		ObservedAt: time.Unix(402, 0).UTC(), RecordedAt: time.Unix(402, 0).UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "source-recovery", AdapterVersion: "source-recovery/v1alpha1",
			DeviceID: store.DeviceID(), ThreadID: entry.SessionID, SourcePathHash: pathHash,
			SourceCursor: "recovery:missing",
		},
		Completeness: ledger.Completeness{
			Status: ledger.CompletenessMissing, Reason: "source_not_found_after_local_search",
		},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	if _, err := store.Append(gap); err != nil {
		t.Fatal(err)
	}
	accounted, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "legacy-accounted-missing", Now: func() time.Time { return time.Unix(403, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if accounted.CorpusID == result.CorpusID || accounted.Counts.AccountedMissingRollouts != 1 ||
		accounted.Counts.UnaccountedMissingRollouts != 0 || accounted.Counts.MissingRollouts != 1 {
		t.Fatalf("explicit source gap was not separated from silent loss: %+v", accounted)
	}
	manifest, err := LoadCorpusManifest(store, accounted.CorpusID)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Rollouts) != 1 || len(manifest.Rollouts[0].MissingGapEventIDs) != 1 ||
		manifest.Rollouts[0].MissingGapEventIDs[0] != gap.EventID {
		t.Fatalf("corpus did not bind the missing-source evidence: %+v", manifest.Rollouts)
	}
	verification := VerifyCorpus(store, accounted.CorpusID)
	if len(verification.Issues) != 0 || verification.MissingGapEventsChecked != 1 {
		t.Fatalf("accounted missing corpus failed verification: %+v", verification)
	}
	baseline, err := BuildLegacyCaptureInput(store, accounted.CorpusID, LegacyCaptureInputOptions{
		SuiteID: "accounted-capture", RunID: "run-accounted", SystemVersion: "test-v1",
		MinimumCoverage: 1, Now: func() time.Time { return time.Unix(404, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Cases) != 1 || baseline.Cases[0].Capture.AccountedMissing != 1 {
		t.Fatalf("baseline omitted accounted missing evidence: %+v", baseline.Cases)
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

func TestFreezeLegacyCorpusIndexLimitStoresAndReusesOnlyTheSelectedPrefix(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	firstCard := filepath.Join(legacyRoot, "summaries", "first.md")
	secondCard := filepath.Join(legacyRoot, "summaries", "second.md")
	writeTestFile(t, firstCard, []byte("first card\n"))
	writeTestFile(t, secondCard, []byte("second card\n"))
	first, _ := json.Marshal(legacyIndexEntry{CardPath: firstCard, SessionID: "first-session"})
	second, _ := json.Marshal(legacyIndexEntry{CardPath: secondCard, SessionID: "second-session"})
	indexPath := filepath.Join(legacyRoot, "data", "index.jsonl")
	indexData := append(append(append([]byte{}, first...), '\n'), second...)
	indexData = append(indexData, '\n')
	writeTestFile(t, indexPath, indexData)

	options := FreezeOptions{Name: "limited-index", IndexEntryLimit: 1,
		Now: func() time.Time { return time.Unix(500, 0).UTC() }}
	frozen, err := FreezeLegacyCorpus(store, legacyRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadCorpusManifest(store, frozen.CorpusID)
	if err != nil {
		t.Fatal(err)
	}
	var indexBlob *ledger.BlobRef
	for _, artifact := range manifest.Artifacts {
		if artifact.Role == RoleLegacyIndex {
			copy := artifact.Blob
			indexBlob = &copy
			break
		}
	}
	if indexBlob == nil {
		t.Fatal("limited corpus has no index artifact")
	}
	file, err := store.OpenBlob(*indexBlob)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(append(first, '\n')) || strings.Contains(string(stored), "second-session") {
		t.Fatalf("index artifact escaped its selected prefix: %q", stored)
	}
	third, _ := json.Marshal(legacyIndexEntry{CardPath: secondCard, SessionID: "third-session"})
	writeTestFile(t, indexPath, append(indexData, append(third, '\n')...))
	replayed, err := FreezeLegacyCorpus(store, legacyRoot, options)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.CorpusID != frozen.CorpusID || !replayed.Reused {
		t.Fatalf("data beyond the selected prefix changed the corpus: first=%+v replay=%+v", frozen, replayed)
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
