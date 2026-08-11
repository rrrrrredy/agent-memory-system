package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	defaultLegacyReviewSample = 20
	maximumLegacyReviewSample = 200
	maximumCheckpointChecks   = 20

	CandidateStratumExplicitRemember     = "explicit_remember"
	CandidateStratumUserCorrection       = "user_correction"
	CandidateStratumStableRepetition     = "stable_repetition"
	CandidateStratumCompactionCorrection = "compaction_correction"
	CandidateStratumSemanticConflict     = "semantic_conflict"
	CandidateStratumUntrustedInstruction = "untrusted_instruction"

	CompactionStratumDriftEvidence        = "drift_evidence"
	CompactionStratumAtRisk               = "at_risk"
	CompactionStratumPreserved            = "preserved"
	CompactionStratumInsufficientEvidence = "insufficient_evidence"
)

var candidateReviewStrata = []string{
	CandidateStratumExplicitRemember,
	CandidateStratumUserCorrection,
	CandidateStratumStableRepetition,
	CandidateStratumCompactionCorrection,
	CandidateStratumSemanticConflict,
	CandidateStratumUntrustedInstruction,
}

var compactionReviewStrata = []string{
	CompactionStratumDriftEvidence,
	CompactionStratumAtRisk,
	CompactionStratumPreserved,
	CompactionStratumInsufficientEvidence,
}

type rankedCandidate struct {
	ID   string
	Rank string
}

type rankedCompaction struct {
	Sample CompactionReviewSample
}

// PrepareLegacyReviewPack builds a deterministic, local-only human review pack
// from candidates and compaction checkpoints that overlap a frozen legacy corpus.
// It never labels, promotes, exports, or appends evidence.
func PrepareLegacyReviewPack(
	store *ledger.Store, corpusID, candidateGeneration string, options LegacyReviewPackOptions,
) (LegacyReviewPackResult, error) {
	result := LegacyReviewPackResult{SchemaVersion: LegacyReviewPackResultSchema, Privacy: "local_only"}
	if store == nil {
		return result, errors.New("store is required")
	}
	sampleLimit := options.SamplePerStratum
	if sampleLimit == 0 {
		sampleLimit = defaultLegacyReviewSample
	}
	if sampleLimit < 1 || sampleLimit > maximumLegacyReviewSample {
		return result, fmt.Errorf("sample_per_stratum must be between 1 and %d", maximumLegacyReviewSample)
	}
	verification := VerifyCorpus(store, corpusID)
	if len(verification.Issues) != 0 {
		return result, fmt.Errorf("legacy corpus verification failed: %s", strings.Join(verification.Issues, "; "))
	}
	corpus, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		return result, fmt.Errorf("load legacy corpus: %w", err)
	}
	generation, err := candidates.OpenGeneration(store, candidateGeneration)
	if err != nil {
		return result, err
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return result, fmt.Errorf("candidate generation is not current: %w", err)
	}
	prefix, err := generation.SourceEvidencePrefix()
	if err != nil {
		return result, err
	}
	candidateManifestSHA, err := hashReviewPackFile(filepath.Join(generation.Path, "manifest.json"))
	if err != nil {
		return result, err
	}

	corpusSessions := corpusSessionSet(corpus)
	episodePath := filepath.Join(
		store.Root(), "derived", "generations", generation.Manifest.SourceEpisodeGeneration, "episodes.jsonl",
	)
	corpusEpisodes, compactionPopulation, compactionSamples, err := scanCorpusEpisodes(
		episodePath, generation.Manifest, corpusSessions, corpus.CorpusContentSHA256, generation.Name, sampleLimit,
	)
	if err != nil {
		return result, err
	}
	candidatePopulation, selectedByStratum, err := scanReviewCandidates(
		filepath.Join(generation.Path, "candidates.jsonl"), corpusEpisodes,
		corpus.CorpusContentSHA256, generation.Name, sampleLimit,
	)
	if err != nil {
		return result, err
	}
	selectedIDs := uniqueRankedCandidateIDs(selectedByStratum)
	selection, err := generation.Select(selectedIDs)
	if err != nil {
		return result, fmt.Errorf("verify selected candidate provenance: %w", err)
	}
	candidateSamples := materializeCandidateSamples(selectedByStratum, selection.Candidates, corpusEpisodes)

	pack := LegacyReviewPack{
		SchemaVersion: LegacyReviewPackSchema, CorpusID: corpus.CorpusID,
		CorpusContentSHA256: corpus.CorpusContentSHA256,
		CandidateGeneration: generation.Name, CandidateManifestSHA256: candidateManifestSHA,
		CandidatesSHA256:     generation.Manifest.CandidatesSHA256,
		EpisodeGeneration:    generation.Manifest.SourceEpisodeGeneration,
		EpisodesSHA256:       generation.Manifest.SourceEpisodesSHA256,
		SourceEvidencePrefix: prefix, SamplePerStratum: sampleLimit, CorpusCounts: corpus.Counts,
		CandidatePopulation: candidatePopulation, CompactionPopulation: compactionPopulation,
		CandidateSamples: candidateSamples, CompactionSamples: compactionSamples, Privacy: "local_only",
	}
	identityBytes, err := json.Marshal(pack)
	if err != nil {
		return result, fmt.Errorf("encode review pack identity: %w", err)
	}
	pack.PackID = "review-pack-" + sha256Hex(identityBytes)
	packBytes, err := marshalIndented(pack)
	if err != nil {
		return result, fmt.Errorf("encode review pack: %w", err)
	}
	packPath, reused, err := writeLegacyReviewPack(store.Root(), pack.PackID, packBytes)
	if err != nil {
		return result, err
	}

	result.PackID = pack.PackID
	result.PackPath = packPath
	result.PackSHA256 = sha256Hex(packBytes)
	result.CandidateSamples = countCandidateSamples(candidateSamples)
	result.UniqueCandidates = len(selectedIDs)
	result.CompactionSamples = countCompactionSamples(compactionSamples)
	result.CandidatePopulation = candidatePopulation
	result.CompactionPopulation = compactionPopulation
	result.Reused = reused
	return result, nil
}

