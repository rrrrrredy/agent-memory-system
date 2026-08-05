package sourcerecovery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestPlanLegacyCodexFindsRelocatedSourceAndProducesApplicableManifest(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeRecoveryTestFile(t, cardPath, []byte("# card\n"))
	threadID := "00000000-0000-0000-0000-000000000061"
	originalPath := filepath.Join(base, "sessions", "missing.jsonl")
	aliasCardPath := filepath.Join(legacyRoot, "summaries", "alias-card.md")
	writeRecoveryTestFile(t, aliasCardPath, []byte("# alias card\n"))
	aliasLine, err := json.Marshal(map[string]string{
		"card_path":       aliasCardPath,
		"session_id":      "00000000-0000-0000-0000-000000000060",
		"transcript_path": originalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexLine, err := json.Marshal(map[string]string{
		"card_path": cardPath, "session_id": threadID, "transcript_path": originalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexData := append(append([]byte{}, aliasLine...), '\n')
	indexData = append(indexData, indexLine...)
	indexData = append(indexData, '\n')
	writeRecoveryTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), indexData)
	frozen, err := evaluation.FreezeLegacyCorpus(store, legacyRoot, evaluation.FreezeOptions{
		Name: "relocation-plan", Now: func() time.Time { return time.Unix(8000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Counts.MissingRollouts != 1 {
		t.Fatalf("test corpus is not missing one rollout: %+v", frozen.Counts)
	}

	sourceRoot := filepath.Join(base, "archive")
	relative := filepath.ToSlash(filepath.Join("2027", "rollout-2027-03-01T08-00-00-"+threadID+".jsonl"))
	rollout := []byte(`{"timestamp":"2027-03-01T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `"}}` + "\n")
	writeRecoveryTestFile(t, filepath.Join(sourceRoot, filepath.FromSlash(relative)), rollout)
	output := filepath.Join(base, "recovery.json")
	plan, err := PlanLegacyCodex(store, frozen.CorpusID, sourceRoot, output, time.Unix(8001, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Expected != 1 || plan.Available != 1 || plan.Missing != 0 || plan.Ambiguous != 0 {
		t.Fatalf("unexpected recovery plan: %+v", plan)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := DecodeManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	wantedLogicalHash, err := adapterjsonl.HashSourcePath(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Status != StatusAvailable ||
		manifest.Entries[0].RelativePath != relative ||
		manifest.Entries[0].ThreadID != threadID ||
		manifest.Entries[0].LogicalSourcePathSHA256 != wantedLogicalHash {
		t.Fatalf("relocated source was not bound to its original identity: %+v", manifest.Entries)
	}
	result, err := Apply(store, sourceRoot, bytes.NewReader(raw), time.Unix(8002, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcesRecovered != 1 || result.GapsAppended != 0 {
		t.Fatalf("planned recovery was not applicable: %+v", result)
	}
}

func TestPlanLegacyCodexMarksUnidentifiedCandidatesAsIncompleteSearch(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeRecoveryTestFile(t, cardPath, []byte("# card\n"))
	indexLine, _ := json.Marshal(map[string]string{
		"card_path": cardPath, "session_id": "00000000-0000-0000-0000-000000000063",
		"transcript_path": filepath.Join(base, "sessions", "missing.jsonl"),
	})
	writeRecoveryTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(indexLine, '\n'))
	frozen, err := evaluation.FreezeLegacyCorpus(store, legacyRoot, evaluation.FreezeOptions{
		Name: "incomplete-search", Now: func() time.Time { return time.Unix(8200, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(base, "archive")
	writeRecoveryTestFile(t, filepath.Join(sourceRoot, "rollout-unidentified.jsonl"), []byte("{}\n"))
	output := filepath.Join(base, "incomplete.json")
	plan, err := PlanLegacyCodex(store, frozen.CorpusID, sourceRoot, output, time.Unix(8201, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := DecodeManifest(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if plan.FilesExamined != 1 || len(manifest.Entries) != 1 ||
		manifest.Entries[0].Reason != "source_not_found_search_incomplete" ||
		!hasPlanIssue(plan.Issues, "candidate_identity_missing") {
		t.Fatalf("incomplete search was reported as exhaustive: plan=%+v manifest=%+v", plan, manifest)
	}
}

func TestPlanLegacyCodexQuarantinesAmbiguousSources(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(base, "legacy")
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	writeRecoveryTestFile(t, cardPath, []byte("# card\n"))
	threadID := "00000000-0000-0000-0000-000000000062"
	indexLine, _ := json.Marshal(map[string]string{
		"card_path": cardPath, "session_id": threadID,
		"transcript_path": filepath.Join(base, "sessions", "missing.jsonl"),
	})
	writeRecoveryTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(indexLine, '\n'))
	frozen, err := evaluation.FreezeLegacyCorpus(store, legacyRoot, evaluation.FreezeOptions{
		Name: "ambiguous-plan", Now: func() time.Time { return time.Unix(8100, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(base, "archive")
	for index, message := range []string{"first", "second"} {
		data := []byte(`{"timestamp":"2027-03-01T08:00:00Z","type":"session_meta","payload":{"id":"` + threadID + `"}}` + "\n" +
			`{"timestamp":"2027-03-01T08:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":"` + message + `"}}` + "\n")
		writeRecoveryTestFile(t, filepath.Join(sourceRoot, string(rune('a'+index)),
			"rollout-2027-03-01T08-00-00-"+threadID+".jsonl"), data)
	}
	output := filepath.Join(base, "ambiguous.json")
	plan, err := PlanLegacyCodex(store, frozen.CorpusID, sourceRoot, output, time.Unix(8101, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Available != 0 || plan.Missing != 1 || plan.Ambiguous != 1 || len(plan.Issues) != 1 ||
		plan.Issues[0].Code != "source_ambiguous_after_local_search" {
		t.Fatalf("ambiguous sources were selected silently: %+v", plan)
	}
}

func writeRecoveryTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasPlanIssue(issues []PlanIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
