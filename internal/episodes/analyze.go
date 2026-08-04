package episodes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	continuityCoverageThreshold = 0.65
	correctionOverlapThreshold  = 0.30
	maxStatementRunes           = 2048
)

type statementOccurrence struct {
	statementID string
	kind        string
	eventID     string
	eventIndex  int
}

type textProfile struct {
	normalized string
	features   map[string]struct{}
}

type compactionCluster struct {
	indices []int
}

type episodeAnalysis struct {
	episode        Episode
	timelineEvents []ledger.Event
}

func analyzeEpisode(
	store *ledger.Store, events []ledger.Event, sourceSnapshots map[string]ledger.Event,
) episodeAnalysis {
	sortEvents(events)
	processEvents := events
	if len(processEvents) == 0 {
		return episodeAnalysis{}
	}
	episodeID := deterministicID("episode", string(processEvents[0].Source.Agent),
		processEvents[0].Source.ThreadID)
	episode := Episode{
		SchemaVersion: EpisodeSchemaVersion, EpisodeID: episodeID,
		Agent: processEvents[0].Source.Agent, ThreadID: processEvents[0].Source.ThreadID,
		StartedAt:    processEvents[0].ObservedAt.UTC(),
		EndedAt:      processEvents[len(processEvents)-1].ObservedAt.UTC(),
		FirstEventID: processEvents[0].EventID,
		LastEventID:  processEvents[len(processEvents)-1].EventID,
		EventCounts:  map[string]int{}, Statements: []Statement{},
		Compactions: []CompactionCheckpoint{}, Privacy: "local_only",
	}
	resolver := newRawResolver(store, events, sourceSnapshots)
	reasons := map[string]struct{}{}
	for _, event := range processEvents {
		episode.EventCounts[string(event.Kind)]++
		if event.Kind == ledger.KindGap {
			episode.Completeness.GapEventIDs = append(episode.Completeness.GapEventIDs,
				event.EventID)
		}
		if event.Completeness.Status != ledger.CompletenessComplete {
			reason := strings.TrimSpace(event.Completeness.Reason)
			if reason == "" {
				reason = string(event.Completeness.Status)
			}
			reasons[reason] = struct{}{}
		}
	}
	if len(reasons) == 0 && len(episode.Completeness.GapEventIDs) == 0 {
		episode.Completeness.Status = ledger.CompletenessComplete
	} else {
		episode.Completeness.Status = ledger.CompletenessPartial
	}
	for reason := range reasons {
		episode.Completeness.Reasons = append(episode.Completeness.Reasons, reason)
	}
	sort.Strings(episode.Completeness.Reasons)

	statements, occurrences, issues := deriveStatements(resolver, processEvents)
	episode.Statements = statements
	episode.Issues = append(episode.Issues, issues...)
	clusters := findCompactionClusters(processEvents)
	profiles := map[string]textProfile{}
	if len(clusters) > 0 {
		profiles = prepareStatementProfiles(statements)
	}
	for index, cluster := range clusters {
		nextStart := len(processEvents)
		if index+1 < len(clusters) {
			nextStart = clusters[index+1].indices[0]
		}
		checkpoint, checkpointIssues := analyzeCompaction(
			resolver, episodeID, processEvents, cluster, nextStart, occurrences, profiles,
		)
		episode.Compactions = append(episode.Compactions, checkpoint)
		episode.Issues = append(episode.Issues, checkpointIssues...)
	}
	return episodeAnalysis{episode: episode, timelineEvents: processEvents}
}