func corpusSessionSet(corpus CorpusManifest) map[string]struct{} {
	result := map[string]struct{}{}
	for _, rollout := range corpus.Rollouts {
		for _, sessionID := range rollout.SessionIDs {
			if strings.TrimSpace(sessionID) != "" {
				result[sessionID] = struct{}{}
			}
		}
	}
	return result
}

func scanCorpusEpisodes(
	path string, manifest candidates.Manifest, corpusSessions map[string]struct{}, corpusHash, generation string,
	limit int,
) (map[string]struct{}, map[string]int, map[string][]CompactionReviewSample, error) {
	population := zeroPopulation(compactionReviewStrata)
	ranked := make(map[string][]rankedCompaction, len(compactionReviewStrata))
	for _, stratum := range compactionReviewStrata {
		ranked[stratum] = []rankedCompaction{}
	}
	corpusEpisodes := map[string]struct{}{}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open source episodes: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, hasher))
	count := 0
	previousID := ""
	for {
		var episode episodes.Episode
		if err := decoder.Decode(&episode); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, nil, fmt.Errorf("decode source episode %d: %w", count+1, err)
		}
		count++
		if episode.SchemaVersion != episodes.EpisodeSchemaVersion || episode.Privacy != "local_only" ||
			strings.TrimSpace(episode.EpisodeID) == "" || (previousID != "" && episode.EpisodeID <= previousID) {
			return nil, nil, nil, fmt.Errorf("source episode %d is incompatible or unsorted", count)
		}
		previousID = episode.EpisodeID
		if episode.Agent != ledger.AgentCodex {
			continue
		}
		if _, belongs := corpusSessions[episode.ThreadID]; !belongs {
			continue
		}
		corpusEpisodes[episode.EpisodeID] = struct{}{}
		statements := make(map[string]episodes.Statement, len(episode.Statements))
		for _, statement := range episode.Statements {
			statements[statement.StatementID] = statement
		}
		for _, checkpoint := range episode.Compactions {
			stratum, ok := compactionStratum(checkpoint.Status)
			if !ok {
				continue
			}
			for _, check := range checkpoint.Checks {
				if _, exists := statements[check.StatementID]; !exists {
					return nil, nil, nil, fmt.Errorf(
						"checkpoint %q references an unavailable statement", checkpoint.CheckpointID,
					)
				}
			}
			population[stratum]++
			sample := projectCompactionSample(
				corpusHash, generation, stratum, episode.EpisodeID, episode.Agent, checkpoint, statements,
			)
			ranked[stratum] = keepLowestCompactions(ranked[stratum], rankedCompaction{Sample: sample}, limit)
		}
	}
	if count != manifest.SourceEpisodes || hex.EncodeToString(hasher.Sum(nil)) != manifest.SourceEpisodesSHA256 {
		return nil, nil, nil, errors.New("source episodes do not match the candidate manifest")
	}
	samples := make(map[string][]CompactionReviewSample, len(compactionReviewStrata))
	for _, stratum := range compactionReviewStrata {
		items := ranked[stratum]
		sort.Slice(items, func(left, right int) bool {
			return compactionLess(items[left], items[right])
		})
		for _, item := range items {
			samples[stratum] = append(samples[stratum], item.Sample)
		}
		if samples[stratum] == nil {
			samples[stratum] = []CompactionReviewSample{}
		}
	}
	return corpusEpisodes, population, samples, nil
}

