package candidates

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestListReturnsVerifiedFilteredCandidates(t *testing.T) {
	store, episodePath := createEpisodeGeneration(t, policyEpisodes())
	result, err := Build(store, BuildOptions{
		EpisodeGenerationPath: episodePath,
		ShardCount:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := OpenGeneration(store, filepath.Base(result.GenerationPath))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := generation.List(ListOptions{Statuses: []ReviewStatus{StatusReviewReady}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if listed.SchemaVersion != ListSchemaVersion || listed.Total != result.Candidates ||
		listed.Matching != result.ReviewReady || listed.Returned != result.ReviewReady ||
		listed.Truncated || len(listed.Candidates) != result.ReviewReady {
		t.Fatalf("unexpected candidate list: %+v", listed)
	}
	for _, item := range listed.Candidates {
		if item.Candidate.Validation.Status != StatusReviewReady {
			t.Fatalf("non-review-ready candidate was returned: %+v", item)
		}
		digest := sha256.Sum256([]byte(item.Candidate.Text))
		if item.TextSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("candidate text confirmation hash is invalid: %+v", item)
		}
	}
}

func TestListRejectsInvalidLimitAndTamperedGeneration(t *testing.T) {
	store, episodePath := createEpisodeGeneration(t, policyEpisodes())
	result, err := Build(store, BuildOptions{
		EpisodeGenerationPath: episodePath,
		ShardCount:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := OpenGeneration(store, filepath.Base(result.GenerationPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generation.List(ListOptions{Limit: 1001}); err == nil {
		t.Fatal("oversized candidate list was accepted")
	}
	path := filepath.Join(generation.Path, "candidates.jsonl")
	data := []byte("{}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := generation.List(ListOptions{Limit: 10}); err == nil {
		t.Fatal("tampered candidate generation was accepted")
	}
}
