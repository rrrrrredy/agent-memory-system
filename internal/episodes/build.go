package episodes

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const defaultShardCount = 64

type stagedEvent struct {
	Event ledger.Event `json:"event"`
}

type shardWriter struct {
	file    *os.File
	gzip    *gzip.Writer
	encoder *json.Encoder
}

type hashedJSONLWriter struct {
	file    *os.File
	buffer  *bufio.Writer
	hasher  hash.Hash
	encoder *json.Encoder
}

func Build(store *ledger.Store, options BuildOptions) (BuildResult, error) {
	result := BuildResult{SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion}
	if store == nil {
		return result, errors.New("store is required")
	}
	shardCount := options.ShardCount
	if shardCount == 0 {
		shardCount = defaultShardCount
	}
	if shardCount < 1 || shardCount > 256 || shardCount&(shardCount-1) != 0 {
		return result, errors.New("episode shard count must be a power of two between 1 and 256")
	}
	workRoot := filepath.Join(store.Root(), "state")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return result, fmt.Errorf("create derivation work root: %w", err)
	}
	workDirectory, err := os.MkdirTemp(workRoot, "episode-derive-")
	if err != nil {
		return result, fmt.Errorf("create derivation work directory: %w", err)
	}
	defer os.RemoveAll(workDirectory)

	shards := make([]*shardWriter, shardCount)
	sourceSnapshots := map[string]ledger.Event{}
	closeShards := func() error {
		var closeErrors []error
		for index, writer := range shards {
			if writer == nil {
				continue
			}
			if err := writer.gzip.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close shard %d compressor: %w", index, err))
			}
			if err := writer.file.Sync(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("sync shard %d: %w", index, err))
			}
			if err := writer.file.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close shard %d: %w", index, err))
			}
		}
		return errors.Join(closeErrors...)
	}
	visitErr := store.VisitRecords(func(record ledger.Record) error {
		result.SourceRecords++
		result.SourceLastRecordHash = record.RecordHash
		if record.Event.Kind == ledger.KindSourceSnapshot {
			sourceSnapshots[record.Event.EventID] = record.Event
			if !episodeRelevantSnapshot(record.Event) {
				return nil
			}
		}
		index := episodeShard(record.Event.Source.Agent, record.Event.Source.ThreadID, shardCount)
		writer := shards[index]
		if writer == nil {
			path := filepath.Join(workDirectory, fmt.Sprintf("shard-%03d.jsonl.gz", index))
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return fmt.Errorf("create episode shard: %w", err)
			}
			compressed := gzip.NewWriter(file)
			writer = &shardWriter{file: file, gzip: compressed, encoder: json.NewEncoder(compressed)}
			writer.encoder.SetEscapeHTML(false)
			shards[index] = writer
		}
		if err := writer.encoder.Encode(stagedEvent{Event: record.Event}); err != nil {
			return fmt.Errorf("stage evidence event: %w", err)
		}
		return nil
	})
	closeErr := closeShards()
	if visitErr != nil {
		return result, visitErr
	}
	if closeErr != nil {
		return result, closeErr
	}

	generationsRoot := filepath.Join(store.Root(), "derived", "generations")
	if err := os.MkdirAll(generationsRoot, 0o700); err != nil {
		return result, fmt.Errorf("create generations root: %w", err)
	}
	sourceIdentity := result.SourceLastRecordHash
	if sourceIdentity == "" {
		sourceIdentity = "empty"
	}
	generationName := strings.ReplaceAll(DerivationVersion, "/", "-") + "-" + sourceIdentity
	finalPath := filepath.Join(generationsRoot, generationName)
	if existing, ok, err := loadExistingGeneration(finalPath, result.SourceRecords,
		result.SourceLastRecordHash); err != nil {
		return result, err
	} else if ok {
		existing.GenerationPath = finalPath
		existing.Reused = true
		return existing, nil
	}

	temporaryGeneration, err := os.MkdirTemp(generationsRoot, ".episode-generation-")
	if err != nil {
		return result, fmt.Errorf("create temporary generation: %w", err)
	}
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.RemoveAll(temporaryGeneration)
		}
	}()
	timeline, err := newHashedJSONLWriter(filepath.Join(temporaryGeneration, "timeline.jsonl"))
	if err != nil {
		return result, err
	}
	episodesFile, err := newHashedJSONLWriter(filepath.Join(temporaryGeneration, "episodes.jsonl"))
	if err != nil {
		_ = timeline.close()
		return result, err
	}
	sequence := int64(0)
	for index := 0; index < shardCount; index++ {
		if shards[index] == nil {
			continue
		}
		groups, err := readShard(filepath.Join(workDirectory,
			fmt.Sprintf("shard-%03d.jsonl.gz", index)))
		if err != nil {
			_ = timeline.close()
			_ = episodesFile.close()
			return result, err
		}
		keys := make([]string, 0, len(groups))
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool {
			leftID := episodeIDFromKey(keys[left])
			rightID := episodeIDFromKey(keys[right])
			return leftID < rightID
		})
		for _, key := range keys {
			analysis := analyzeEpisode(store, groups[key], sourceSnapshots)
			if analysis.episode.EpisodeID == "" {
				continue
			}
			if err := episodesFile.encoder.Encode(analysis.episode); err != nil {
				_ = timeline.close()
				_ = episodesFile.close()
				return result, fmt.Errorf("write episode: %w", err)
			}
			result.Episodes++
			for _, checkpoint := range analysis.episode.Compactions {
				result.Compactions++
				switch checkpoint.Status {
				case ContinuityDriftEvidence:
					result.DriftEvidence++
				case ContinuityAtRisk:
					result.AtRisk++
				case ContinuityInsufficientEvidence:
					result.InsufficientEvidence++
				}
			}
			for _, event := range analysis.timelineEvents {
				entry := TimelineEntry{
					SchemaVersion: TimelineSchemaVersion, Sequence: sequence,
					EpisodeID: analysis.episode.EpisodeID, EventID: event.EventID,
					Kind: event.Kind, ObservedAt: event.ObservedAt.UTC(),
					RecordedAt: event.RecordedAt.UTC(), Source: event.Source,
					Completeness: event.Completeness, Causality: event.Causality,
				}
				if event.Reasoning != nil {
					entry.ReasoningVisibility = event.Reasoning.Visibility
				}
				if err := timeline.encoder.Encode(entry); err != nil {
					_ = timeline.close()
					_ = episodesFile.close()
					return result, fmt.Errorf("write timeline entry: %w", err)
				}
				sequence++
			}
		}
	}
	result.TimelineEntries = sequence
	timelineDigest, err := timeline.closeDigest()
	if err != nil {
		_ = episodesFile.close()
		return result, err
	}
	episodesDigest, err := episodesFile.closeDigest()
	if err != nil {
		return result, err
	}
	result.TimelineSHA256 = timelineDigest
	result.EpisodesSHA256 = episodesDigest
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, DerivationVersion: DerivationVersion,
		Privacy: "local_only", SourceRecords: result.SourceRecords,
		SourceLastRecordHash: result.SourceLastRecordHash, Episodes: result.Episodes,
		TimelineEntries: result.TimelineEntries, Compactions: result.Compactions,
		DriftEvidence: result.DriftEvidence, AtRisk: result.AtRisk,
		InsufficientEvidence: result.InsufficientEvidence,
		TimelineFile:         "timeline.jsonl", TimelineSHA256: timelineDigest,
		EpisodesFile: "episodes.jsonl", EpisodesSHA256: episodesDigest,
	}
	if err := writeManifest(filepath.Join(temporaryGeneration, "manifest.json"), manifest); err != nil {
		return result, err
	}
	if err := os.Rename(temporaryGeneration, finalPath); err != nil {
		if _, statErr := os.Stat(finalPath); statErr == nil {
			existing, ok, verifyErr := loadExistingGeneration(finalPath, result.SourceRecords,
				result.SourceLastRecordHash)
			if verifyErr != nil {
				return result, verifyErr
			}
			if ok {
				existing.GenerationPath = finalPath
				existing.Reused = true
				return existing, nil
			}
		}
		return result, fmt.Errorf("commit episode generation: %w", err)
	}
	keepTemporary = true
	result.GenerationPath = finalPath
	return result, nil
}