func scanReviewCandidates(
	path string, corpusEpisodes map[string]struct{}, corpusHash, generation string, limit int,
) (map[string]int, map[string][]rankedCandidate, error) {
	population := zeroPopulation(candidateReviewStrata)
	ranked := make(map[string][]rankedCandidate, len(candidateReviewStrata))
	for _, stratum := range candidateReviewStrata {
		ranked[stratum] = []rankedCandidate{}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open review candidates: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for index := 1; ; index++ {
		var candidate candidates.Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("decode review candidate %d: %w", index, err)
		}
		corpusEpisodeIDs := candidateCorpusEpisodeIDs(candidate, corpusEpisodes)
		if len(corpusEpisodeIDs) == 0 {
			continue
		}
		for _, stratum := range candidateStrata(candidate, corpusEpisodeIDs) {
			population[stratum]++
			item := rankedCandidate{
				ID:   candidate.CandidateID,
				Rank: selectionRank(corpusHash, generation, stratum, candidate.CandidateID),
			}
			ranked[stratum] = keepLowestCandidates(ranked[stratum], item, limit)
		}
	}
	for _, stratum := range candidateReviewStrata {
		sort.Slice(ranked[stratum], func(left, right int) bool {
			return candidateRankLess(ranked[stratum][left], ranked[stratum][right])
		})
	}
	return population, ranked, nil
}

func candidateStrata(candidate candidates.Candidate, corpusEpisodeIDs []string) []string {
	corpusEpisodeSet := make(map[string]struct{}, len(corpusEpisodeIDs))
	for _, episodeID := range corpusEpisodeIDs {
		corpusEpisodeSet[episodeID] = struct{}{}
	}
	support := map[candidates.SupportType]bool{}
	for _, observation := range candidate.Observations {
		if _, belongs := corpusEpisodeSet[observation.EpisodeID]; !belongs {
			continue
		}
		for _, item := range observation.SupportTypes {
			support[item] = true
		}
	}
	result := []string{}
	if support[candidates.SupportExplicitRemember] {
		result = append(result, CandidateStratumExplicitRemember)
	}
	if support[candidates.SupportUserCorrection] {
		result = append(result, CandidateStratumUserCorrection)
	}
	if len(corpusEpisodeIDs) >= 2 {
		result = append(result, CandidateStratumStableRepetition)
	}
	if support[candidates.SupportCompactionDrift] {
		result = append(result, CandidateStratumCompactionCorrection)
	}
	if candidate.Validation.Status == candidates.StatusQuarantined && candidate.ConflictGroupID != "" {
		result = append(result, CandidateStratumSemanticConflict)
	}
	if candidate.Validation.Status == candidates.StatusUntrusted && support[candidates.SupportUserInstruction] {
		result = append(result, CandidateStratumUntrustedInstruction)
	}
	return result
}