func deriveStatements(
	resolver *rawResolver, events []ledger.Event,
) ([]Statement, []statementOccurrence, []DerivationIssue) {
	statementsByID := map[string]*Statement{}
	occurrences := []statementOccurrence{}
	issues := []DerivationIssue{}
	firstUser := true
	for index, event := range events {
		if event.Kind != ledger.KindUserMessage {
			continue
		}
		raw, err := resolver.resolve(event)
		if err != nil {
			issues = append(issues, DerivationIssue{Code: "user_message_unresolvable",
				EventIDs: []string{event.EventID}})
			continue
		}
		text := extractEventText(event.Source.Agent, event.Kind, raw)
		if strings.TrimSpace(text) == "" {
			issues = append(issues, DerivationIssue{Code: "user_message_text_unavailable",
				EventIDs: []string{event.EventID}})
			continue
		}
		for _, clause := range splitClauses(text) {
			if utf8.RuneCountInString(clause) > maxStatementRunes {
				issues = append(issues, DerivationIssue{Code: "user_statement_too_long",
					EventIDs: []string{event.EventID}})
				continue
			}
			normalized := normalizeText(clause)
			kind := classifyNormalizedStatement(normalized, firstUser)
			if kind == "" {
				continue
			}
			if normalized == "" {
				continue
			}
			statementID := deterministicID("statement", normalized)
			statement, exists := statementsByID[statementID]
			if !exists {
				statement = &Statement{
					StatementID: statementID, Kind: kind, Text: clause,
					FirstSeenAt: event.ObservedAt.UTC(), LastSeenAt: event.ObservedAt.UTC(),
				}
				statementsByID[statementID] = statement
			}
			if !contains(statement.EvidenceEventIDs, event.EventID) {
				statement.EvidenceEventIDs = append(statement.EvidenceEventIDs, event.EventID)
			}
			if event.ObservedAt.Before(statement.FirstSeenAt) {
				statement.FirstSeenAt = event.ObservedAt.UTC()
			}
			if event.ObservedAt.After(statement.LastSeenAt) {
				statement.LastSeenAt = event.ObservedAt.UTC()
			}
			occurrences = append(occurrences, statementOccurrence{
				statementID: statementID, kind: kind, eventID: event.EventID,
				eventIndex: index,
			})
		}
		firstUser = false
	}
	statements := make([]Statement, 0, len(statementsByID))
	for _, statement := range statementsByID {
		statements = append(statements, *statement)
	}
	sort.Slice(statements, func(left, right int) bool {
		if !statements[left].FirstSeenAt.Equal(statements[right].FirstSeenAt) {
			return statements[left].FirstSeenAt.Before(statements[right].FirstSeenAt)
		}
		return statements[left].StatementID < statements[right].StatementID
	})
	return statements, occurrences, issues
}

func prepareStatementProfiles(statements []Statement) map[string]textProfile {
	profiles := make(map[string]textProfile, len(statements))
	for _, statement := range statements {
		profiles[statement.StatementID] = profileText(statement.Text)
	}
	return profiles
}

