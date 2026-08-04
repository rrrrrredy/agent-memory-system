package ruleapproval

import (
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

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestRuleApprovalAuthorizeRevokeAndVerify(t *testing.T) {
	fixture := newRuleApprovalFixture(t)
	request := authorizationRequest(fixture, ActionAuthorize, StatusNotAuthorized)
	authorized, err := Apply(fixture.store, request)
	if err != nil {
		t.Fatal(err)
	}
	if authorized.Status != StatusAuthorized || authorized.Surface != SurfaceSkill {
		t.Fatalf("unexpected rule authorization: %+v", authorized)
	}
	status, err := GetStatus(
		fixture.store, fixture.memoryID, fixture.revisionID, SurfaceSkill, "skill:reporting",
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.ApprovalStatus != StatusAuthorized || !status.Effective || status.Events != 1 {
		t.Fatalf("unexpected authorization status: %+v", status)
	}
	otherTarget, err := GetStatus(
		fixture.store, fixture.memoryID, fixture.revisionID, SurfaceSkill, "skill:release-notes",
	)
	if err != nil {
		t.Fatal(err)
	}
	if otherTarget.ApprovalStatus != StatusNotAuthorized || otherTarget.Effective || otherTarget.Events != 0 {
		t.Fatalf("authorization leaked to another target: %+v", otherTarget)
	}
	if _, err := Apply(fixture.store, request); err == nil || !strings.Contains(err.Error(), "status is") {
		t.Fatalf("stale duplicate authorization was accepted: %v", err)
	}
	verification := Verify(fixture.store)
	if verification.RecordsChecked != 1 || verification.ApprovalsChecked != 1 ||
		len(verification.Issues) != 0 {
		t.Fatalf("rule approval ledger did not verify: %+v", verification)
	}

	revoked, err := Apply(fixture.store, authorizationRequest(fixture, ActionRevoke, StatusAuthorized))
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Status != StatusNotAuthorized {
		t.Fatalf("unexpected approval revocation: %+v", revoked)
	}
	status, err = GetStatus(
		fixture.store, fixture.memoryID, fixture.revisionID, SurfaceSkill, "skill:reporting",
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.ApprovalStatus != StatusNotAuthorized || status.Effective || status.Events != 2 {
		t.Fatalf("revoked approval remained effective: %+v", status)
	}
}

func TestRuleApprovalBecomesIneffectiveWhenMemoryIsRevoked(t *testing.T) {
	fixture := newRuleApprovalFixture(t)
	if _, err := Apply(fixture.store,
		authorizationRequest(fixture, ActionAuthorize, StatusNotAuthorized)); err != nil {
		t.Fatal(err)
	}
	if _, err := promotion.Apply(fixture.store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionRevoke,
		MemoryID:      fixture.memoryID, ExpectedRevisionID: fixture.revisionID,
		Reason: "This memory is obsolete.",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(
		fixture.store, fixture.memoryID, fixture.revisionID, SurfaceSkill, "skill:reporting",
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.ApprovalStatus != StatusAuthorized || status.Effective {
		t.Fatalf("approval remained effective after memory revocation: %+v", status)
	}
	verification := Verify(fixture.store)
	if len(verification.Issues) != 1 || !strings.Contains(verification.Issues[0], "no longer effective") {
		t.Fatalf("stale rule authorization was not reported: %+v", verification)
	}
	if _, err := Apply(fixture.store,
		authorizationRequest(fixture, ActionRevoke, StatusAuthorized)); err != nil {
		t.Fatal(err)
	}
	if verification = Verify(fixture.store); len(verification.Issues) != 0 {
		t.Fatalf("approval revocation did not close stale authorization: %+v", verification)
	}
}

func TestConcurrentRuleAuthorizationHasOneWinner(t *testing.T) {
	fixture := newRuleApprovalFixture(t)
	request := authorizationRequest(fixture, ActionAuthorize, StatusNotAuthorized)
	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := Apply(fixture.store, request)
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for index := 0; index < 2; index++ {
		if err := <-results; err == nil {
			succeeded++
		} else if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "status is") {
			t.Fatalf("unexpected concurrent authorization failure: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent authorization successes = %d, want 1", succeeded)
	}
}

func TestRuleApprovalRejectsTamperingUnknownFieldsAndTrailingData(t *testing.T) {
	fixture := newRuleApprovalFixture(t)
	if _, err := Apply(fixture.store,
		authorizationRequest(fixture, ActionAuthorize, StatusNotAuthorized)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.store.Root(), filepath.FromSlash(approvalLedgerPath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if verification := Verify(fixture.store); len(verification.Issues) == 0 {
		t.Fatalf("tampered rule approval ledger verified: %+v", verification)
	}

	unknown := `{"schema_version":"rule-change-approval-request/v1alpha1","unknown":true}`
	if _, err := DecodeRequest(strings.NewReader(unknown)); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown request field was accepted: %v", err)
	}
	if _, err := DecodeRequest(strings.NewReader(`{} {}`)); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Fatalf("trailing request value was accepted: %v", err)
	}
}

type ruleApprovalFixture struct {
	store      *ledger.Store
	memoryID   string
	revisionID string
}

func newRuleApprovalFixture(t *testing.T) ruleApprovalFixture {
	t.Helper()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	text := "Please remember that Skills changes require approval"
	payload := ledger.InlinePayload("utf-8", "text/plain", text)
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "rule-memory-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
			DeviceID: store.DeviceID(), ThreadID: "rule-memory-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	episodeGeneration := writeRuleApprovalEpisode(t, store, now, text)
	candidateBuild, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeGeneration, ShardCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(candidateBuild.GenerationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var candidate candidates.Candidate
	for {
		var item candidates.Candidate
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if item.Text == text {
			candidate = item
		}
	}
	if candidate.CandidateID == "" {
		t.Fatal("fixture candidate was not derived")
	}
	generation := filepath.Base(candidateBuild.GenerationPath)
	reviewed, err := review.Apply(store, generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
			Scope: &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis: []review.Basis{review.BasisExplicitRemember}, Reason: "Evidence and scope reviewed.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	textHash := sha256.Sum256([]byte(text))
	promoted, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation, CandidateID: candidate.CandidateID,
			CandidateContentSHA256:  candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewed.RecordSHA256,
		},
		ExpectedTextSHA256: hex.EncodeToString(textHash[:]),
		Reason:             "The reviewed candidate is safe to promote.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ruleApprovalFixture{
		store: store, memoryID: promoted.Revision.MemoryID, revisionID: promoted.Revision.RevisionID,
	}
}

func writeRuleApprovalEpisode(
	t *testing.T, store *ledger.Store, observed time.Time, text string,
) string {
	t.Helper()
	path := filepath.Join(store.Root(), "derived", "generations", "episodes-rule-approval-fixture")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	episodeID := sha256.Sum256([]byte("rule-approval-episode"))
	statementID := sha256.Sum256([]byte("rule-approval-statement"))
	episode := episodes.Episode{
		SchemaVersion: episodes.EpisodeSchemaVersion,
		EpisodeID:     "episode-" + hex.EncodeToString(episodeID[:]),
		Agent:         ledger.AgentCodex, ThreadID: "rule-memory-thread",
		StartedAt: observed, EndedAt: observed.Add(time.Minute),
		FirstEventID: "rule-memory-event", LastEventID: "rule-memory-event",
		EventCounts:  map[string]int{"user_message": 1},
		Completeness: episodes.EpisodeCompleteness{Status: ledger.CompletenessComplete},
		Statements: []episodes.Statement{{
			StatementID: "statement-" + hex.EncodeToString(statementID[:]),
			Kind:        "goal", Text: text, EvidenceEventIDs: []string{"rule-memory-event"},
			FirstSeenAt: observed, LastSeenAt: observed,
		}},
		Privacy: "local_only",
	}
	episodesPath := filepath.Join(path, "episodes.jsonl")
	file, err := os.OpenFile(episodesPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(episode); err != nil {
		_ = file.Close()
		t.Fatal(err)
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
	evidence := store.Verify()
	if len(evidence.Issues) != 0 {
		t.Fatalf("fixture evidence did not verify: %+v", evidence)
	}
	manifest := episodes.Manifest{
		SchemaVersion: episodes.ManifestSchemaVersion, DerivationVersion: episodes.DerivationVersion,
		Privacy: "local_only", SourceRecords: evidence.RecordsChecked,
		SourceLastRecordHash: evidence.LastRecordHash,
		Episodes:             1, TimelineFile: "timeline.jsonl",
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

func authorizationRequest(
	fixture ruleApprovalFixture, action Action, expected Status,
) Request {
	return Request{
		SchemaVersion: RequestSchemaVersion, Approver: Approver{Kind: "human", ID: "owner"},
		MemoryID: fixture.memoryID, RevisionID: fixture.revisionID, Surface: SurfaceSkill,
		Target:         "skill:reporting",
		ExpectedStatus: expected, Action: action,
		Reason: "This exact rule change was reviewed and approved.",
	}
}
