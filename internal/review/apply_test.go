package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestApplyReviewTransitionsAndOptimisticState(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	request := validationRequest(remember, BasisExplicitRemember, nil)
	result, err := Apply(fixture.store, fixture.generation, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sequence != 1 || len(result.Transitions) != 1 ||
		result.Transitions[0].ResultingStatus != StatusValidated {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	status, err := GetStatus(fixture.store, fixture.generation, remember.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewStatus != StatusValidated || status.ReviewEvents != 1 ||
		status.LastReviewRecordSHA256 != result.RecordSHA256 {
		t.Fatalf("unexpected validated status: %+v", status)
	}
	if _, err := Apply(fixture.store, fixture.generation, request); err == nil ||
		!strings.Contains(err.Error(), "status is") {
		t.Fatalf("stale optimistic state was accepted: %v", err)
	}

	rejected, err := Apply(fixture.store, fixture.generation, Request{
		SchemaVersion: RequestSchemaVersion, Reviewer: humanReviewer(),
		Transitions: []TransitionRequest{{
			CandidateID: remember.CandidateID, CandidateContentSHA256: remember.ContentSHA256,
			ExpectedStatus: StatusValidated, Action: ActionReject, Reason: "No longer applicable.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Sequence != 2 || rejected.Transitions[0].ResultingStatus != StatusRejected {
		t.Fatalf("unexpected reject result: %+v", rejected)
	}
	reopened, err := Apply(fixture.store, fixture.generation, Request{
		SchemaVersion: RequestSchemaVersion, Reviewer: humanReviewer(),
		Transitions: []TransitionRequest{{
			CandidateID: remember.CandidateID, CandidateContentSHA256: remember.ContentSHA256,
			ExpectedStatus: StatusRejected, Action: ActionReopen, Reason: "New evidence requires review.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Sequence != 3 || reopened.Transitions[0].ResultingStatus != StatusPending {
		t.Fatalf("unexpected reopen result: %+v", reopened)
	}
	report := Verify(fixture.store)
	if report.RecordsChecked != 3 || len(report.Issues) != 0 ||
		report.LastRecordSHA256 != reopened.RecordSHA256 {
		t.Fatalf("review ledger did not verify: %+v", report)
	}
}

func TestValidationProofBindsCurrentAndHistoricalReviewRecords(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	validated, err := Apply(
		fixture.store, fixture.generation,
		validationRequest(remember, BasisExplicitRemember, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveCurrentValidation(
		fixture.store, fixture.generation, remember.CandidateID, remember.ContentSHA256,
		validated.RecordSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Candidate.CandidateID != remember.CandidateID ||
		resolved.Proof.ReviewRecordSHA256 != validated.RecordSHA256 ||
		resolved.Proof.Scope.Kind != ScopeGlobal || resolved.Proof.Privacy != "local_only" {
		t.Fatalf("unexpected validation proof: %+v", resolved)
	}

	if _, err := Apply(fixture.store, fixture.generation, Request{
		SchemaVersion: RequestSchemaVersion, Reviewer: humanReviewer(),
		Transitions: []TransitionRequest{{
			CandidateID: remember.CandidateID, CandidateContentSHA256: remember.ContentSHA256,
			ExpectedStatus: StatusValidated, Action: ActionReject, Reason: "The memory is obsolete.",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCurrentValidation(
		fixture.store, fixture.generation, remember.CandidateID, remember.ContentSHA256,
		validated.RecordSHA256,
	); err == nil || !strings.Contains(err.Error(), "not the current") {
		t.Fatalf("obsolete validation was accepted as current: %v", err)
	}
	historical, err := VerifyValidationRecord(
		fixture.store, fixture.generation, remember.CandidateID, remember.ContentSHA256,
		validated.RecordSHA256,
	)
	if err != nil || historical.Proof.ReviewEventID != validated.EventID {
		t.Fatalf("historical validation proof was not reproducible: %+v, %v", historical, err)
	}
}

func TestReviewStateDoesNotSilentlyCarryAcrossCandidateGenerations(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	if _, err := Apply(fixture.store, fixture.generation,
		validationRequest(remember, BasisExplicitRemember, nil)); err != nil {
		t.Fatal(err)
	}
	nextEpisodes := append([]episodes.Episode(nil), fixture.episodes...)
	base := time.Date(2026, 8, 4, 3, 0, 0, 0, time.UTC)
	nextEpisodes = append(nextEpisodes, fixtureEpisode("three", base, []episodes.Statement{
		fixtureStatement("extra", "constraint", "Keep reports offline", "extra-event", base),
	}))
	nextEpisodeGeneration := writeEpisodeGeneration(
		t, fixture.store, nextEpisodes, "episodes-review-fixture-next",
	)
	next, err := candidates.Build(fixture.store, candidates.BuildOptions{
		EpisodeGenerationPath: nextEpisodeGeneration, ShardCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, next.GenerationPath, remember.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if status.ReviewStatus != StatusPending || status.ReviewEvents != 0 {
		t.Fatalf("old decision silently carried into a new candidate generation: %+v", status)
	}
}

func TestConcurrentReviewRequestsHaveOneWinner(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	request := validationRequest(remember, BasisExplicitRemember, nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := Apply(fixture.store, fixture.generation, request)
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for index := 0; index < 2; index++ {
		if err := <-results; err == nil {
			succeeded++
		} else if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "status is") {
			t.Fatalf("unexpected concurrent review failure: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent review successes = %d, want 1", succeeded)
	}
	report := Verify(fixture.store)
	if report.RecordsChecked != 1 || len(report.Issues) != 0 {
		t.Fatalf("concurrent review corrupted ledger: %+v", report)
	}
}

func TestValidationRequiresSupportedOrVerifiedEvidence(t *testing.T) {
	fixture := newReviewFixture(t)
	untrusted := fixture.byText["Keep audit logs"]
	forged := untrusted
	forged.Observations = append([]candidates.Observation(nil), untrusted.Observations...)
	forged.Observations[0].EvidenceEventIDs = []string{"agent-claim-event"}
	if err := verifyCandidateEvidence(fixture.store, map[string]candidates.Candidate{
		forged.CandidateID: forged,
	}); err == nil || !strings.Contains(err.Error(), "not a user message") {
		t.Fatalf("non-user candidate provenance was accepted: %v", err)
	}
	unsupported := validationRequest(untrusted, BasisExplicitRemember, nil)
	if _, err := Apply(fixture.store, fixture.generation, unsupported); err == nil ||
		!strings.Contains(err.Error(), "not present") {
		t.Fatalf("unsupported validation basis was accepted: %v", err)
	}

	missing := validationRequest(untrusted, BasisOutcomeEvidence, []string{"missing-event"})
	if _, err := Apply(fixture.store, fixture.generation, missing); err == nil ||
		!strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing result evidence was accepted: %v", err)
	}
	selfConfirmation := validationRequest(untrusted, BasisOutcomeEvidence, []string{"agent-claim-event"})
	if _, err := Apply(fixture.store, fixture.generation, selfConfirmation); err == nil ||
		!strings.Contains(err.Error(), "no eligible result event") {
		t.Fatalf("agent self-confirmation was accepted as outcome evidence: %v", err)
	}
	incomplete := validationRequest(untrusted, BasisOutcomeEvidence, []string{"partial-outcome-event"})
	if _, err := Apply(fixture.store, fixture.generation, incomplete); err == nil ||
		!strings.Contains(err.Error(), "not complete") {
		t.Fatalf("incomplete outcome evidence was accepted: %v", err)
	}

	withOutcome := validationRequest(untrusted, BasisOutcomeEvidence, []string{"outcome-event"})
	result, err := Apply(fixture.store, fixture.generation, withOutcome)
	if err != nil {
		t.Fatal(err)
	}
	if result.Transitions[0].ResultingStatus != StatusValidated {
		t.Fatalf("verified outcome did not validate candidate: %+v", result)
	}
}

func TestConflictResolutionIsAtomicAndComplete(t *testing.T) {
	fixture := newReviewFixture(t)
	positive := fixture.byText["You must upload raw evidence"]
	negative := fixture.byText["You must not upload raw evidence"]
	if positive.ConflictGroupID == "" || positive.ConflictGroupID != negative.ConflictGroupID {
		t.Fatal("fixture did not produce a conflict group")
	}
	individual := validationRequest(negative, BasisUserCorrection, nil)
	individual.Transitions[0].ExpectedStatus = StatusQuarantined
	if _, err := Apply(fixture.store, fixture.generation, individual); err == nil ||
		!strings.Contains(err.Error(), "full conflict resolution") {
		t.Fatalf("individual conflict validation was accepted: %v", err)
	}

	missingMember := individual
	missingMember.ConflictGroupID = negative.ConflictGroupID
	if _, err := Apply(fixture.store, fixture.generation, missingMember); err == nil ||
		!strings.Contains(err.Error(), "every candidate") {
		t.Fatalf("partial conflict resolution was accepted: %v", err)
	}

	requests := []TransitionRequest{
		{
			CandidateID: positive.CandidateID, CandidateContentSHA256: positive.ContentSHA256,
			ExpectedStatus: StatusQuarantined, Action: ActionReject, Reason: "Conflicts with the correction.",
		},
		{
			CandidateID: negative.CandidateID, CandidateContentSHA256: negative.ContentSHA256,
			ExpectedStatus: StatusQuarantined, Action: ActionValidate,
			Scope: &Scope{Kind: ScopeGlobal, Value: "*"}, Basis: []Basis{BasisUserCorrection},
			Reason: "The explicit correction is authoritative.",
		},
	}
	sort.Slice(requests, func(left, right int) bool {
		return requests[left].CandidateID < requests[right].CandidateID
	})
	result, err := Apply(fixture.store, fixture.generation, Request{
		SchemaVersion: RequestSchemaVersion, Reviewer: humanReviewer(),
		ConflictGroupID: negative.ConflictGroupID, Transitions: requests,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Transitions) != 2 {
		t.Fatalf("conflict resolution was not atomic: %+v", result)
	}
	negativeStatus, err := GetStatus(fixture.store, fixture.generation, negative.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	positiveStatus, err := GetStatus(fixture.store, fixture.generation, positive.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if negativeStatus.ReviewStatus != StatusValidated || positiveStatus.ReviewStatus != StatusRejected {
		t.Fatalf("unexpected conflict states: negative=%+v positive=%+v", negativeStatus, positiveStatus)
	}
}

func TestApplyRejectsContentMismatchLockAndTamperedLedger(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	mismatch := validationRequest(remember, BasisExplicitRemember, nil)
	mismatch.Transitions[0].CandidateContentSHA256 = strings.Repeat("0", 64)
	if _, err := Apply(fixture.store, fixture.generation, mismatch); err == nil ||
		!strings.Contains(err.Error(), "content hash") {
		t.Fatalf("candidate content mismatch was accepted: %v", err)
	}

	lockPath := filepath.Join(fixture.store.Root(), "state", "candidate-review.lock")
	if err := os.WriteFile(lockPath, []byte("occupied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(fixture.store, fixture.generation,
		validationRequest(remember, BasisExplicitRemember, nil)); err == nil ||
		!strings.Contains(err.Error(), "is locked") {
		t.Fatalf("existing review lock was ignored: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(fixture.store, fixture.generation,
		validationRequest(remember, BasisExplicitRemember, nil)); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(fixture.store.Root(), filepath.FromSlash(reviewLedgerPath))
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(ledgerPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report := Verify(fixture.store)
	if len(report.Issues) == 0 {
		t.Fatal("tampered review ledger verified successfully")
	}
}

func TestVerifyRejectsRehashedUnsupportedDecision(t *testing.T) {
	fixture := newReviewFixture(t)
	remember := fixture.byText["Please remember that reports stay local"]
	if _, err := Apply(fixture.store, fixture.generation,
		validationRequest(remember, BasisExplicitRemember, nil)); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(fixture.store.Root(), filepath.FromSlash(reviewLedgerPath))
	data, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.Event.Transitions[0].Basis = []Basis{BasisStableRepetition}
	generation, err := candidates.OpenGeneration(fixture.store, fixture.generation)
	if err != nil {
		t.Fatal(err)
	}
	request := validationRequest(remember, BasisStableRepetition, nil)
	digest, err := hashRequest(generation, request)
	if err != nil {
		t.Fatal(err)
	}
	record.Event.RequestSHA256 = digest
	record, err = makeRecord(record.Event, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(ledgerPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report := Verify(fixture.store)
	if len(report.Issues) == 0 || !strings.Contains(report.Issues[0], "not present") {
		t.Fatalf("rehashed unsupported review decision verified: %+v", report)
	}
	if _, err := GetStatus(fixture.store, fixture.generation, remember.CandidateID); err == nil ||
		!strings.Contains(err.Error(), "not present") {
		t.Fatalf("status query trusted a semantically invalid review chain: %v", err)
	}
}

func TestDecodeRequestRejectsUnknownAndTrailingData(t *testing.T) {
	unknown := `{"schema_version":"candidate-review-request/v1alpha1","reviewer":{"kind":"human","id":"owner"},"transitions":[],"unknown":true}`
	if _, err := DecodeRequest(strings.NewReader(unknown)); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field was accepted: %v", err)
	}
	if _, err := DecodeRequest(strings.NewReader(`{} {}`)); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Fatalf("trailing request value was accepted: %v", err)
	}
}

type reviewFixture struct {
	store      *ledger.Store
	generation string
	byText     map[string]candidates.Candidate
	episodes   []episodes.Episode
}

func newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	appendFixtureEvidence(t, store, base)
	episodeSet := []episodes.Episode{
		fixtureEpisode("one", base, []episodes.Statement{
			fixtureStatement("remember", "goal", "Please remember that reports stay local", "remember-event", base),
			fixtureStatement("audit", "constraint", "Keep audit logs", "audit-event", base.Add(time.Second)),
			fixtureStatement("positive", "constraint", "You must upload raw evidence", "positive-event", base.Add(2*time.Second)),
		}),
		fixtureEpisode("two", base.Add(time.Hour), []episodes.Statement{
			fixtureStatement("negative", "correction", "You must not upload raw evidence", "negative-event", base.Add(time.Hour)),
		}),
	}
	episodeGeneration := writeEpisodeGeneration(t, store, episodeSet, "episodes-review-fixture")
	built, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeGeneration, ShardCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(built.GenerationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	byText := map[string]candidates.Candidate{}
	for {
		var candidate candidates.Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		byText[candidate.Text] = candidate
	}
	return reviewFixture{
		store: store, generation: built.GenerationPath, byText: byText, episodes: episodeSet,
	}
}

func appendFixtureEvidence(t *testing.T, store *ledger.Store, base time.Time) {
	t.Helper()
	types := []struct {
		id   string
		kind ledger.EventKind
	}{
		{"remember-event", ledger.KindUserMessage},
		{"audit-event", ledger.KindUserMessage},
		{"positive-event", ledger.KindUserMessage},
		{"negative-event", ledger.KindUserMessage},
		{"outcome-event", ledger.KindToolResult},
		{"confirmation-event", ledger.KindUserMessage},
		{"agent-claim-event", ledger.KindAgentMessage},
	}
	events := make([]ledger.Event, 0, len(types))
	for index, item := range types {
		payload := ledger.InlinePayload("utf-8", "text/plain", item.id)
		events = append(events, ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: item.id, Kind: item.kind,
			ObservedAt: base.Add(time.Duration(index) * time.Second),
			RecordedAt: base.Add(time.Duration(index) * time.Second),
			Source: ledger.Source{
				Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
				DeviceID: store.DeviceID(), ThreadID: "fixture-thread",
			},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Privacy: ledger.Privacy{Classification: "local_only"},
		})
	}
	events = append(events, ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "partial-outcome-event", Kind: ledger.KindToolResult,
		ObservedAt: base.Add(20 * time.Second), RecordedAt: base.Add(20 * time.Second),
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
			DeviceID: store.DeviceID(), ThreadID: "fixture-thread",
		},
		Completeness: ledger.Completeness{Status: ledger.CompletenessPartial, Reason: "capture interrupted"},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	})
	if _, err := store.AppendBatch(events); err != nil {
		t.Fatal(err)
	}
}

func writeEpisodeGeneration(
	t *testing.T, store *ledger.Store, episodeSet []episodes.Episode, name string,
) string {
	t.Helper()
	path := filepath.Join(store.Root(), "derived", "generations", name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	episodesPath := filepath.Join(path, "episodes.jsonl")
	file, err := os.OpenFile(episodesPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, episode := range episodeSet {
		if err := encoder.Encode(episode); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	episodeData, err := os.ReadFile(episodesPath)
	if err != nil {
		t.Fatal(err)
	}
	episodesDigest := sha256.Sum256(episodeData)
	emptyDigest := sha256.Sum256(nil)
	manifest := episodes.Manifest{
		SchemaVersion: episodes.ManifestSchemaVersion, DerivationVersion: episodes.DerivationVersion,
		Privacy: "local_only", Episodes: len(episodeSet), TimelineFile: "timeline.jsonl",
		TimelineSHA256: hex.EncodeToString(emptyDigest[:]), EpisodesFile: "episodes.jsonl",
		EpisodesSHA256: hex.EncodeToString(episodesDigest[:]),
	}
	if err := os.WriteFile(filepath.Join(path, "timeline.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(path, "manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureEpisode(name string, observed time.Time, statements []episodes.Statement) episodes.Episode {
	return episodes.Episode{
		SchemaVersion: episodes.EpisodeSchemaVersion, EpisodeID: fixtureID("episode", name),
		Agent: ledger.AgentCodex, ThreadID: "thread-" + name,
		StartedAt: observed, EndedAt: observed.Add(time.Minute),
		FirstEventID: "first-" + name, LastEventID: "last-" + name,
		EventCounts:  map[string]int{"user_message": len(statements)},
		Completeness: episodes.EpisodeCompleteness{Status: ledger.CompletenessComplete},
		Statements:   statements, Privacy: "local_only",
	}
}

func fixtureStatement(
	name, kind, text, eventID string, observed time.Time,
) episodes.Statement {
	return episodes.Statement{
		StatementID: fixtureID("statement", name), Kind: kind, Text: text,
		EvidenceEventIDs: []string{eventID}, FirstSeenAt: observed, LastSeenAt: observed,
	}
}

func fixtureID(prefix, value string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

func humanReviewer() Reviewer {
	return Reviewer{Kind: "human", ID: "owner"}
}

func validationRequest(
	candidate candidates.Candidate, basis Basis, evidenceEventIDs []string,
) Request {
	return Request{
		SchemaVersion: RequestSchemaVersion, Reviewer: humanReviewer(),
		Transitions: []TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: StatusPending, Action: ActionValidate,
			Scope: &Scope{Kind: ScopeGlobal, Value: "*"}, Basis: []Basis{basis},
			EvidenceEventIDs: evidenceEventIDs, Reason: "Evidence and scope reviewed.",
		}},
	}
}
