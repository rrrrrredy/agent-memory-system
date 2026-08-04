package candidates

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

	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const defaultShardCount = 64

type episodeSource struct {
	generationPath string
	generationName string
	manifest       episodes.Manifest
	manifestSHA256 string
	episodesPath   string
}

type compressedJSONLWriter struct {
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
		return result, errors.New("candidate shard count must be a power of two between 1 and 256")
	}
	if strings.TrimSpace(options.EpisodeGenerationPath) == "" {
		return result, errors.New("episode generation path is required")
	}

	source, err := openEpisodeSource(store.Root(), options.EpisodeGenerationPath)
	if err != nil {
		return result, err
	}
	result.SourceEpisodeGeneration = source.generationName
	result.SourceEpisodeManifestSHA256 = source.manifestSHA256
	result.SourceEpisodesSHA256 = source.manifest.EpisodesSHA256

	workRoot := filepath.Join(store.Root(), "state")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		return result, fmt.Errorf("create candidate work root: %w", err)
	}
	workDirectory, err := os.MkdirTemp(workRoot, "candidate-derive-")
	if err != nil {
		return result, fmt.Errorf("create candidate work directory: %w", err)
	}
	defer os.RemoveAll(workDirectory)

	observationShards := make([]*compressedJSONLWriter, shardCount)
	actualEpisodesDigest, visitErr := visitEpisodes(source.episodesPath, func(episode episodes.Episode) error {
		if episode.SchemaVersion != episodes.EpisodeSchemaVersion || episode.Privacy != "local_only" {
			return fmt.Errorf("episode %q has an unsupported schema or privacy classification", episode.EpisodeID)
		}
		result.SourceEpisodes++
		for _, observation := range deriveEpisodeObservations(episode) {
			index, err := shardForHex(observation.SemanticKeySHA256, shardCount)
			if err != nil {
				return err
			}
			writer, err := ensureCompressedWriter(
				observationShards, index, filepath.Join(workDirectory,
					fmt.Sprintf("observations-%03d.jsonl.gz", index)),
			)
			if err != nil {
				return err
			}
			if err := writer.encoder.Encode(observation); err != nil {
				return fmt.Errorf("stage candidate observation: %w", err)
			}
			result.Observations++
		}
		return nil
	})
	closeErr := closeCompressedWriters(observationShards)
	if visitErr != nil {
		return result, visitErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	if actualEpisodesDigest != source.manifest.EpisodesSHA256 ||
		result.SourceEpisodes != source.manifest.Episodes {
		return result, errors.New("episode generation failed source verification")
	}

	generationsRoot := filepath.Join(store.Root(), "derived", "generations")
	if err := os.MkdirAll(generationsRoot, 0o700); err != nil {
		return result, fmt.Errorf("create generations root: %w", err)
	}
	generationName := strings.ReplaceAll(DerivationVersion, "/", "-") + "-" +
		source.manifest.EpisodesSHA256
	finalPath := filepath.Join(generationsRoot, generationName)
	if existing, ok, err := loadExistingGeneration(finalPath, source, result); err != nil {
		return result, err
	} else if ok {
		existing.GenerationPath = finalPath
		existing.Reused = true
		return existing, nil
	}

	temporaryGeneration, err := os.MkdirTemp(generationsRoot, ".candidate-generation-")
	if err != nil {
		return result, fmt.Errorf("create temporary candidate generation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporaryGeneration)
		}
	}()

	finalShards := make([]*compressedJSONLWriter, shardCount)
	for index := 0; index < shardCount; index++ {
		if observationShards[index] == nil {
			continue
		}
		records, err := readObservations(filepath.Join(workDirectory,
			fmt.Sprintf("observations-%03d.jsonl.gz", index)))
		if err != nil {
			_ = closeCompressedWriters(finalShards)
			return result, err
		}
		derived, conflicts, err := aggregateCandidates(records)
		if err != nil {
			_ = closeCompressedWriters(finalShards)
			return result, err
		}
		result.ConflictGroups += conflicts
		for _, candidate := range derived {
			finalIndex, err := candidateShard(candidate.CandidateID, shardCount)
			if err != nil {
				_ = closeCompressedWriters(finalShards)
				return result, err
			}
			writer, err := ensureCompressedWriter(
				finalShards, finalIndex, filepath.Join(workDirectory,
					fmt.Sprintf("candidates-%03d.jsonl.gz", finalIndex)),
			)
			if err != nil {
				_ = closeCompressedWriters(finalShards)
				return result, err
			}
			if err := writer.encoder.Encode(candidate); err != nil {
				_ = closeCompressedWriters(finalShards)
				return result, fmt.Errorf("stage finalized candidate: %w", err)
			}
		}
	}
	if err := closeCompressedWriters(finalShards); err != nil {
		return result, err
	}

	output, err := newHashedJSONLWriter(filepath.Join(temporaryGeneration, "candidates.jsonl"))
	if err != nil {
		return result, err
	}
	for index := 0; index < shardCount; index++ {
		if finalShards[index] == nil {
			continue
		}
		candidates, err := readCandidates(filepath.Join(workDirectory,
			fmt.Sprintf("candidates-%03d.jsonl.gz", index)))
		if err != nil {
			_ = output.close()
			return result, err
		}
		sort.Slice(candidates, func(left, right int) bool {
			return candidates[left].CandidateID < candidates[right].CandidateID
		})
		for _, candidate := range candidates {
			if err := output.encoder.Encode(candidate); err != nil {
				_ = output.close()
				return result, fmt.Errorf("write candidate: %w", err)
			}
			result.Candidates++
			switch candidate.Validation.Status {
			case StatusReviewReady:
				result.ReviewReady++
			case StatusUntrusted:
				result.Untrusted++
			case StatusQuarantined:
				result.Quarantined++
			}
		}
	}
	candidatesDigest, err := output.closeDigest()
	if err != nil {
		return result, err
	}
	result.CandidatesSHA256 = candidatesDigest
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, DerivationVersion: DerivationVersion,
		Privacy: "local_only", SourceEpisodeGeneration: source.generationName,
		SourceEpisodeManifestSHA256: source.manifestSHA256,
		SourceEpisodesSHA256:        source.manifest.EpisodesSHA256,
		SourceEpisodes:              result.SourceEpisodes, Observations: result.Observations,
		Candidates: result.Candidates, ReviewReady: result.ReviewReady,
		Untrusted: result.Untrusted, Quarantined: result.Quarantined,
		ConflictGroups: result.ConflictGroups,
		CandidatesFile: "candidates.jsonl", CandidatesSHA256: candidatesDigest,
	}
	if err := writeManifest(filepath.Join(temporaryGeneration, "manifest.json"), manifest); err != nil {
		return result, err
	}
	if err := os.Rename(temporaryGeneration, finalPath); err != nil {
		if _, statErr := os.Stat(finalPath); statErr == nil {
			existing, ok, verifyErr := loadExistingGeneration(finalPath, source, result)
			if verifyErr != nil {
				return result, verifyErr
			}
			if ok {
				existing.GenerationPath = finalPath
				existing.Reused = true
				return existing, nil
			}
		}
		return result, fmt.Errorf("commit candidate generation: %w", err)
	}
	committed = true
	result.GenerationPath = finalPath
	return result, nil
}

