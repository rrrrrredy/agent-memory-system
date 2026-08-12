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
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type Generation struct {
	Name      string
	Path      string
	Manifest  Manifest
	storeRoot string
}

type Selection struct {
	Candidates     map[string]Candidate
	ConflictGroups map[string][]string
}

type EvidencePrefix struct {
	Records        int
	LastRecordHash string
}

type candidateMetadata struct {
	semanticKey string
	polarity    CandidatePolarity
	groupID     string
	conflicting []string
	related     []string
}

func OpenGeneration(store *ledger.Store, supplied string) (Generation, error) {
	result := Generation{}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(supplied) == "" {
		return result, errors.New("candidate generation path is required")
	}
	root := filepath.Join(store.Root(), "derived", "generations")
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return result, fmt.Errorf("resolve generations root: %w", err)
	}
	target := supplied
	if !filepath.IsAbs(target) {
		if filepath.Base(target) != target {
			return result, errors.New("relative candidate generation must be a directory name")
		}
		target = filepath.Join(root, target)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return result, fmt.Errorf("resolve candidate generation: %w", err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || relative == "." || filepath.Dir(relative) != "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return result, errors.New("candidate generation must be a direct child of the local generations root")
	}
	info, err := os.Stat(resolvedTarget)
	if err != nil || !info.IsDir() {
		return result, errors.New("candidate generation is not a directory")
	}
	data, err := os.ReadFile(filepath.Join(resolvedTarget, "manifest.json"))
	if err != nil {
		return result, fmt.Errorf("read candidate manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return result, fmt.Errorf("decode candidate manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return result, err
	}
	return Generation{
		Name: filepath.Base(resolvedTarget), Path: resolvedTarget, Manifest: manifest,
		storeRoot: store.Root(),
	}, nil
}

// OpenCurrentGeneration resolves the candidate generation bound to the latest
// verified episode-generation audit. It never guesses from directory names or
// modification times.
func OpenCurrentGeneration(store *ledger.Store) (Generation, error) {
	if store == nil {
		return Generation{}, errors.New("store is required")
	}
	audits, err := episodes.ListVerifiedGenerationAudits(store)
	if err != nil {
		return Generation{}, fmt.Errorf("verify episode generation audits: %w", err)
	}
	if len(audits) == 0 {
		return Generation{}, errors.New("no verified episode generation is available; run derive episodes")
	}
	latest := audits[len(audits)-1]
	name := strings.ReplaceAll(DerivationVersion, "/", "-") + "-" + latest.Audit.ManifestSHA256
	generation, err := OpenGeneration(store, name)
	if err != nil {
		return Generation{}, fmt.Errorf("open current candidate generation: %w", err)
	}
	if err := generation.RequireCurrentEvidence(store); err != nil {
		return Generation{}, err
	}
	return generation, nil
}

// All returns every candidate from a fully verified generation in stable ID
// order. It is intended for local status and review accounting, not retrieval.
func (generation Generation) All() ([]Candidate, error) {
	path := filepath.Join(generation.Path, "candidates.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open candidate generation: %w", err)
	}
	defer file.Close()
	ids := make([]string, 0, generation.Manifest.Candidates)
	decoder := json.NewDecoder(file)
	for {
		var candidate Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode candidate generation: %w", err)
		}
		ids = append(ids, candidate.CandidateID)
	}
	selection, err := generation.Select(ids)
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		result = append(result, selection.Candidates[id])
	}
	return result, nil
}

func (generation Generation) SourceEvidencePrefix() (EvidencePrefix, error) {
	source, err := openEpisodeSource(generation.storeRoot, generation.Manifest.SourceEpisodeGeneration)
	if err != nil {
		return EvidencePrefix{}, fmt.Errorf("open candidate episode provenance: %w", err)
	}
	if source.manifestSHA256 != generation.Manifest.SourceEpisodeManifestSHA256 ||
		source.manifest.EpisodesSHA256 != generation.Manifest.SourceEpisodesSHA256 ||
		source.manifest.Episodes != generation.Manifest.SourceEpisodes {
		return EvidencePrefix{}, errors.New("candidate episode provenance does not match the candidate manifest")
	}
	return EvidencePrefix{
		Records: source.manifest.SourceRecords, LastRecordHash: source.manifest.SourceLastRecordHash,
	}, nil
}

