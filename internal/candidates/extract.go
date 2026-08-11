package candidates

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type stagedObservation struct {
	CandidateID       string            `json:"candidate_id"`
	NormalizedText    string            `json:"normalized_text"`
	SemanticCore      string            `json:"semantic_core"`
	SemanticKeySHA256 string            `json:"semantic_key_sha256"`
	Kind              CandidateKind     `json:"kind"`
	Text              string            `json:"text"`
	Polarity          CandidatePolarity `json:"polarity"`
	Agent             ledger.Agent      `json:"agent"`
	RequiresApproval  bool              `json:"requires_explicit_rule_change_approval"`
	Observation       Observation       `json:"observation"`
}

type candidateAggregate struct {
	base         stagedObservation
	observations []Observation
}

func deriveEpisodeObservations(episode episodes.Episode) []stagedObservation {
	correctionEvidence := map[string][]string{}
	for _, checkpoint := range episode.Compactions {
		if checkpoint.Status != episodes.ContinuityDriftEvidence {
			continue
		}
		for _, check := range checkpoint.Checks {
			if check.Status != episodes.StatementCorrectionAfterCompaction {
				continue
			}
			correctionEvidence[check.StatementID] = appendUnique(
				correctionEvidence[check.StatementID], check.CorrectionEventIDs...,
			)
		}
	}

	result := []stagedObservation{}
	for _, statement := range episode.Statements {
		support := []SupportType{}
		kind := CandidateKind("")
		switch statement.Kind {
		case "constraint":
			kind = KindConstraint
			support = append(support, SupportUserInstruction)
		case "correction":
			kind = KindCorrection
			support = append(support, SupportUserCorrection)
		}
		remember := isExplicitRemember(statement.Text)
		if remember {
			kind = KindDirective
			support = append(support, SupportExplicitRemember)
		}
		corrections := correctionEvidence[statement.StatementID]
		if len(corrections) > 0 {
			if kind == "" {
				kind = KindConstraint
			}
			support = append(support, SupportUserCorrection, SupportCompactionDrift)
		}
		if kind == "" {
			continue
		}

		normalized := normalizeText(statement.Text)
		if normalized == "" {
			continue
		}
		semanticCore := semanticCore(normalized)
		candidateID := deterministicID("candidate", string(kind), string(episode.Agent), normalized)
		semanticKey := deterministicHex("candidate-semantic-key", string(episode.Agent), semanticCore)
		observation := Observation{
			EpisodeID: episode.EpisodeID, StatementID: statement.StatementID,
			StatementKind:      statement.Kind,
			EvidenceEventIDs:   appendUnique(nil, statement.EvidenceEventIDs...),
			CorrectionEventIDs: appendUnique(nil, corrections...),
			SupportTypes:       sortedSupportTypes(support),
			FirstSeenAt:        statement.FirstSeenAt.UTC(), LastSeenAt: statement.LastSeenAt.UTC(),
		}
		result = append(result, stagedObservation{
			CandidateID: candidateID, NormalizedText: normalized,
			SemanticCore: semanticCore, SemanticKeySHA256: semanticKey,
			Kind: kind, Text: statement.Text, Polarity: detectPolarity(normalized),
			Agent: episode.Agent, RequiresApproval: requiresRuleChangeApproval(normalized),
			Observation: observation,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CandidateID != result[right].CandidateID {
			return result[left].CandidateID < result[right].CandidateID
		}
		return observationLess(result[left].Observation, result[right].Observation)
	})
	return result
}

func aggregateCandidates(records []stagedObservation) ([]Candidate, int, error) {
	byID := map[string]*candidateAggregate{}
	for _, record := range records {
		aggregate, exists := byID[record.CandidateID]
		if !exists {
			copyRecord := record
			aggregate = &candidateAggregate{base: copyRecord}
			byID[record.CandidateID] = aggregate
		} else if aggregate.base.NormalizedText != record.NormalizedText ||
			aggregate.base.Kind != record.Kind || aggregate.base.Agent != record.Agent {
			return nil, 0, candidateCollisionError(record.CandidateID)
		}
		if record.Text < aggregate.base.Text {
			aggregate.base.Text = record.Text
		}
		aggregate.base.RequiresApproval = aggregate.base.RequiresApproval || record.RequiresApproval
		aggregate.observations = append(aggregate.observations, record.Observation)
	}

	groups := map[string][]int{}
	candidates := make([]Candidate, 0, len(byID))
	for _, aggregate := range byID {
		candidate := finalizeCandidate(aggregate)
		candidates = append(candidates, candidate)
		groupKey := string(candidate.Scope.Agent) + "\x00" + aggregate.base.SemanticCore
		groups[groupKey] = append(groups[groupKey], len(candidates)-1)
	}

	conflictGroups := 0
	for groupKey, group := range groups {
		if len(group) < 2 {
			continue
		}
		for _, candidateIndex := range group {
			candidate := &candidates[candidateIndex]
			for _, otherIndex := range group {
				other := &candidates[otherIndex]
				if candidate.CandidateID == other.CandidateID {
					continue
				}
				if candidate.Polarity == other.Polarity {
					candidate.RelatedCandidateIDs = appendUnique(
						candidate.RelatedCandidateIDs, other.CandidateID,
					)
				}
			}
		}
		hasAffirmative := false
		hasProhibitive := false
		for _, candidateIndex := range group {
			candidate := &candidates[candidateIndex]
			hasAffirmative = hasAffirmative || candidate.Polarity == PolarityAffirmative
			hasProhibitive = hasProhibitive || candidate.Polarity == PolarityProhibitive
		}
		if !hasAffirmative || !hasProhibitive {
			continue
		}
		conflictGroups++
		conflictID := deterministicID("candidate-conflict", groupKey)
		for _, candidateIndex := range group {
			candidate := &candidates[candidateIndex]
			if candidate.Polarity != PolarityAffirmative && candidate.Polarity != PolarityProhibitive {
				continue
			}
			candidate.ConflictGroupID = conflictID
			for _, otherIndex := range group {
				other := &candidates[otherIndex]
				if other.CandidateID != candidate.CandidateID && other.Polarity != candidate.Polarity &&
					(other.Polarity == PolarityAffirmative || other.Polarity == PolarityProhibitive) {
					candidate.ConflictingCandidateIDs = appendUnique(
						candidate.ConflictingCandidateIDs, other.CandidateID,
					)
				}
			}
			if len(candidate.ConflictingCandidateIDs) > 0 {
				candidate.Validation.Status = StatusQuarantined
				candidate.Validation.Reasons = appendUnique(
					candidate.Validation.Reasons, "semantic_conflict",
				)
			}
		}
	}

	for index := range candidates {
		sort.Strings(candidates[index].ConflictingCandidateIDs)
		sort.Strings(candidates[index].RelatedCandidateIDs)
		sort.Strings(candidates[index].Validation.Reasons)
		candidates[index].ContentSHA256 = candidateContentSHA256(candidates[index])
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].CandidateID < candidates[right].CandidateID
	})
	return candidates, conflictGroups, nil
}

