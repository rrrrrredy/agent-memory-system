package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/workflow"
)

func TestUnpromotedValidatedCountDoesNotUseGlobalActivePopulation(t *testing.T) {
	oldCandidate := candidates.Candidate{CandidateID: "candidate-" + strings.Repeat("a", 64),
		ContentSHA256: strings.Repeat("b", 64)}
	currentCandidate := candidates.Candidate{CandidateID: "candidate-" + strings.Repeat("c", 64),
		ContentSHA256: strings.Repeat("d", 64)}
	histories := []promotion.History{{MemoryID: "memory-" + strings.Repeat("e", 64), Revisions: []promotion.Revision{{
		Source: &promotion.Source{CandidateGeneration: "old-generation", CandidateID: oldCandidate.CandidateID,
			CandidateContentSHA256: oldCandidate.ContentSHA256},
	}}}}
	if got := unpromotedValidatedCount("current-generation", []candidates.Candidate{currentCandidate}, histories); got != 1 {
		t.Fatalf("old active memory hid current pending promotion: got %d", got)
	}
	histories = append(histories, promotion.History{MemoryID: "memory-" + strings.Repeat("f", 64),
		Revisions: []promotion.Revision{{Source: &promotion.Source{CandidateGeneration: "current-generation",
			CandidateID: currentCandidate.CandidateID, CandidateContentSHA256: currentCandidate.ContentSHA256}}}})
	if got := unpromotedValidatedCount("current-generation", []candidates.Candidate{currentCandidate}, histories); got != 0 {
		t.Fatalf("exact promoted source remained pending: got %d", got)
	}
}

func TestRepeatedOnboardReportsPromotionAndExportActions(t *testing.T) {
	root := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "examples", "quickstart"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"codex", "--root", root, "--path", fixture, "--shards", "1"}
	first := captureOnboardResult(t, args)
	if first.NextAction != "review_candidates" {
		t.Fatalf("first onboarding action = %q", first.NextAction)
	}
	store, err := ledger.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := candidates.OpenCurrentGeneration(store)
	if err != nil {
		t.Fatal(err)
	}
	items, err := generation.All()
	if err != nil || len(items) != 1 {
		t.Fatalf("candidate population = %d, err = %v", len(items), err)
	}
	candidate := items[0]
	reviewed, err := review.Apply(store, generation.Name, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: review.ReviewerKindSyntheticTest, ID: "quickstart-test"},
		Transitions: []review.TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
			Scope: &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis: []review.Basis{review.BasisExplicitRemember}, Reason: "Synthetic lifecycle validation.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	validated := captureOnboardResult(t, args)
	if validated.NextAction != "promote_validated_candidates" {
		t.Fatalf("validated onboarding action = %q", validated.NextAction)
	}
	digest := sha256.Sum256([]byte(candidate.Text))
	if _, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: promotion.ApproverKindSyntheticTest, ID: "quickstart-test"},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation.Name, CandidateID: candidate.CandidateID,
			CandidateContentSHA256: candidate.ContentSHA256, ExpectedReviewRecordSHA: reviewed.RecordSHA256,
		},
		ExpectedTextSHA256: hex.EncodeToString(digest[:]), Reason: "Synthetic lifecycle promotion.",
	}); err != nil {
		t.Fatal(err)
	}
	promoted := captureOnboardResult(t, args)
	if promoted.NextAction != "export_and_sync" {
		t.Fatalf("promoted onboarding action = %q", promoted.NextAction)
	}
}

func captureOnboardResult(t *testing.T, args []string) workflow.OnboardResult {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "onboard-*.json")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = output
	runErr := runOnboard(args)
	closeErr := output.Close()
	os.Stdout = previous
	data, readErr := os.ReadFile(output.Name())
	if runErr != nil || closeErr != nil || readErr != nil {
		t.Fatalf("onboard failed: run=%v close=%v read=%v", runErr, closeErr, readErr)
	}
	var result workflow.OnboardResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode onboard result: %v\n%s", err, data)
	}
	return result
}