func episodeRelevantSnapshot(event ledger.Event) bool {
	return event.Source.Adapter == "claude-code-companion-files" &&
		!strings.HasPrefix(event.Source.ThreadID, "global-") &&
		!strings.HasPrefix(event.Source.ThreadID, "unknown-")
}

func episodeShard(agent ledger.Agent, threadID string, shardCount int) int {
	digest := deterministicDigest("episode", string(agent), threadID)
	if shardCount == 1 {
		return 0
	}
	shardBits := bits.Len(uint(shardCount)) - 1
	return int(digest[0] >> (8 - shardBits))
}

func episodeKey(agent ledger.Agent, threadID string) string {
	return string(agent) + "\x00" + threadID
}

func episodeIDFromKey(key string) string {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return deterministicID("episode", key)
	}
	return deterministicID("episode", parts[0], parts[1])
}

func readShard(path string) (map[string][]ledger.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open episode shard: %w", err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open episode shard compressor: %w", err)
	}
	defer compressed.Close()
	decoder := json.NewDecoder(compressed)
	groups := map[string][]ledger.Event{}
	for {
		var staged stagedEvent
		if err := decoder.Decode(&staged); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode episode shard: %w", err)
		}
		key := episodeKey(staged.Event.Source.Agent, staged.Event.Source.ThreadID)
		groups[key] = append(groups[key], staged.Event)
	}
	return groups, nil
}