func finalizeCandidate(aggregate *candidateAggregate) Candidate {
	observations := mergeObservations(aggregate.observations)
	supportSet := map[SupportType]struct{}{}
	episodesSeen := map[string]struct{}{}
	eventsSeen := map[string]struct{}{}
	var firstSeen, lastSeen = observations[0].FirstSeenAt, observations[0].LastSeenAt
	for _, observation := range observations {
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
	support := make([]SupportType, 0, len(supportSet))
	for item := range supportSet {
		support = append(support, item)
	}
	support = sortedSupportTypes(support)
	validation := validationFor(support, aggregate.base.RequiresApproval)
	return Candidate{
		SchemaVersion: CandidateSchemaVersion, CandidateID: aggregate.base.CandidateID,
		SemanticKeySHA256: aggregate.base.SemanticKeySHA256,
		Kind:              aggregate.base.Kind, Text: aggregate.base.Text, Polarity: aggregate.base.Polarity,
		Scope:        CandidateScope{Agent: aggregate.base.Agent, Status: ScopeUnconfirmed},
		SupportTypes: support, Observations: observations,
		EpisodeCount: len(episodesSeen), EvidenceEventCount: len(eventsSeen),
		FirstSeenAt: firstSeen.UTC(), LastSeenAt: lastSeen.UTC(), Validation: validation,
		RequiresExplicitRuleChangeApproval: aggregate.base.RequiresApproval,
		Privacy:                            "local_only",
	}
}

func validationFor(support []SupportType, requiresApproval bool) Validation {
	reasons := []string{"scope_confirmation_required"}
	strong := false
	for _, item := range support {
		switch item {
		case SupportExplicitRemember:
			strong = true
			reasons = append(reasons, "explicit_user_remember")
		case SupportUserCorrection:
			strong = true
			reasons = append(reasons, "user_correction")
		case SupportCompactionDrift:
			strong = true
			reasons = append(reasons, "compaction_drift_with_user_correction")
		case SupportStableRepetition:
			strong = true
			reasons = append(reasons, "stable_repetition")
		}
	}
	if !strong {
		reasons = append(reasons, "single_task_instruction_only")
	}
	if requiresApproval {
		reasons = append(reasons, "rule_change_requires_explicit_approval")
	}
	status := StatusUntrusted
	if strong {
		status = StatusReviewReady
	}
	sort.Strings(reasons)
	return Validation{
		Status: status, Reasons: reasons, ScopeConfirmationRequired: true,
		AutomaticPromotionEligible: false,
	}
}

func mergeObservations(observations []Observation) []Observation {
	sort.Slice(observations, func(left, right int) bool {
		return observationLess(observations[left], observations[right])
	})
	result := make([]Observation, 0, len(observations))
	for _, observation := range observations {
		observation.EvidenceEventIDs = appendUnique(nil, observation.EvidenceEventIDs...)
		observation.CorrectionEventIDs = appendUnique(nil, observation.CorrectionEventIDs...)
		observation.SupportTypes = sortedSupportTypes(observation.SupportTypes)
		if len(result) == 0 || result[len(result)-1].EpisodeID != observation.EpisodeID ||
			result[len(result)-1].StatementID != observation.StatementID {
			result = append(result, observation)
			continue
		}
		current := &result[len(result)-1]
		current.EvidenceEventIDs = appendUnique(current.EvidenceEventIDs, observation.EvidenceEventIDs...)
		current.CorrectionEventIDs = appendUnique(current.CorrectionEventIDs, observation.CorrectionEventIDs...)
		current.SupportTypes = sortedSupportTypes(append(current.SupportTypes, observation.SupportTypes...))
		if observation.FirstSeenAt.Before(current.FirstSeenAt) {
			current.FirstSeenAt = observation.FirstSeenAt
		}
		if observation.LastSeenAt.After(current.LastSeenAt) {
			current.LastSeenAt = observation.LastSeenAt
		}
	}
	return result
}

func observationLess(left, right Observation) bool {
	if left.EpisodeID != right.EpisodeID {
		return left.EpisodeID < right.EpisodeID
	}
	return left.StatementID < right.StatementID
}

func isExplicitRemember(text string) bool {
	normalized := normalizeText(text)
	padded := " " + normalized + " "
	for _, phrase := range []string{" remember ", " remember that ", " please remember ", " from now on "} {
		if strings.Contains(padded, phrase) {
			return true
		}
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, phrase := range []string{"请记住", "记住", "以后都", "从现在开始"} {
		if strings.Contains(compact, phrase) {
			return true
		}
	}
	return false
}

func detectPolarity(normalized string) CandidatePolarity {
	padded := " " + normalized + " "
	for _, phrase := range []string{
		" must not ", " do not ", " don t ", " never ", " without ", " prohibited ", " forbid ",
	} {
		if strings.Contains(padded, phrase) {
			return PolarityProhibitive
		}
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, phrase := range []string{"不要", "不得", "不能", "禁止", "不可", "不允许"} {
		if strings.Contains(compact, phrase) {
			return PolarityProhibitive
		}
	}
	return PolarityAffirmative
}

func semanticCore(normalized string) string {
	value := normalized
	for _, phrase := range []string{
		"请记住", "记住", "以后都", "从现在开始", "我再次强调", "重新强调", "再次",
		"我说", "刚才", "前面", "仍然", "必须", "不要", "不得", "不能", "禁止", "不可", "不允许",
	} {
		value = strings.ReplaceAll(value, phrase, "")
	}
	ignored := map[string]bool{
		"a": true, "again": true, "already": true, "always": true, "an": true,
		"as": true, "be": true, "do": true, "don": true, "forbid": true,
		"from": true, "i": true, "keep": true, "must": true, "never": true,
		"not": true, "only": true, "please": true, "preserve": true, "prohibited": true,
		"remember": true, "required": true, "said": true, "shall": true, "should": true,
		"still": true, "t": true, "that": true, "to": true, "without": true,
		"you": true,
	}
	words := strings.Fields(value)
	kept := make([]string, 0, len(words))
	for _, word := range words {
		if !ignored[word] {
			kept = append(kept, word)
		}
	}
	core := strings.Join(kept, " ")
	if core == "" {
		return normalized
	}
	return core
}

func requiresRuleChangeApproval(normalized string) bool {
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, phrase := range []string{
		"agents md", "skill", "skills", "hook", "hooks", "plugin", "plugins",
		"global rule", "global config", "codex config", "全局规则", "全局配置", "技能", "钩子", "插件",
	} {
		if strings.Contains(normalized, phrase) || strings.Contains(compact, strings.ReplaceAll(phrase, " ", "")) {
			return true
		}
	}
	return false
}

func normalizeText(text string) string {
	var builder strings.Builder
	space := false
	for _, char := range strings.ToLower(text) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			space = false
		} else if !space && builder.Len() > 0 {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func sortedSupportTypes(values []SupportType) []SupportType {
	seen := map[SupportType]struct{}{}
	for _, value := range values {
		seen[value] = struct{}{}
	}
	result := make([]SupportType, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func appendUnique(values []string, added ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(added))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, value := range added {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func candidateContentSHA256(candidate Candidate) string {
	content := struct {
		Kind     CandidateKind     `json:"kind"`
		Text     string            `json:"text"`
		Polarity CandidatePolarity `json:"polarity"`
		Scope    CandidateScope    `json:"scope"`
	}{candidate.Kind, candidate.Text, candidate.Polarity, candidate.Scope}
	data, _ := json.Marshal(content)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func deterministicID(namespace string, parts ...string) string {
	return namespace + "-" + deterministicHex(namespace, parts...)
}

func deterministicHex(namespace string, parts ...string) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(namespace))
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(part))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
