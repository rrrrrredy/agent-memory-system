package candidates

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestBuildAppliesEvidenceGatesDeduplicationAndConflictQuarantine(t *testing.T) {
	episodeSet := policyEpisodes()
	firstStore, firstGeneration := createEpisodeGeneration(t, episodeSet)
	first, err := Build(firstStore, BuildOptions{
		EpisodeGenerationPath: firstGeneration, ShardCount: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Observations != 8 || first.Candidates != 7 || first.ReviewReady != 4 ||
		first.Untrusted != 1 || first.Quarantined != 2 || first.ConflictGroups != 1 || first.Reused {
		t.Fatalf("unexpected candidate result: %+v", first)
	}
	candidates := readCandidateFile(t, filepath.Join(first.GenerationPath, "candidates.jsonl"))
	if len(candidates) != 7 {
		t.Fatalf("got %d candidates", len(candidates))
	}
	for index, candidate := range candidates {
		if index > 0 && candidates[index-1].CandidateID >= candidate.CandidateID {
			t.Fatal("candidate output is not sorted")
		}
		if candidate.Validation.AutomaticPromotionEligible ||
			!candidate.Validation.ScopeConfirmationRequired || candidate.Scope.Status != ScopeUnconfirmed {
			t.Fatalf("candidate bypassed review boundaries: %+v", candidate)
		}
		if strings.Contains(candidate.Text, "Build a dashboard") {
			t.Fatal("ordinary task goal became a durable candidate")
		}
	}

	stable := findCandidate(t, candidates, "You must keep audit logs")
	if stable.EpisodeCount != 2 || stable.Validation.Status != StatusReviewReady ||
		!hasSupport(stable.SupportTypes, SupportStableRepetition) {
		t.Fatalf("stable repetition was not recognized: %+v", stable)
	}
	remember := findCandidate(t, candidates, "Please remember that reports stay local")
	if remember.Kind != KindDirective || remember.Validation.Status != StatusReviewReady ||
		!hasSupport(remember.SupportTypes, SupportExplicitRemember) {
		t.Fatalf("explicit remember was not review ready: %+v", remember)
	}
	drift := findCandidate(t, candidates, "Support offline mode")
	if drift.Validation.Status != StatusReviewReady ||
		!hasSupport(drift.SupportTypes, SupportCompactionDrift) ||
		len(drift.Observations[0].CorrectionEventIDs) != 1 {
		t.Fatalf("evidence-backed compaction drift was not preserved: %+v", drift)
	}
	rule := findCandidate(t, candidates, "You must not update AGENTS.md without approval")
	if !rule.RequiresExplicitRuleChangeApproval || rule.Validation.Status != StatusUntrusted ||
		!containsString(rule.Validation.Reasons, "rule_change_requires_explicit_approval") {
		t.Fatalf("rule-change approval gate is missing: %+v", rule)
	}
	positive := findCandidate(t, candidates, "You must upload raw evidence")
	negative := findCandidate(t, candidates, "You must not upload raw evidence")
	if positive.Validation.Status != StatusQuarantined || negative.Validation.Status != StatusQuarantined ||
		positive.ConflictGroupID == "" || positive.ConflictGroupID != negative.ConflictGroupID ||
		len(positive.ConflictingCandidateIDs) != 1 || len(negative.ConflictingCandidateIDs) != 1 {
		t.Fatalf("opposing candidates were not quarantined: positive=%+v negative=%+v",
			positive, negative)
	}
	verifiedGeneration, err := OpenGeneration(firstStore, first.GenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := verifiedGeneration.Select([]string{remember.CandidateID, positive.CandidateID})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Candidates) != 2 ||
		len(selection.ConflictGroups[positive.ConflictGroupID]) != 2 {
		t.Fatalf("verified selection is incomplete: %+v", selection)
	}

	reused, err := Build(firstStore, BuildOptions{
		EpisodeGenerationPath: firstGeneration, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.CandidatesSHA256 != first.CandidatesSHA256 {
		t.Fatalf("verified candidate generation was not reused: %+v", reused)
	}

	secondStore, secondGeneration := createEpisodeGeneration(t, episodeSet)
	second, err := Build(secondStore, BuildOptions{
		EpisodeGenerationPath: secondGeneration, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CandidatesSHA256 != first.CandidatesSHA256 {
		t.Fatalf("shard count changed deterministic output: %s != %s",
			second.CandidatesSHA256, first.CandidatesSHA256)
	}
}

func TestBuildRejectsTamperedOrOutOfRootEpisodeGeneration(t *testing.T) {
	store, generation := createEpisodeGeneration(t, policyEpisodes()[:1])
	episodesPath := filepath.Join(generation, "episodes.jsonl")
	file, err := os.OpenFile(episodesPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(" \n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(store, BuildOptions{EpisodeGenerationPath: generation}); err == nil ||
		!strings.Contains(err.Error(), "source verification") {
		t.Fatalf("tampered episode generation was accepted: %v", err)
	}

	outside := t.TempDir()
	if _, err := Build(store, BuildOptions{EpisodeGenerationPath: outside}); err == nil ||
		!strings.Contains(err.Error(), "direct child") {
		t.Fatalf("out-of-root episode generation was accepted: %v", err)
	}
}

func TestBuildHandlesEmptySourceAndRejectsInvalidShardCounts(t *testing.T) {
	store, generation := createEpisodeGeneration(t, nil)
	result, err := Build(store, BuildOptions{EpisodeGenerationPath: generation, ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceEpisodes != 0 || result.Observations != 0 || result.Candidates != 0 ||
		result.ReviewReady != 0 || result.Untrusted != 0 || result.Quarantined != 0 {
		t.Fatalf("empty source produced candidates: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(result.GenerationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("empty source produced %d candidate bytes", len(data))
	}

	for _, shardCount := range []int{-1, 3, 257} {
		if _, err := Build(store, BuildOptions{
			EpisodeGenerationPath: generation, ShardCount: shardCount,
		}); err == nil || !strings.Contains(err.Error(), "power of two") {
			t.Fatalf("invalid shard count %d was accepted: %v", shardCount, err)
		}
	}
}

func TestBuildSeparatesIdenticalEpisodeContentFromDifferentLedgerPrefixes(t *testing.T) {
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	firstEpisodes, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	firstCandidates, err := Build(store, BuildOptions{
		EpisodeGenerationPath: firstEpisodes.GenerationPath, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	payload := ledger.InlinePayload("utf-8", "application/json", `{}`)
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "global-claude-source",
		Kind: ledger.KindSourceSnapshot, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentClaudeCode, Adapter: "claude-code-companion-files",
			AdapterVersion: "claude-code-companion-files/v1alpha1",
			DeviceID:       store.DeviceID(), ThreadID: "global-claude-home",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	secondEpisodes, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if secondEpisodes.EpisodesSHA256 != firstEpisodes.EpisodesSHA256 ||
		secondEpisodes.SourceLastRecordHash == firstEpisodes.SourceLastRecordHash {
		t.Fatalf("fixture did not preserve episode content across different ledger prefixes: first=%+v second=%+v",
			firstEpisodes, secondEpisodes)
	}
	secondCandidates, err := Build(store, BuildOptions{
		EpisodeGenerationPath: secondEpisodes.GenerationPath, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondCandidates.GenerationPath == firstCandidates.GenerationPath ||
		secondCandidates.CandidatesSHA256 != firstCandidates.CandidatesSHA256 || secondCandidates.Reused {
		t.Fatalf("candidate generations did not preserve distinct provenance: first=%+v second=%+v",
			firstCandidates, secondCandidates)
	}
}

func TestGenerationVerificationRejectsCoordinatedContentTamper(t *testing.T) {
	store, episodeGeneration := createEpisodeGeneration(t, policyEpisodes()[:1])
	built, err := Build(store, BuildOptions{EpisodeGenerationPath: episodeGeneration, ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(built.GenerationPath, "candidates.jsonl")
	data, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(data, []byte("keep audit logs"), []byte("keep audit logz"), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("fixture candidate text was not found")
	}
	if err := os.WriteFile(candidatePath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(built.GenerationPath, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.CandidatesSHA256 = fileSHA256(t, candidatePath)
	manifestData, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := OpenGeneration(store, built.GenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verified.Select(nil); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("coordinated candidate and manifest tamper was accepted: %v", err)
	}
}

func TestGenerationVerificationRejectsForgedObservationProvenance(t *testing.T) {
	store, episodeGeneration := createEpisodeGeneration(t, policyEpisodes()[:1])
	built, err := Build(store, BuildOptions{EpisodeGenerationPath: episodeGeneration, ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(built.GenerationPath, "candidates.jsonl")
	items := readCandidateFile(t, candidatePath)
	selectedIndex := -1
	for index := range items {
		if items[index].Text == "Please remember that reports stay local" {
			selectedIndex = index
			break
		}
	}
	if selectedIndex < 0 {
		t.Fatal("fixture remember candidate was not found")
	}
	selectedID := items[selectedIndex].CandidateID
	items[selectedIndex].Observations[0].EvidenceEventIDs[0] = "forged-event"
	file, err := os.OpenFile(candidatePath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, candidate := range items {
		if err := encoder.Encode(candidate); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(built.GenerationPath, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.CandidatesSHA256 = fileSHA256(t, candidatePath)
	manifestData, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := OpenGeneration(store, built.GenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verified.Select([]string{selectedID}); err == nil ||
		!strings.Contains(err.Error(), "provenance") {
		t.Fatalf("forged candidate observation provenance was accepted: %v", err)
	}
}

func TestSemanticConflictCoreHandlesEnglishAndChineseNegation(t *testing.T) {
	englishPositive := normalizeText("You must upload raw evidence")
	englishNegative := normalizeText("You must not upload raw evidence")
	if semanticCore(englishPositive) != semanticCore(englishNegative) ||
		detectPolarity(englishPositive) != PolarityAffirmative ||
		detectPolarity(englishNegative) != PolarityProhibitive {
		t.Fatal("English polarity pair did not share a semantic core")
	}
	chinesePositive := normalizeText("必须上传原始证据")
	chineseNegative := normalizeText("不得上传原始证据")
	if semanticCore(chinesePositive) != semanticCore(chineseNegative) ||
		detectPolarity(chinesePositive) != PolarityAffirmative ||
		detectPolarity(chineseNegative) != PolarityProhibitive {
		t.Fatal("Chinese polarity pair did not share a semantic core")
	}
}

func policyEpisodes() []episodes.Episode {
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	return []episodes.Episode{
		makeEpisode("one", base, []episodes.Statement{
			makeStatement("goal", "goal", "Build a dashboard", "event-goal", base),
			makeStatement("audit-one", "constraint", "You must keep audit logs", "event-audit-one", base.Add(time.Second)),
			makeStatement("remember", "goal", "Please remember that reports stay local", "event-remember", base.Add(2*time.Second)),
			makeStatement("positive", "constraint", "You must upload raw evidence", "event-positive", base.Add(3*time.Second)),
			makeStatement("rule", "constraint", "You must not update AGENTS.md without approval", "event-rule", base.Add(4*time.Second)),
		}, nil),
		makeEpisode("two", base.Add(time.Hour), []episodes.Statement{
			makeStatement("audit-two", "constraint", "You must keep audit logs", "event-audit-two", base.Add(time.Hour)),
			makeStatement("negative", "constraint", "You must not upload raw evidence", "event-negative", base.Add(time.Hour+time.Second)),
		}, nil),
		makeEpisode("three", base.Add(2*time.Hour), []episodes.Statement{
			makeStatement("correction", "correction", "I said again support offline mode", "event-correction", base.Add(2*time.Hour)),
		}, nil),
		makeEpisode("four", base.Add(3*time.Hour), []episodes.Statement{
			makeStatement("drift", "goal", "Support offline mode", "event-drift", base.Add(3*time.Hour)),
		}, []episodes.CompactionCheckpoint{{
			CheckpointID: deterministicID("compaction-checkpoint", "four"),
			EventIDs:     []string{"event-compaction"}, ObservedAt: base.Add(3*time.Hour + time.Minute),
			RepresentationAvailable: true, Status: episodes.ContinuityDriftEvidence,
			Checks: []episodes.ContinuityCheck{{
				StatementID: deterministicID("statement", "drift"), Coverage: 0,
				Status:             episodes.StatementCorrectionAfterCompaction,
				CorrectionEventIDs: []string{"event-drift-correction"},
			}},
		}}),
	}
}

func makeEpisode(
	name string, observed time.Time, statements []episodes.Statement,
	compactions []episodes.CompactionCheckpoint,
) episodes.Episode {
	return episodes.Episode{
		SchemaVersion: episodes.EpisodeSchemaVersion,
		EpisodeID:     deterministicID("episode", name), Agent: ledger.AgentCodex,
		ThreadID: "thread-" + name, StartedAt: observed, EndedAt: observed.Add(time.Minute),
		FirstEventID: "first-" + name, LastEventID: "last-" + name,
		EventCounts:  map[string]int{"user_message": len(statements)},
		Completeness: episodes.EpisodeCompleteness{Status: ledger.CompletenessComplete},
		Statements:   statements, Compactions: compactions, Privacy: "local_only",
	}
}

func makeStatement(name, kind, text, eventID string, observed time.Time) episodes.Statement {
	return episodes.Statement{
		StatementID: deterministicID("statement", name), Kind: kind, Text: text,
		EvidenceEventIDs: []string{eventID}, FirstSeenAt: observed, LastSeenAt: observed,
	}
}

func createEpisodeGeneration(t *testing.T, episodeSet []episodes.Episode) (*ledger.Store, string) {
	t.Helper()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(store.Root(), "derived", "generations", "episodes-fixture")
	if err := os.MkdirAll(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	episodesPath := filepath.Join(generation, "episodes.jsonl")
	file, err := os.OpenFile(episodesPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, episode := range episodeSet {
		if err := encoder.Encode(episode); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	episodesDigest := fileSHA256(t, episodesPath)
	emptyDigest := sha256.Sum256(nil)
	manifest := episodes.Manifest{
		SchemaVersion:     episodes.ManifestSchemaVersion,
		DerivationVersion: episodes.DerivationVersion, Privacy: "local_only",
		Episodes: len(episodeSet), TimelineFile: "timeline.jsonl",
		TimelineSHA256: hex.EncodeToString(emptyDigest[:]),
		EpisodesFile:   "episodes.jsonl", EpisodesSHA256: episodesDigest,
	}
	if err := os.WriteFile(filepath.Join(generation, "timeline.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(generation, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return store, generation
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readCandidateFile(t *testing.T, path string) []Candidate {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	result := []Candidate{}
	for {
		var candidate Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, candidate)
	}
	return result
}

func findCandidate(t *testing.T, candidates []Candidate, text string) Candidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Text == text {
			return candidate
		}
	}
	t.Fatalf("candidate %q not found", text)
	return Candidate{}
}

func hasSupport(values []SupportType, wanted SupportType) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