func newHashedJSONLWriter(path string) (*hashedJSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create derived JSONL: %w", err)
	}
	hasher := sha256.New()
	buffer := bufio.NewWriterSize(io.MultiWriter(file, hasher), 256*1024)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	return &hashedJSONLWriter{file: file, buffer: buffer, hasher: hasher, encoder: encoder}, nil
}

func (w *hashedJSONLWriter) close() error {
	_, err := w.closeDigest()
	return err
}

func (w *hashedJSONLWriter) closeDigest() (string, error) {
	if w.file == nil {
		return "", nil
	}
	if err := w.buffer.Flush(); err != nil {
		_ = w.file.Close()
		w.file = nil
		return "", fmt.Errorf("flush derived JSONL: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		w.file = nil
		return "", fmt.Errorf("sync derived JSONL: %w", err)
	}
	if err := w.file.Close(); err != nil {
		w.file = nil
		return "", fmt.Errorf("close derived JSONL: %w", err)
	}
	w.file = nil
	return hex.EncodeToString(w.hasher.Sum(nil)), nil
}

func writeManifest(path string, manifest Manifest) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create derivation manifest: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = file.Close()
		return fmt.Errorf("write derivation manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync derivation manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close derivation manifest: %w", err)
	}
	return nil
}

func loadExistingGeneration(
	path string, sourceRecords int, sourceLastHash string,
) (BuildResult, bool, error) {
	result := BuildResult{SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion}
	data, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("read existing derivation manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return result, false, fmt.Errorf("decode existing derivation manifest: %w", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion ||
		manifest.DerivationVersion != DerivationVersion ||
		manifest.SourceRecords != sourceRecords ||
		manifest.SourceLastRecordHash != sourceLastHash || manifest.Privacy != "local_only" ||
		manifest.TimelineFile != "timeline.jsonl" || manifest.EpisodesFile != "episodes.jsonl" {
		return result, false, errors.New("existing episode generation does not match its source ledger")
	}
	timelineDigest, err := hashFile(filepath.Join(path, manifest.TimelineFile))
	if err != nil {
		return result, false, err
	}
	episodesDigest, err := hashFile(filepath.Join(path, manifest.EpisodesFile))
	if err != nil {
		return result, false, err
	}
	if timelineDigest != manifest.TimelineSHA256 || episodesDigest != manifest.EpisodesSHA256 {
		return result, false, errors.New("existing episode generation failed content verification")
	}
	return BuildResult{
		SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion,
		SourceRecords: manifest.SourceRecords, SourceLastRecordHash: manifest.SourceLastRecordHash,
		Episodes: manifest.Episodes, TimelineEntries: manifest.TimelineEntries,
		Compactions: manifest.Compactions, DriftEvidence: manifest.DriftEvidence,
		AtRisk: manifest.AtRisk, InsufficientEvidence: manifest.InsufficientEvidence,
		TimelineSHA256: manifest.TimelineSHA256, EpisodesSHA256: manifest.EpisodesSHA256,
	}, true, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open derived file: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash derived file: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