func (generation Generation) RequireCurrentEvidence(store *ledger.Store) error {
	if store == nil {
		return errors.New("store is required")
	}
	storeRoot, err := filepath.EvalSymlinks(store.Root())
	if err != nil {
		return fmt.Errorf("resolve evidence store root: %w", err)
	}
	generationRoot, err := filepath.EvalSymlinks(generation.storeRoot)
	if err != nil {
		return fmt.Errorf("resolve candidate store root: %w", err)
	}
	if storeRoot != generationRoot {
		return errors.New("candidate generation belongs to a different evidence store")
	}
	prefix, err := generation.SourceEvidencePrefix()
	if err != nil {
		return err
	}
	report := store.Verify()
	if len(report.Issues) != 0 {
		return fmt.Errorf("evidence ledger verification failed: %s", strings.Join(report.Issues, "; "))
	}
	if prefix.Records == report.RecordsChecked && prefix.LastRecordHash == report.LastRecordHash {
		return nil
	}
	if report.RecordsChecked != prefix.Records+1 {
		return errors.New("candidate generation does not cover the current evidence ledger prefix")
	}
	audits, err := episodes.ListVerifiedGenerationAudits(store)
	if err != nil {
		return fmt.Errorf("verify episode generation audit suffix: %w", err)
	}
	if len(audits) == 0 {
		return errors.New("candidate generation does not cover the current evidence ledger prefix")
	}
	audited := audits[len(audits)-1]
	if audited.LedgerIndex != report.RecordsChecked || audited.Record.RecordHash != report.LastRecordHash ||
		audited.Audit.SourceRecords != prefix.Records ||
		audited.Audit.SourceLastRecordHash != prefix.LastRecordHash ||
		audited.Audit.GenerationName != generation.Manifest.SourceEpisodeGeneration {
		return errors.New("candidate generation does not cover the current evidence ledger prefix")
	}
	return nil
}

func (generation Generation) Select(candidateIDs []string) (Selection, error) {
	wanted := make(map[string]struct{}, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if !validPrefixedHash(candidateID, "candidate-") {
			return Selection{}, fmt.Errorf("invalid candidate id %q", candidateID)
		}
		wanted[candidateID] = struct{}{}
	}
	selection := Selection{
		Candidates: make(map[string]Candidate, len(wanted)), ConflictGroups: map[string][]string{},
	}
	metadata := map[string]candidateMetadata{}
	statusCounts := map[ReviewStatus]int{}
	conflictGroups := map[string]struct{}{}
	previousID := ""
	count := 0
	path := filepath.Join(generation.Path, "candidates.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return Selection{}, fmt.Errorf("open candidate generation: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, hasher))
	for {
		var candidate Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return Selection{}, fmt.Errorf("decode candidate %d: %w", count+1, err)
		}
		if err := validateCandidate(candidate); err != nil {
			return Selection{}, fmt.Errorf("validate candidate %d: %w", count+1, err)
		}
		if previousID != "" && candidate.CandidateID <= previousID {
			return Selection{}, errors.New("candidate generation is not strictly sorted")
		}
		previousID = candidate.CandidateID
		count++
		statusCounts[candidate.Validation.Status]++
		if candidate.ConflictGroupID != "" {
			conflictGroups[candidate.ConflictGroupID] = struct{}{}
			selection.ConflictGroups[candidate.ConflictGroupID] = append(
				selection.ConflictGroups[candidate.ConflictGroupID], candidate.CandidateID,
			)
		}
		metadata[candidate.CandidateID] = candidateMetadata{
			semanticKey: candidate.SemanticKeySHA256, polarity: candidate.Polarity,
			groupID:     candidate.ConflictGroupID,
			conflicting: append([]string(nil), candidate.ConflictingCandidateIDs...),
			related:     append([]string(nil), candidate.RelatedCandidateIDs...),
		}
		if _, exists := wanted[candidate.CandidateID]; exists {
			selection.Candidates[candidate.CandidateID] = candidate
		}
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != generation.Manifest.CandidatesSHA256 || count != generation.Manifest.Candidates ||
		statusCounts[StatusReviewReady] != generation.Manifest.ReviewReady ||
		statusCounts[StatusUntrusted] != generation.Manifest.Untrusted ||
		statusCounts[StatusQuarantined] != generation.Manifest.Quarantined ||
		len(conflictGroups) != generation.Manifest.ConflictGroups {
		return Selection{}, errors.New("candidate generation failed manifest verification")
	}
	if err := validateCandidateLinks(metadata); err != nil {
		return Selection{}, err
	}
	for candidateID := range wanted {
		if _, exists := selection.Candidates[candidateID]; !exists {
			return Selection{}, fmt.Errorf("candidate %q is not present in generation %q", candidateID, generation.Name)
		}
	}
	for groupID := range selection.ConflictGroups {
		sort.Strings(selection.ConflictGroups[groupID])
	}
	if err := generation.verifySelectedProvenance(selection.Candidates); err != nil {
		return Selection{}, err
	}
	return selection, nil
}