func openEpisodeSource(storeRoot, supplied string) (episodeSource, error) {
	result := episodeSource{}
	generationsRoot := filepath.Join(storeRoot, "derived", "generations")
	resolvedRoot, err := filepath.EvalSymlinks(generationsRoot)
	if err != nil {
		return result, fmt.Errorf("resolve generations root: %w", err)
	}
	target := supplied
	if !filepath.IsAbs(target) {
		if filepath.Base(target) != target {
			return result, errors.New("relative episode generation must be a directory name")
		}
		target = filepath.Join(generationsRoot, target)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return result, fmt.Errorf("resolve episode generation: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || relative == "." || filepath.Dir(relative) != "." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return result, errors.New("episode generation must be a direct child of the local generations root")
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil || !info.IsDir() {
		return result, errors.New("episode generation is not a directory")
	}
	manifestPath := filepath.Join(resolvedTarget, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return result, fmt.Errorf("read episode manifest: %w", err)
	}
	var manifest episodes.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return result, fmt.Errorf("decode episode manifest: %w", err)
	}
	if manifest.SchemaVersion != episodes.ManifestSchemaVersion ||
		manifest.DerivationVersion != episodes.DerivationVersion ||
		manifest.Privacy != "local_only" || manifest.EpisodesFile != "episodes.jsonl" ||
		len(manifest.EpisodesSHA256) != sha256.Size*2 {
		return result, errors.New("episode generation manifest is incompatible")
	}
	manifestDigest := sha256.Sum256(manifestData)
	return episodeSource{
		generationPath: resolvedTarget, generationName: filepath.Base(resolvedTarget),
		manifest: manifest, manifestSHA256: hex.EncodeToString(manifestDigest[:]),
		episodesPath: filepath.Join(resolvedTarget, "episodes.jsonl"),
	}, nil
}

func visitEpisodes(path string, visitor func(episodes.Episode) error) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open episodes: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, hasher))
	for {
		var episode episodes.Episode
		if err := decoder.Decode(&episode); errors.Is(err, io.EOF) {
			return hex.EncodeToString(hasher.Sum(nil)), nil
		} else if err != nil {
			return "", fmt.Errorf("decode episode: %w", err)
		}
		if err := visitor(episode); err != nil {
			return "", err
		}
	}
}

