package candidates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const ListSchemaVersion = "candidate-list/v1alpha1"

type ListOptions struct {
	Statuses []ReviewStatus
	Limit    int
}
type ListItem struct {
	Candidate  Candidate `json:"candidate"`
	TextSHA256 string    `json:"text_sha256"`
}

type ListResult struct {
	SchemaVersion string     `json:"schema_version"`
	Generation    string     `json:"generation"`
	Total         int        `json:"total"`
	Matching      int        `json:"matching"`
	Returned      int        `json:"returned"`
	Truncated     bool       `json:"truncated"`
	Candidates    []ListItem `json:"candidates"`
	Privacy       string     `json:"privacy"`
}

func (generation Generation) List(options ListOptions) (ListResult, error) {
	result := ListResult{SchemaVersion: ListSchemaVersion, Generation: generation.Name,
		Candidates: []ListItem{}, Privacy: "local_only"}
	if options.Limit == 0 {
		options.Limit = 50
	}
	if options.Limit < 1 || options.Limit > 1000 {
		return result, errors.New("candidate list limit must be between 1 and 1000")
	}
	statuses := map[ReviewStatus]struct{}{}
	for _, status := range options.Statuses {
		switch status {
		case StatusReviewReady, StatusUntrusted, StatusQuarantined:
			statuses[status] = struct{}{}
		default:
			return result, fmt.Errorf("unsupported candidate derivation status %q", status)
		}
	}
	path := filepath.Join(generation.Path, "candidates.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open candidate generation: %w", err)
	}
	defer file.Close()
	ids := []string{}
	decoder := json.NewDecoder(file)
	for {
		var candidate Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return result, fmt.Errorf("decode candidate list: %w", err)
		}
		result.Total++
		if len(statuses) != 0 {
			if _, wanted := statuses[candidate.Validation.Status]; !wanted {
				continue
			}
		}
		result.Matching++
		if len(ids) < options.Limit {
			ids = append(ids, candidate.CandidateID)
		}
	}
	selection, err := generation.Select(ids)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		candidate := selection.Candidates[id]
		digest := sha256.Sum256([]byte(candidate.Text))
		result.Candidates = append(result.Candidates, ListItem{
			Candidate: candidate, TextSHA256: hex.EncodeToString(digest[:]),
		})
	}
	result.Returned = len(result.Candidates)
	result.Truncated = result.Returned < result.Matching
	return result, nil
}