func (generation Generation) verifySelectedProvenance(selected map[string]Candidate) error {
	source, err := openEpisodeSource(generation.storeRoot, generation.Manifest.SourceEpisodeGeneration)
	if err != nil {
		return fmt.Errorf("open candidate episode provenance: %w", err)
	}
	if source.manifestSHA256 != generation.Manifest.SourceEpisodeManifestSHA256 ||
		source.manifest.EpisodesSHA256 != generation.Manifest.SourceEpisodesSHA256 ||
		source.manifest.Episodes != generation.Manifest.SourceEpisodes {
		return errors.New("candidate episode provenance does not match the candidate manifest")
	}
	type proof struct {
		observation Observation
		found       bool
	}
	selectedIDs := make(map[string]struct{}, len(selected))
	proofs := map[string]*proof{}
	for candidateID, candidate := range selected {
		selectedIDs[candidateID] = struct{}{}
		for _, observation := range candidate.Observations {
			key := observationProofKey(candidateID, observation)
			if _, exists := proofs[key]; exists {
				return fmt.Errorf("candidate %q repeats an observation proof", candidateID)
			}
			proofs[key] = &proof{observation: observation}
		}
	}
	episodesSeen := 0
	digest, err := visitEpisodes(source.episodesPath, func(episode episodes.Episode) error {
		if episode.SchemaVersion != episodes.EpisodeSchemaVersion || episode.Privacy != "local_only" {
			return fmt.Errorf("episode %q has an unsupported schema or privacy classification", episode.EpisodeID)
		}
		episodesSeen++
		for _, staged := range deriveEpisodeObservations(episode) {
			if _, wanted := selectedIDs[staged.CandidateID]; !wanted {
				continue
			}
			key := observationProofKey(staged.CandidateID, staged.Observation)
			item, exists := proofs[key]
			if !exists {
				return fmt.Errorf("candidate %q omitted a source observation", staged.CandidateID)
			}
			if item.found || !equalObservation(item.observation, staged.Observation) {
				return fmt.Errorf("candidate %q has inconsistent source observation provenance", staged.CandidateID)
			}
			item.found = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if digest != generation.Manifest.SourceEpisodesSHA256 || episodesSeen != generation.Manifest.SourceEpisodes {
		return errors.New("candidate episode provenance failed source verification")
	}
	for key, item := range proofs {
		if !item.found {
			return fmt.Errorf("candidate observation %q is absent from source episodes", key)
		}
	}
	return nil
}

func observationProofKey(candidateID string, observation Observation) string {
	return candidateID + "\x00" + observation.EpisodeID + "\x00" + observation.StatementID
}

func equalObservation(left, right Observation) bool {
	return left.EpisodeID == right.EpisodeID && left.StatementID == right.StatementID &&
		left.StatementKind == right.StatementKind &&
		equalStrings(left.EvidenceEventIDs, right.EvidenceEventIDs) &&
		equalStrings(left.CorrectionEventIDs, right.CorrectionEventIDs) &&
		equalSupport(left.SupportTypes, right.SupportTypes) &&
		left.FirstSeenAt.Equal(right.FirstSeenAt) && left.LastSeenAt.Equal(right.LastSeenAt)
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.DerivationVersion != DerivationVersion ||
		manifest.Privacy != "local_only" || manifest.CandidatesFile != "candidates.jsonl" {
		return errors.New("candidate generation manifest is incompatible")
	}
	if strings.TrimSpace(manifest.SourceEpisodeGeneration) == "" ||
		!validHash(manifest.SourceEpisodeManifestSHA256) || !validHash(manifest.SourceEpisodesSHA256) ||
		!validHash(manifest.CandidatesSHA256) {
		return errors.New("candidate generation manifest has invalid source or content identity")
	}
	if manifest.SourceEpisodes < 0 || manifest.Observations < 0 || manifest.Candidates < 0 ||
		manifest.ReviewReady < 0 || manifest.Untrusted < 0 || manifest.Quarantined < 0 ||
		manifest.ConflictGroups < 0 ||
		manifest.ReviewReady+manifest.Untrusted+manifest.Quarantined != manifest.Candidates {
		return errors.New("candidate generation manifest has invalid counts")
	}
	return nil
}

func validateCandidate(candidate Candidate) error {
	if candidate.SchemaVersion != CandidateSchemaVersion || candidate.Privacy != "local_only" {
		return errors.New("unsupported candidate schema or privacy classification")
	}
	if strings.TrimSpace(candidate.Text) == "" || !validHash(candidate.ContentSHA256) ||
		!validHash(candidate.SemanticKeySHA256) || !validPrefixedHash(candidate.CandidateID, "candidate-") {
		return errors.New("candidate identity is invalid")
	}
	if !validCandidateKind(candidate.Kind) || !validAgent(candidate.Scope.Agent) ||
		candidate.Scope.Status != ScopeUnconfirmed {
		return errors.New("candidate kind or scope is invalid")
	}
	normalized := normalizeText(candidate.Text)
	if candidate.CandidateID != deterministicID(
		"candidate", string(candidate.Kind), string(candidate.Scope.Agent), normalized,
	) || candidate.SemanticKeySHA256 != deterministicHex(
		"candidate-semantic-key", string(candidate.Scope.Agent), semanticCore(normalized),
	) || candidate.Polarity != detectPolarity(normalized) ||
		candidate.RequiresExplicitRuleChangeApproval != requiresRuleChangeApproval(normalized) ||
		candidate.ContentSHA256 != candidateContentSHA256(candidate) {
		return errors.New("candidate derived identity is inconsistent")
	}
	if len(candidate.Observations) == 0 || candidate.FirstSeenAt.IsZero() || candidate.LastSeenAt.IsZero() ||
		candidate.LastSeenAt.Before(candidate.FirstSeenAt) {
		return errors.New("candidate observation range is invalid")
	}
	episodesSeen := map[string]struct{}{}
	eventsSeen := map[string]struct{}{}
	supportSet := map[SupportType]struct{}{}
	firstSeen := candidate.Observations[0].FirstSeenAt
	lastSeen := candidate.Observations[0].LastSeenAt
	for index, observation := range candidate.Observations {
		if index > 0 && !observationLess(candidate.Observations[index-1], observation) {
			return errors.New("candidate observations are not strictly sorted")
		}
		if !validObservation(observation) {
			return errors.New("candidate observation is invalid")
		}
		episodesSeen[observation.EpisodeID] = struct{}{}
		for _, eventID := range observation.EvidenceEventIDs {
			eventsSeen[eventID] = struct{}{}
		}
		for _, eventID := range observation.CorrectionEventIDs {
			eventsSeen[eventID] = struct{}{}
		}
		for _, support := range observation.SupportTypes {
			supportSet[support] = struct{}{}
		}
		if observation.FirstSeenAt.Before(firstSeen) {
			firstSeen = observation.FirstSeenAt
		}
		if observation.LastSeenAt.After(lastSeen) {
			lastSeen = observation.LastSeenAt
		}
	}
	if len(episodesSeen) >= 2 {
		supportSet[SupportStableRepetition] = struct{}{}
	}
	expectedSupport := make([]SupportType, 0, len(supportSet))
	for support := range supportSet {
		expectedSupport = append(expectedSupport, support)
	}
	expectedSupport = sortedSupportTypes(expectedSupport)
	if candidate.EpisodeCount != len(episodesSeen) || candidate.EvidenceEventCount != len(eventsSeen) ||
		!candidate.FirstSeenAt.Equal(firstSeen) || !candidate.LastSeenAt.Equal(lastSeen) ||
		!equalSupport(candidate.SupportTypes, expectedSupport) {
		return errors.New("candidate aggregate counts or support are inconsistent")
	}
	if !sortedUniqueCandidateIDs(candidate.ConflictingCandidateIDs) ||
		!sortedUniqueCandidateIDs(candidate.RelatedCandidateIDs) {
		return errors.New("candidate links are invalid")
	}
	expectedValidation := validationFor(expectedSupport, candidate.RequiresExplicitRuleChangeApproval)
	if len(candidate.ConflictingCandidateIDs) > 0 {
		expectedConflictID := deterministicID(
			"candidate-conflict", string(candidate.Scope.Agent)+"\x00"+semanticCore(normalized),
		)
		if candidate.ConflictGroupID != expectedConflictID {
			return errors.New("candidate conflict group is invalid")
		}
		expectedValidation.Status = StatusQuarantined
		expectedValidation.Reasons = appendUnique(expectedValidation.Reasons, "semantic_conflict")
	} else if candidate.ConflictGroupID != "" {
		return errors.New("candidate has a conflict group without conflicts")
	}
	if candidate.Validation.Status != expectedValidation.Status ||
		candidate.Validation.ScopeConfirmationRequired != expectedValidation.ScopeConfirmationRequired ||
		candidate.Validation.AutomaticPromotionEligible != expectedValidation.AutomaticPromotionEligible ||
		!equalStrings(candidate.Validation.Reasons, expectedValidation.Reasons) {
		return errors.New("candidate validation state is inconsistent")
	}
	return nil
}

func validObservation(observation Observation) bool {
	if !validPrefixedHash(observation.EpisodeID, "episode-") ||
		!validPrefixedHash(observation.StatementID, "statement-") ||
		(observation.StatementKind != "goal" && observation.StatementKind != "constraint" &&
			observation.StatementKind != "correction") || len(observation.EvidenceEventIDs) == 0 ||
		observation.FirstSeenAt.IsZero() || observation.LastSeenAt.IsZero() ||
		observation.LastSeenAt.Before(observation.FirstSeenAt) ||
		!sortedUniqueStrings(observation.EvidenceEventIDs, false) ||
		!sortedUniqueStrings(observation.CorrectionEventIDs, true) ||
		!sortedUniqueSupport(observation.SupportTypes) {
		return false
	}
	return true
}

func validateCandidateLinks(metadata map[string]candidateMetadata) error {
	groups := map[string][]string{}
	for candidateID, item := range metadata {
		groups[item.semanticKey] = append(groups[item.semanticKey], candidateID)
		for _, otherID := range item.conflicting {
			other, exists := metadata[otherID]
			if !exists || other.groupID != item.groupID || other.semanticKey != item.semanticKey ||
				other.polarity == item.polarity || !containsSortedString(other.conflicting, candidateID) {
				return fmt.Errorf("candidate %q has an invalid conflict link", candidateID)
			}
		}
		for _, otherID := range item.related {
			other, exists := metadata[otherID]
			if otherID == candidateID || !exists || other.semanticKey != item.semanticKey ||
				other.polarity != item.polarity ||
				!containsSortedString(other.related, candidateID) {
				return fmt.Errorf("candidate %q has an invalid related link", candidateID)
			}
		}
	}
	for _, candidateIDs := range groups {
		for leftIndex, leftID := range candidateIDs {
			left := metadata[leftID]
			for rightIndex, rightID := range candidateIDs {
				if leftIndex == rightIndex {
					continue
				}
				right := metadata[rightID]
				if left.polarity == right.polarity {
					if !containsSortedString(left.related, rightID) {
						return fmt.Errorf("candidate %q is missing a related link", leftID)
					}
				} else if (left.polarity == PolarityAffirmative || left.polarity == PolarityProhibitive) &&
					(right.polarity == PolarityAffirmative || right.polarity == PolarityProhibitive) {
					if left.groupID == "" || left.groupID != right.groupID ||
						!containsSortedString(left.conflicting, rightID) {
						return fmt.Errorf("candidate %q is missing a conflict link", leftID)
					}
				}
			}
		}
	}
	return nil
}

func validCandidateKind(kind CandidateKind) bool {
	return kind == KindConstraint || kind == KindCorrection || kind == KindDirective
}

func validAgent(agent ledger.Agent) bool {
	return agent == ledger.AgentCodex || agent == ledger.AgentClaudeCode ||
		agent == ledger.AgentOpenCode || agent == ledger.AgentUnknown
}

func validSupport(support SupportType) bool {
	switch support {
	case SupportUserInstruction, SupportUserCorrection, SupportExplicitRemember,
		SupportCompactionDrift, SupportStableRepetition:
		return true
	default:
		return false
	}
}

func sortedUniqueSupport(values []SupportType) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		if !validSupport(value) || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func sortedUniqueCandidateIDs(values []string) bool {
	for index, value := range values {
		if !validPrefixedHash(value, "candidate-") || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func sortedUniqueStrings(values []string, allowEmpty bool) bool {
	if !allowEmpty && len(values) == 0 {
		return false
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func equalSupport(left, right []SupportType) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func containsSortedString(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validPrefixedHash(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validHash(strings.TrimPrefix(value, prefix))
}