func analyzeCompaction(
	resolver *rawResolver, episodeID string, events []ledger.Event,
	cluster compactionCluster, nextStart int, occurrences []statementOccurrence,
	profiles map[string]textProfile,
) (CompactionCheckpoint, []DerivationIssue) {
	checkpoint := CompactionCheckpoint{Checks: []ContinuityCheck{}, Issues: []DerivationIssue{}}
	parts := []string{episodeID}
	representation := []string{}
	for _, eventIndex := range cluster.indices {
		event := events[eventIndex]
		checkpoint.EventIDs = append(checkpoint.EventIDs, event.EventID)
		parts = append(parts, event.EventID)
		if checkpoint.ObservedAt.IsZero() || event.ObservedAt.Before(checkpoint.ObservedAt) {
			checkpoint.ObservedAt = event.ObservedAt.UTC()
		}
		raw, err := resolver.resolve(event)
		if err != nil {
			checkpoint.Issues = append(checkpoint.Issues, DerivationIssue{
				Code: "compaction_representation_unresolvable", EventIDs: []string{event.EventID},
			})
			continue
		}
		text := strings.TrimSpace(extractEventText(event.Source.Agent, event.Kind, raw))
		if text != "" {
			representation = append(representation, text)
			checkpoint.RepresentationEventIDs = append(checkpoint.RepresentationEventIDs,
				event.EventID)
		}
	}
	checkpoint.CheckpointID = deterministicID("compaction-checkpoint", parts...)
	checkpoint.RepresentationAvailable = len(representation) > 0
	firstIndex := cluster.indices[0]
	lastIndex := cluster.indices[len(cluster.indices)-1]
	representationText := strings.Join(representation, "\n")
	active := activeStatementIDs(occurrences, firstIndex)
	if len(active) == 0 {
		checkpoint.Status = ContinuityNoActiveStatements
		return checkpoint, checkpoint.Issues
	}
	representationProfile := profileText(representationText)
	correctionsByStatement := correctionEvidenceByStatement(
		active, occurrences, profiles, lastIndex, nextStart,
	)
	allPreserved := true
	hasCorrection := false
	for _, statementID := range active {
		coverage := roundCoverage(profileCoverage(profiles[statementID], representationProfile))
		corrections := correctionsByStatement[statementID]
		check := ContinuityCheck{StatementID: statementID, Coverage: coverage}
		if coverage > 0 {
			check.RepresentationEventIDs = append(check.RepresentationEventIDs,
				checkpoint.RepresentationEventIDs...)
		}
		switch {
		case len(corrections) > 0:
			check.Status = StatementCorrectionAfterCompaction
			check.CorrectionEventIDs = corrections
			hasCorrection = true
			allPreserved = false
		case coverage >= continuityCoverageThreshold:
			check.Status = StatementPreserved
		default:
			check.Status = StatementMissingFromRepresentation
			allPreserved = false
		}
		checkpoint.Checks = append(checkpoint.Checks, check)
	}
	switch {
	case hasCorrection:
		checkpoint.Status = ContinuityDriftEvidence
	case !checkpoint.RepresentationAvailable:
		checkpoint.Status = ContinuityInsufficientEvidence
	case allPreserved:
		checkpoint.Status = ContinuityPreserved
	default:
		checkpoint.Status = ContinuityAtRisk
	}
	return checkpoint, checkpoint.Issues
}

func findCompactionClusters(events []ledger.Event) []compactionCluster {
	clusters := []compactionCluster{}
	current := compactionCluster{}
	barrier := false
	for index, event := range events {
		if event.Kind != ledger.KindCompaction {
			if len(current.indices) > 0 && isCompactionBarrier(event.Kind) {
				barrier = true
			}
			continue
		}
		if len(current.indices) == 0 {
			current.indices = []int{index}
			barrier = false
			continue
		}
		previous := events[current.indices[len(current.indices)-1]]
		withinWindow := event.ObservedAt.Sub(previous.ObservedAt) <= 2*time.Minute
		if !barrier && withinWindow {
			current.indices = append(current.indices, index)
			continue
		}
		clusters = append(clusters, current)
		current = compactionCluster{indices: []int{index}}
		barrier = false
	}
	if len(current.indices) > 0 {
		clusters = append(clusters, current)
	}
	return clusters
}

func isCompactionBarrier(kind ledger.EventKind) bool {
	switch kind {
	case ledger.KindUserMessage, ledger.KindAgentMessage, ledger.KindToolCall,
		ledger.KindToolResult, ledger.KindApproval, ledger.KindFileChange,
		ledger.KindSubagentEvent:
		return true
	default:
		return false
	}
}

func activeStatementIDs(occurrences []statementOccurrence, beforeIndex int) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, occurrence := range occurrences {
		if occurrence.eventIndex >= beforeIndex {
			continue
		}
		if _, exists := seen[occurrence.statementID]; exists {
			continue
		}
		seen[occurrence.statementID] = struct{}{}
		result = append(result, occurrence.statementID)
	}
	sort.Strings(result)
	return result
}

func correctionEvidenceByStatement(
	active []string, occurrences []statementOccurrence, profiles map[string]textProfile,
	afterIndex, beforeIndex int,
) map[string][]string {
	result := map[string][]string{}
	featurePostings := map[string][]string{}
	for _, statementID := range active {
		for feature := range profiles[statementID].features {
			featurePostings[feature] = append(featurePostings[feature], statementID)
		}
	}
	for _, occurrence := range occurrences {
		if occurrence.eventIndex <= afterIndex || occurrence.eventIndex >= beforeIndex ||
			occurrence.kind != "correction" {
			continue
		}
		matches := map[string]int{}
		for feature := range profiles[occurrence.statementID].features {
			for _, statementID := range featurePostings[feature] {
				matches[statementID]++
			}
		}
		qualifying := make([]string, 0, len(matches))
		for statementID, matched := range matches {
			featureCount := len(profiles[statementID].features)
			if featureCount > 0 && float64(matched)/float64(featureCount) >= correctionOverlapThreshold {
				qualifying = append(qualifying, statementID)
			}
		}
		sort.Strings(qualifying)
		for _, statementID := range qualifying {
			if !contains(result[statementID], occurrence.eventID) {
				result[statementID] = append(result[statementID], occurrence.eventID)
			}
		}
	}
	return result
}

