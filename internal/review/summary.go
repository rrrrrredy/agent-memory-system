package review

import (
	"errors"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const SummarySchemaVersion = "candidate-review-summary/v1alpha1"

// Summary is the authoritative current review state for one verified
// candidate generation.
type Summary struct {
	SchemaVersion string `json:"schema_version"`
	Generation    string `json:"generation"`
	Total         int    `json:"total"`
	Pending       int    `json:"pending"`
	Validated     int    `json:"validated"`
	Rejected      int    `json:"rejected"`
	Quarantined   int    `json:"quarantined"`
	Privacy       string `json:"privacy"`
}

// Summarize verifies the complete candidate generation and review ledger
// before deriving current counts. A candidate is counted exactly once.
func Summarize(store *ledger.Store, generationPath string) (Summary, error) {
	result := Summary{SchemaVersion: SummarySchemaVersion, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	generation, err := candidates.OpenGeneration(store, generationPath)
	if err != nil {
		return result, err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return result, err
	}
	items, err := generation.All()
	if err != nil {
		return result, err
	}
	if err := verifyCandidateEvidence(store, candidateMap(items)); err != nil {
		return result, err
	}
	state, err := replayVerified(store)
	if err != nil {
		return result, err
	}
	result.Generation = generation.Name
	result.Total = len(items)
	for _, candidate := range items {
		status := initialStatus(candidate)
		key := candidateKey(generation.Manifest.CandidatesSHA256, candidate.CandidateID, candidate.ContentSHA256)
		if reviewed, exists := state.statuses[key]; exists {
			status = reviewed
		}
		switch status {
		case StatusPending:
			result.Pending++
		case StatusValidated:
			result.Validated++
		case StatusRejected:
			result.Rejected++
		case StatusQuarantined:
			result.Quarantined++
		}
	}
	return result, nil
}

func candidateMap(items []candidates.Candidate) map[string]candidates.Candidate {
	result := make(map[string]candidates.Candidate, len(items))
	for _, item := range items {
		result[item.CandidateID] = item
	}
	return result
}
