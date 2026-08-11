package episodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

// LoadVerifiedGeneration loads a content-verified derivation for an exact
// ledger prefix. It never rebuilds from a newer ledger tail.
func LoadVerifiedGeneration(store *ledger.Store, sourceRecords int,
	sourceLastHash string) (BuildResult, []Episode, error) {
	result := BuildResult{SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion}
	if store == nil || sourceRecords < 1 || sourceLastHash == "" {
		return result, nil, errors.New("an exact non-empty ledger prefix is required")
	}
	identity := sourceLastHash
	if identity == "" {
		identity = "empty"
	}
	path := filepath.Join(store.Root(), "derived", "generations",
		strings.ReplaceAll(DerivationVersion, "/", "-")+"-"+identity)
	verified, ok, err := loadExistingGeneration(path, sourceRecords, sourceLastHash)
	if err != nil {
		return result, nil, err
	}
	if !ok {
		return result, nil, errors.New("verified episode generation is unavailable for the ledger prefix")
	}
	file, err := os.Open(filepath.Join(path, "episodes.jsonl"))
	if err != nil {
		return result, nil, fmt.Errorf("open verified episode generation: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	episodes := make([]Episode, 0, verified.Episodes)
	checkpointIDs := map[string]struct{}{}
	for {
		var episode Episode
		if err := decoder.Decode(&episode); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return result, nil, fmt.Errorf("decode verified episode generation: %w", err)
		}
		if episode.SchemaVersion != EpisodeSchemaVersion || episode.EpisodeID == "" ||
			episode.Privacy != "local_only" || episode.Agent == ledger.AgentUnknown || episode.ThreadID == "" {
			return result, nil, errors.New("verified episode generation contains an invalid episode")
		}
		for _, checkpoint := range episode.Compactions {
			if checkpoint.CheckpointID == "" || len(checkpoint.EventIDs) == 0 {
				return result, nil, errors.New("verified episode generation contains an invalid compaction checkpoint")
			}
			if _, duplicate := checkpointIDs[checkpoint.CheckpointID]; duplicate {
				return result, nil, errors.New("verified episode generation repeats a compaction checkpoint")
			}
			checkpointIDs[checkpoint.CheckpointID] = struct{}{}
		}
		episodes = append(episodes, episode)
	}
	if len(episodes) != verified.Episodes {
		return result, nil, errors.New("verified episode generation count is inconsistent")
	}
	verified.GenerationPath = path
	verified.Reused = true
	return verified, episodes, nil
}

// ListVerifiedGenerations returns every retained detector generation after
// verifying its manifest and derived files against the exact ledger prefix.
func ListVerifiedGenerations(store *ledger.Store) ([]BuildResult, error) {
	if store == nil {
		return nil, errors.New("store is required")
	}
	root := filepath.Join(store.Root(), "derived", "generations")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []BuildResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read retained episode generations: %w", err)
	}
	results := []BuildResult{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".episode-generation-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "manifest.json"))
		if err != nil {
			return nil, fmt.Errorf("read retained episode generation manifest: %w", err)
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("decode retained episode generation manifest: %w", err)
		}
		result, _, err := LoadVerifiedGeneration(store, manifest.SourceRecords, manifest.SourceLastRecordHash)
		if err != nil || filepath.Base(result.GenerationPath) != entry.Name() {
			return nil, errors.New("retained episode generation failed verification")
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SourceRecords != results[j].SourceRecords {
			return results[i].SourceRecords < results[j].SourceRecords
		}
		return results[i].SourceLastRecordHash < results[j].SourceLastRecordHash
	})
	return results, nil
}