func extractEventText(_ ledger.Agent, kind ledger.EventKind, raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	allowed := map[string]struct{}{
		"message": {}, "text": {}, "content": {}, "prompt": {},
		"instruction": {}, "instructions": {},
	}
	if kind == ledger.KindCompaction {
		for _, key := range []string{"summary", "body", "title", "goal", "goals",
			"constraint", "constraints", "replacement_history", "compact_summary",
			"custom_instructions"} {
			allowed[key] = struct{}{}
		}
	}
	fragments := []string{}
	collectTextFragments(value, "", allowed, &fragments)
	seen := map[string]struct{}{}
	result := []string{}
	for _, fragment := range fragments {
		fragment = strings.TrimSpace(fragment)
		if fragment == "" {
			continue
		}
		if _, exists := seen[fragment]; exists {
			continue
		}
		seen[fragment] = struct{}{}
		result = append(result, fragment)
	}
	return strings.Join(result, "\n")
}

func collectTextFragments(value any, parentKey string, allowed map[string]struct{}, result *[]string) {
	switch typed := value.(type) {
	case string:
		if _, ok := allowed[strings.ToLower(parentKey)]; ok {
			*result = append(*result, typed)
		}
	case []any:
		for _, item := range typed {
			collectTextFragments(item, parentKey, allowed, result)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := typed[key]
			collectTextFragments(item, key, allowed, result)
		}
	}
}

func splitClauses(text string) []string {
	clauses := []string{}
	var builder strings.Builder
	flush := func() {
		clause := strings.Join(strings.Fields(builder.String()), " ")
		builder.Reset()
		if utf8.RuneCountInString(clause) >= 4 && !isAcknowledgement(clause) {
			clauses = append(clauses, clause)
		}
	}
	for _, char := range text {
		switch char {
		case '\n', '\r', '.', '?', '!', ';', '。', '？', '！', '；':
			flush()
		default:
			builder.WriteRune(char)
		}
	}
	flush()
	return clauses
}

func classifyNormalizedStatement(normalized string, firstUser bool) string {
	if normalized == "" || isAcknowledgement(normalized) {
		return ""
	}
	if containsMarker(normalized, correctionMarkers) {
		return "correction"
	}
	if containsMarker(normalized, constraintMarkers) {
		return "constraint"
	}
	if firstUser || containsMarker(normalized, goalMarkers) {
		return "goal"
	}
	return ""
}

type textMarker struct {
	value string
	ascii bool
}

var correctionMarkers = prepareMarkers([]string{
	"again", "i said", "as i said", "already told", "still", "not what i",
	"再次", "我说", "刚才", "前面", "仍然", "不是", "重新强调", "我再次强调",
})

var constraintMarkers = prepareMarkers([]string{
	"must", "must not", "do not", "don't", "never", "only", "required",
	"keep", "preserve", "without", "before", "after", "always",
	"必须", "不要", "不能", "不得", "只", "务必", "保留", "保持", "禁止", "先", "之后",
})

var goalMarkers = prepareMarkers([]string{
	"please", "implement", "build", "create", "fix", "add", "support", "need", "want",
	"请", "帮", "实现", "修复", "创建", "增加", "支持", "需要", "希望", "我要", "完成",
})

func prepareMarkers(values []string) []textMarker {
	markers := make([]textMarker, 0, len(values))
	for _, value := range values {
		if normalized := normalizeText(value); normalized != "" {
			markers = append(markers, textMarker{value: normalized, ascii: isASCII(value)})
		}
	}
	return markers
}