func ensureCompressedWriter(
	writers []*compressedJSONLWriter, index int, path string,
) (*compressedJSONLWriter, error) {
	if writers[index] != nil {
		return writers[index], nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create candidate shard: %w", err)
	}
	compressed := gzip.NewWriter(file)
	encoder := json.NewEncoder(compressed)
	encoder.SetEscapeHTML(false)
	writer := &compressedJSONLWriter{file: file, gzip: compressed, encoder: encoder}
	writers[index] = writer
	return writer, nil
}

func closeCompressedWriters(writers []*compressedJSONLWriter) error {
	var closeErrors []error
	for index, writer := range writers {
		if writer == nil || writer.file == nil {
			continue
		}
		if err := writer.gzip.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close candidate shard %d compressor: %w", index, err))
		}
		if err := writer.file.Sync(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("sync candidate shard %d: %w", index, err))
		}
		if err := writer.file.Close(); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close candidate shard %d: %w", index, err))
		}
		writer.file = nil
	}
	return errors.Join(closeErrors...)
}

func readObservations(path string) ([]stagedObservation, error) {
	result := []stagedObservation{}
	if err := readCompressed(path, func(decoder *json.Decoder) error {
		var observation stagedObservation
		if err := decoder.Decode(&observation); err != nil {
			return err
		}
		result = append(result, observation)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read candidate observations: %w", err)
	}
	return result, nil
}

func readCandidates(path string) ([]Candidate, error) {
	result := []Candidate{}
	if err := readCompressed(path, func(decoder *json.Decoder) error {
		var candidate Candidate
		if err := decoder.Decode(&candidate); err != nil {
			return err
		}
		result = append(result, candidate)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read finalized candidates: %w", err)
	}
	return result, nil
}

func readCompressed(path string, readOne func(*json.Decoder) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	decoder := json.NewDecoder(compressed)
	for {
		err := readOne(decoder)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func shardForHex(value string, shardCount int) (int, error) {
	digest, err := hex.DecodeString(value)
	if err != nil || len(digest) != sha256.Size {
		return 0, errors.New("invalid candidate shard digest")
	}
	if shardCount == 1 {
		return 0, nil
	}
	shardBits := bits.Len(uint(shardCount)) - 1
	return int(digest[0] >> (8 - shardBits)), nil
}

func candidateShard(candidateID string, shardCount int) (int, error) {
	return shardForHex(strings.TrimPrefix(candidateID, "candidate-"), shardCount)
}

func newHashedJSONLWriter(path string) (*hashedJSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create candidate JSONL: %w", err)
	}
	hasher := sha256.New()
	buffer := bufio.NewWriterSize(io.MultiWriter(file, hasher), 256*1024)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	return &hashedJSONLWriter{file: file, buffer: buffer, hasher: hasher, encoder: encoder}, nil
}

func (writer *hashedJSONLWriter) close() error {
	_, err := writer.closeDigest()
	return err
}

func (writer *hashedJSONLWriter) closeDigest() (string, error) {
	if writer.file == nil {
		return "", nil
	}
	if err := writer.buffer.Flush(); err != nil {
		_ = writer.file.Close()
		writer.file = nil
		return "", fmt.Errorf("flush candidate JSONL: %w", err)
	}
	if err := writer.file.Sync(); err != nil {
		_ = writer.file.Close()
		writer.file = nil
		return "", fmt.Errorf("sync candidate JSONL: %w", err)
	}
	if err := writer.file.Close(); err != nil {
		writer.file = nil
		return "", fmt.Errorf("close candidate JSONL: %w", err)
	}
	writer.file = nil
	return hex.EncodeToString(writer.hasher.Sum(nil)), nil
}

func writeManifest(path string, manifest Manifest) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create candidate manifest: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = file.Close()
		return fmt.Errorf("write candidate manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync candidate manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close candidate manifest: %w", err)
	}
	return nil
}

func loadExistingGeneration(
	path string, source episodeSource, current BuildResult,
) (BuildResult, bool, error) {
	result := BuildResult{SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion}
	data, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, fmt.Errorf("read existing candidate manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return result, false, fmt.Errorf("decode existing candidate manifest: %w", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion ||
		manifest.DerivationVersion != DerivationVersion || manifest.Privacy != "local_only" ||
		manifest.SourceEpisodeGeneration != source.generationName ||
		manifest.SourceEpisodeManifestSHA256 != source.manifestSHA256 ||
		manifest.SourceEpisodesSHA256 != source.manifest.EpisodesSHA256 ||
		manifest.SourceEpisodes != current.SourceEpisodes || manifest.Observations != current.Observations ||
		manifest.CandidatesFile != "candidates.jsonl" {
		return result, false, errors.New("existing candidate generation does not match its episode source")
	}
	digest, err := hashFile(filepath.Join(path, "candidates.jsonl"))
	if err != nil {
		return result, false, err
	}
	if digest != manifest.CandidatesSHA256 {
		return result, false, errors.New("existing candidate generation failed content verification")
	}
	return BuildResult{
		SchemaVersion: BuildSchemaVersion, DerivationVersion: DerivationVersion,
		SourceEpisodeGeneration:     manifest.SourceEpisodeGeneration,
		SourceEpisodeManifestSHA256: manifest.SourceEpisodeManifestSHA256,
		SourceEpisodesSHA256:        manifest.SourceEpisodesSHA256,
		SourceEpisodes:              manifest.SourceEpisodes, Observations: manifest.Observations,
		Candidates: manifest.Candidates, ReviewReady: manifest.ReviewReady,
		Untrusted: manifest.Untrusted, Quarantined: manifest.Quarantined,
		ConflictGroups: manifest.ConflictGroups, CandidatesSHA256: manifest.CandidatesSHA256,
	}, true, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open candidate source: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash candidate source: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func candidateCollisionError(candidateID string) error {
	return fmt.Errorf("candidate identity collision for %q", candidateID)
}
