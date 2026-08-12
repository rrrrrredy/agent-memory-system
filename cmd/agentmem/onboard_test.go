package main

import (
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
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