func containsMarker(normalized string, markers []textMarker) bool {
	padded := " " + normalized + " "
	for _, marker := range markers {
		if marker.ascii {
			if strings.Contains(padded, " "+marker.value+" ") {
				return true
			}
			continue
		}
		if strings.Contains(normalized, marker.value) {
			return true
		}
	}
	return false
}

func isASCII(value string) bool {
	for _, char := range value {
		if char > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func isAcknowledgement(text string) bool {
	normalized := strings.TrimSpace(strings.ToLower(text))
	switch normalized {
	case "ok", "okay", "thanks", "thank you", "got it", "好的", "好", "谢谢", "明白", "收到", "继续":
		return true
	default:
		return false
	}
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

func profileText(text string) textProfile {
	return textProfile{normalized: normalizeText(text), features: textFeatures(text)}
}

func profileCoverage(statement, representation textProfile) float64 {
	if representation.normalized == "" {
		return 0
	}
	if statement.normalized != "" && strings.Contains(representation.normalized, statement.normalized) {
		return 1
	}
	return featureOverlap(statement.features, representation.features)
}

func featureOverlap(leftFeatures, rightFeatures map[string]struct{}) float64 {
	if len(leftFeatures) == 0 {
		return 0
	}
	matched := 0
	for feature := range leftFeatures {
		if _, ok := rightFeatures[feature]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(leftFeatures))
}

func textFeatures(text string) map[string]struct{} {
	features := map[string]struct{}{}
	word := []rune{}
	han := []rune{}
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		token := stemEnglish(strings.ToLower(string(word)))
		word = word[:0]
		if len(token) < 2 || englishStopWords[token] {
			return
		}
		features["en:"+token] = struct{}{}
	}
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		if len(han) == 1 {
			features["zh:"+string(han)] = struct{}{}
		} else {
			for index := 0; index+1 < len(han); index++ {
				features["zh:"+string(han[index:index+2])] = struct{}{}
			}
		}
		han = han[:0]
	}
	for _, char := range text {
		switch {
		case unicode.Is(unicode.Han, char):
			flushWord()
			han = append(han, char)
		case unicode.IsLetter(char) || unicode.IsDigit(char):
			flushHan()
			word = append(word, char)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return features
}

var englishStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "for": true, "from": true, "i": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "please": true,
	"the": true, "this": true, "to": true, "we": true, "you": true,
}

func stemEnglish(token string) string {
	for _, suffix := range []string{"ing", "ed", "es", "s"} {
		if strings.HasSuffix(token, suffix) && len(token) > len(suffix)+3 {
			return strings.TrimSuffix(token, suffix)
		}
	}
	return token
}

func roundCoverage(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func deterministicID(namespace string, parts ...string) string {
	digest := deterministicDigest(namespace, parts...)
	return namespace + "-" + hex.EncodeToString(digest[:])
}

func deterministicDigest(namespace string, parts ...string) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(namespace))
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(part))
	}
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sortEvents(events []ledger.Event) {
	sort.SliceStable(events, func(left, right int) bool {
		first, second := events[left], events[right]
		if !first.ObservedAt.Equal(second.ObservedAt) {
			return first.ObservedAt.Before(second.ObservedAt)
		}
		if first.Source.SourcePathHash != second.Source.SourcePathHash {
			return first.Source.SourcePathHash < second.Source.SourcePathHash
		}
		firstStart, secondStart := int64(-1), int64(-1)
		if first.Source.ByteStart != nil {
			firstStart = *first.Source.ByteStart
		}
		if second.Source.ByteStart != nil {
			secondStart = *second.Source.ByteStart
		}
		if firstStart != secondStart {
			return firstStart < secondStart
		}
		if first.Source.SourceCursor != second.Source.SourceCursor {
			return first.Source.SourceCursor < second.Source.SourceCursor
		}
		if !first.RecordedAt.Equal(second.RecordedAt) {
			return first.RecordedAt.Before(second.RecordedAt)
		}
		return first.EventID < second.EventID
	})
}