func candidateCorpusEpisodeIDs(candidate candidates.Candidate, corpusEpisodes map[string]struct{}) []string {
	seen := map[string]struct{}{}
	for _, observation := range candidate.Observations {
		if _, belongs := corpusEpisodes[observation.EpisodeID]; belongs {
			seen[observation.EpisodeID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for episodeID := range seen {
		result = append(result, episodeID)
	}
	sort.Strings(result)
	return result
}

func materializeCandidateSamples(
	ranked map[string][]rankedCandidate, selected map[string]candidates.Candidate,
	corpusEpisodes map[string]struct{},
) map[string][]CandidateReviewSample {
	result := make(map[string][]CandidateReviewSample, len(candidateReviewStrata))
	for _, stratum := range candidateReviewStrata {
		for _, item := range ranked[stratum] {
			candidate := selected[item.ID]
			observations, support := candidateCorpusObservations(candidate, corpusEpisodes)
			result[stratum] = append(result[stratum], CandidateReviewSample{
				SelectionRankSHA256: item.Rank,
				CandidateID:         candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
				SemanticKeySHA256: candidate.SemanticKeySHA256,
				Kind:              candidate.Kind, Text: candidate.Text, Polarity: candidate.Polarity, Scope: candidate.Scope,
				CorpusSupportTypes: support,
				GlobalSupportTypes: append([]candidates.SupportType(nil), candidate.SupportTypes...),
				CorpusObservations: observations, GlobalEpisodeCount: candidate.EpisodeCount,
				GlobalEvidenceEventCount: candidate.EvidenceEventCount,
				FirstSeenAt:              candidate.FirstSeenAt, LastSeenAt: candidate.LastSeenAt,
				Validation: candidate.Validation, ConflictGroupID: candidate.ConflictGroupID,
				ConflictingCandidateIDs:            append([]string(nil), candidate.ConflictingCandidateIDs...),
				RelatedCandidateIDs:                append([]string(nil), candidate.RelatedCandidateIDs...),
				RequiresExplicitRuleChangeApproval: candidate.RequiresExplicitRuleChangeApproval,
				Privacy:                            "local_only",
			})
		}
		if result[stratum] == nil {
			result[stratum] = []CandidateReviewSample{}
		}
	}
	return result
}

func candidateCorpusObservations(
	candidate candidates.Candidate, corpusEpisodes map[string]struct{},
) ([]candidates.Observation, []candidates.SupportType) {
	observations := []candidates.Observation{}
	episodesSeen := map[string]struct{}{}
	supportSet := map[candidates.SupportType]struct{}{}
	for _, observation := range candidate.Observations {
		if _, belongs := corpusEpisodes[observation.EpisodeID]; !belongs {
			continue
		}
		observations = append(observations, observation)
		episodesSeen[observation.EpisodeID] = struct{}{}
		for _, support := range observation.SupportTypes {
			supportSet[support] = struct{}{}
		}
	}
	if len(episodesSeen) >= 2 {
		supportSet[candidates.SupportStableRepetition] = struct{}{}
	}
	support := make([]candidates.SupportType, 0, len(supportSet))
	for item := range supportSet {
		support = append(support, item)
	}
	sort.Slice(support, func(left, right int) bool { return support[left] < support[right] })
	return observations, support
}

func projectCompactionSample(
	corpusHash, generation, stratum, episodeID string, agent ledger.Agent,
	checkpoint episodes.CompactionCheckpoint, statements map[string]episodes.Statement,
) CompactionReviewSample {
	type rankedCheck struct {
		priority int
		review   CompactionCheckReview
	}
	population := map[string]int{
		string(episodes.StatementPreserved):                 0,
		string(episodes.StatementCorrectionAfterCompaction): 0,
		string(episodes.StatementMissingFromRepresentation): 0,
	}
	checks := make([]rankedCheck, 0, len(checkpoint.Checks))
	preferred := preferredCheckStatus(checkpoint.Status)
	for _, check := range checkpoint.Checks {
		population[string(check.Status)]++
		rank := selectionRank(
			corpusHash, generation, "checkpoint-check:"+checkpoint.CheckpointID,
			check.StatementID+"\x00"+string(check.Status),
		)
		var statement *episodes.Statement
		if found, exists := statements[check.StatementID]; exists {
			copy := found
			statement = &copy
		}
		priority := 1
		if check.Status == preferred {
			priority = 0
		}
		checks = append(checks, rankedCheck{priority: priority, review: CompactionCheckReview{
			SelectionRankSHA256: rank, Check: check, Statement: statement,
		}})
	}
	sort.Slice(checks, func(left, right int) bool {
		if checks[left].priority != checks[right].priority {
			return checks[left].priority < checks[right].priority
		}
		if checks[left].review.SelectionRankSHA256 != checks[right].review.SelectionRankSHA256 {
			return checks[left].review.SelectionRankSHA256 < checks[right].review.SelectionRankSHA256
		}
		return checks[left].review.Check.StatementID < checks[right].review.Check.StatementID
	})
	if len(checks) > maximumCheckpointChecks {
		checks = checks[:maximumCheckpointChecks]
	}
	selected := make([]CompactionCheckReview, 0, len(checks))
	for _, item := range checks {
		selected = append(selected, item.review)
	}
	return CompactionReviewSample{
		SelectionRankSHA256: selectionRank(corpusHash, generation, stratum, checkpoint.CheckpointID),
		EpisodeID:           episodeID, Agent: agent, CheckpointID: checkpoint.CheckpointID,
		EventIDs: append([]string(nil), checkpoint.EventIDs...), ObservedAt: checkpoint.ObservedAt,
		RepresentationEventIDs:  append([]string(nil), checkpoint.RepresentationEventIDs...),
		RepresentationAvailable: checkpoint.RepresentationAvailable, Status: checkpoint.Status,
		Issues:      append([]episodes.DerivationIssue(nil), checkpoint.Issues...),
		TotalChecks: len(checkpoint.Checks), CheckPopulation: population, Checks: selected,
		Privacy: "local_only",
	}
}

func preferredCheckStatus(status episodes.ContinuityStatus) episodes.StatementStatus {
	switch status {
	case episodes.ContinuityDriftEvidence:
		return episodes.StatementCorrectionAfterCompaction
	case episodes.ContinuityPreserved:
		return episodes.StatementPreserved
	default:
		return episodes.StatementMissingFromRepresentation
	}
}

func compactionStratum(status episodes.ContinuityStatus) (string, bool) {
	switch status {
	case episodes.ContinuityDriftEvidence:
		return CompactionStratumDriftEvidence, true
	case episodes.ContinuityAtRisk:
		return CompactionStratumAtRisk, true
	case episodes.ContinuityPreserved:
		return CompactionStratumPreserved, true
	case episodes.ContinuityInsufficientEvidence:
		return CompactionStratumInsufficientEvidence, true
	default:
		return "", false
	}
}

func selectionRank(corpusHash, generation, stratum, id string) string {
	return sha256Hex([]byte(strings.Join(
		[]string{"legacy-review-sample", corpusHash, generation, stratum, id}, "\x00",
	)))
}

func keepLowestCandidates(values []rankedCandidate, added rankedCandidate, limit int) []rankedCandidate {
	values = append(values, added)
	sort.Slice(values, func(left, right int) bool { return candidateRankLess(values[left], values[right]) })
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func candidateRankLess(left, right rankedCandidate) bool {
	if left.Rank != right.Rank {
		return left.Rank < right.Rank
	}
	return left.ID < right.ID
}

func keepLowestCompactions(values []rankedCompaction, added rankedCompaction, limit int) []rankedCompaction {
	values = append(values, added)
	sort.Slice(values, func(left, right int) bool { return compactionLess(values[left], values[right]) })
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func compactionLess(left, right rankedCompaction) bool {
	if left.Sample.SelectionRankSHA256 != right.Sample.SelectionRankSHA256 {
		return left.Sample.SelectionRankSHA256 < right.Sample.SelectionRankSHA256
	}
	if left.Sample.EpisodeID != right.Sample.EpisodeID {
		return left.Sample.EpisodeID < right.Sample.EpisodeID
	}
	return left.Sample.CheckpointID < right.Sample.CheckpointID
}

func uniqueRankedCandidateIDs(values map[string][]rankedCandidate) []string {
	seen := map[string]struct{}{}
	for _, stratum := range candidateReviewStrata {
		for _, item := range values[stratum] {
			seen[item.ID] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for candidateID := range seen {
		result = append(result, candidateID)
	}
	sort.Strings(result)
	return result
}

func zeroPopulation(strata []string) map[string]int {
	result := make(map[string]int, len(strata))
	for _, stratum := range strata {
		result[stratum] = 0
	}
	return result
}

func countCandidateSamples(samples map[string][]CandidateReviewSample) int {
	total := 0
	for _, stratum := range candidateReviewStrata {
		total += len(samples[stratum])
	}
	return total
}

func countCompactionSamples(samples map[string][]CompactionReviewSample) int {
	total := 0
	for _, stratum := range compactionReviewStrata {
		total += len(samples[stratum])
	}
	return total
}

func hashReviewPackFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open review pack source: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash review pack source: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeLegacyReviewPack(root, packID string, data []byte) (string, bool, error) {
	base := filepath.Join(root, "derived", "evaluations", "review-packs")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", false, fmt.Errorf("create review pack directory: %w", err)
	}
	destination := filepath.Join(base, packID)
	path := filepath.Join(destination, "pack.json")
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) {
			return "", false, errors.New("existing review pack content does not match its deterministic identity")
		}
		return path, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("inspect existing review pack: %w", err)
	}
	temporary, err := os.MkdirTemp(base, ".review-pack-tmp-")
	if err != nil {
		return "", false, fmt.Errorf("create review pack temp directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	temporaryPath := filepath.Join(temporary, "pack.json")
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, fmt.Errorf("create review pack: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", false, fmt.Errorf("write review pack: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", false, fmt.Errorf("sync review pack: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", false, fmt.Errorf("close review pack: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", false, fmt.Errorf("commit review pack: %w", err)
	}
	return path, false, nil
}
