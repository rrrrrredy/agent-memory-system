package promotion

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
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

func TestPromotionSupersessionAndRevocation(t *testing.T) {
	fixture := newPromotionFixture(t)
	initial := fixture.byText["Please remember that reports stay local"]
	updated := fixture.byText["Reports stay local"]

	promoted, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, initial,
		fixture.reviewRecords[initial.CandidateID], nil, initial.Text,
	))
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Revision.Status != StatusActive || promoted.Revision.Text != initial.Text ||
		promoted.Revision.RuleChangeAuthorization != "not_granted" {
		t.Fatalf("unexpected promoted revision: %+v", promoted.Revision)
	}
	status, err := GetStatus(fixture.store, promoted.Revision.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusActive || status.RevisionCount != 1 ||
		!status.SourceReviewCurrent || !status.ExportEligible {
		t.Fatalf("unexpected promoted status: %+v", status)
	}
	if _, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, initial,
		fixture.reviewRecords[initial.CandidateID], nil, initial.Text,
	)); err == nil || !strings.Contains(err.Error(), "existing memory") {
		t.Fatalf("duplicate initial promotion was accepted: %v", err)
	}
	if _, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, updated,
		fixture.reviewRecords[updated.CandidateID], nil, updated.Text,
	)); err == nil || !strings.Contains(err.Error(), "semantically matching") {
		t.Fatalf("related candidate created a duplicate memory instead of superseding: %v", err)
	}

	superseded, err := Apply(fixture.store, promotionRequest(
		ActionSupersede, promoted.Revision.MemoryID, promoted.Revision.RevisionID,
		fixture.generation, updated, fixture.reviewRecords[updated.CandidateID], nil, updated.Text,
	))
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Revision.ParentRevisionID != promoted.Revision.RevisionID ||
		superseded.Revision.Text != updated.Text {
		t.Fatalf("unexpected supersession: %+v", superseded.Revision)
	}
	histories, err := ListHistories(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(histories) != 1 || histories[0].MemoryID != promoted.Revision.MemoryID ||
		len(histories[0].Revisions) != 2 ||
		histories[0].Revisions[1].RevisionID != superseded.Revision.RevisionID {
		t.Fatalf("unexpected promotion history: %+v", histories)
	}
	wasCurrent, err := RevisionWasCurrentAt(
		fixture.store, promoted.Revision.MemoryID, promoted.Revision.RevisionID,
		promoted.Revision.RecordedAt,
	)
	if err != nil || !wasCurrent {
		t.Fatalf("initial revision was not current at creation: %v, %v", wasCurrent, err)
	}
	wasCurrent, err = RevisionWasCurrentAt(
		fixture.store, promoted.Revision.MemoryID, promoted.Revision.RevisionID,
		superseded.Revision.RecordedAt,
	)
	if err != nil || wasCurrent {
		t.Fatalf("superseded revision remained current at child creation: %v, %v", wasCurrent, err)
	}
	stale := promotionRequest(
		ActionSupersede, promoted.Revision.MemoryID, promoted.Revision.RevisionID,
		fixture.generation, updated, fixture.reviewRecords[updated.CandidateID], nil, updated.Text,
	)
	if _, err := Apply(fixture.store, stale); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale supersession was accepted: %v", err)
	}

	revoked, err := Apply(fixture.store, Request{
		SchemaVersion: RequestSchemaVersion, Approver: humanApprover(), Action: ActionRevoke,
		MemoryID: promoted.Revision.MemoryID, ExpectedRevisionID: superseded.Revision.RevisionID,
		Reason: "This memory is no longer applicable.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Revision.Status != StatusRevoked || revoked.Revision.Text != "" ||
		revoked.Revision.Source != nil {
		t.Fatalf("unexpected revocation: %+v", revoked.Revision)
	}
	status, err = GetStatus(fixture.store, promoted.Revision.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusRevoked || status.RevisionCount != 3 || status.ExportEligible {
		t.Fatalf("unexpected revoked status: %+v", status)
	}
	report := Verify(fixture.store)
	if report.RecordsChecked != 3 || report.MemoriesChecked != 1 || len(report.Issues) != 0 {
		t.Fatalf("promotion ledger did not verify: %+v", report)
	}
}

func TestPromotionRequiresExactRedactionAndCurrentReview(t *testing.T) {
	fixture := newPromotionFixture(t)
	secretCandidate := fixture.byText[fixture.secretText]
	scan, err := ScanCandidate(fixture.store, fixture.generation, secretCandidate.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Report.Findings) == 0 {
		t.Fatal("synthetic credential was not detected")
	}
	encodedScan, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedScan), fixture.secretValue) {
		t.Fatal("scan result echoed the sensitive value")
	}
	request := promotionRequest(
		ActionPromote, "", "", fixture.generation, secretCandidate,
		fixture.reviewRecords[secretCandidate.CandidateID], nil, secretCandidate.Text,
	)
	if _, err := Apply(fixture.store, request); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Fatalf("unredacted sensitive candidate was promoted: %v", err)
	}
	redactions := exactRedactions(secretCandidate.Text, scan.Report)
	redacted, err := secretscan.ApplyRedactions(secretCandidate.Text, scan.Report, redactions)
	if err != nil {
		t.Fatal(err)
	}
	request.Redactions = redactions
	request.ExpectedTextSHA256 = strings.Repeat("0", 64)
	if _, err := Apply(fixture.store, request); err == nil || !strings.Contains(err.Error(), "expected_text") {
		t.Fatalf("promotion accepted unreviewed redacted text: %v", err)
	}
	request.ExpectedTextSHA256 = textDigest(redacted)
	promoted, err := Apply(fixture.store, request)
	if err != nil {
		t.Fatal(err)
	}
	encodedRevision, err := json.Marshal(promoted.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedRevision), fixture.secretValue) {
		t.Fatal("promoted revision retained the sensitive value")
	}

	if _, err := review.Apply(fixture.store, fixture.generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID:            secretCandidate.CandidateID,
			CandidateContentSHA256: secretCandidate.ContentSHA256,
			ExpectedStatus:         review.StatusValidated, Action: review.ActionReject,
			Reason: "The credential-bearing source is obsolete.",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, promoted.Revision.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceReviewCurrent || status.ExportEligible {
		t.Fatalf("obsolete review still allowed portability: %+v", status)
	}
	verification := Verify(fixture.store)
	if len(verification.Issues) != 1 || !strings.Contains(verification.Issues[0], "no longer") {
		t.Fatalf("obsolete active source was not reported: %+v", verification)
	}
	if _, err := Apply(fixture.store, Request{
		SchemaVersion: RequestSchemaVersion, Approver: humanApprover(), Action: ActionRevoke,
		MemoryID: promoted.Revision.MemoryID, ExpectedRevisionID: promoted.Revision.RevisionID,
		Reason: "Revoke the obsolete memory.",
	}); err != nil {
		t.Fatal(err)
	}
	if verification = Verify(fixture.store); len(verification.Issues) != 0 {
		t.Fatalf("revocation did not close the stale-source issue: %+v", verification)
	}
}

func TestPromotionKeepsRuleAuthorizationSeparate(t *testing.T) {
	fixture := newPromotionFixture(t)
	candidate := fixture.byText["Please remember that Skills changes require approval"]
	if !candidate.RequiresExplicitRuleChangeApproval {
		t.Fatal("fixture candidate did not require explicit rule approval")
	}
	result, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, candidate,
		fixture.reviewRecords[candidate.CandidateID], nil, candidate.Text,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Revision.RequiresExplicitRuleChangeApproval ||
		result.Revision.RuleChangeAuthorization != "not_granted" {
		t.Fatalf("promotion implicitly authorized a rule change: %+v", result.Revision)
	}
	unknown := `{"schema_version":"memory-promotion-request/v1alpha1","approver":{"kind":"human","id":"owner"},"action":"revoke","memory_id":"memory-` +
		strings.Repeat("0", 64) + `","expected_revision_id":"memory-revision-` + strings.Repeat("1", 64) +
		`","reason":"Reviewed.","rule_change_authorization":"granted"}`
	if _, err := DecodeRequest(strings.NewReader(unknown)); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("promotion request smuggled rule authorization: %v", err)
	}
}

func TestNewEvidenceInvalidatesOldGenerationForPortability(t *testing.T) {
	fixture := newPromotionFixture(t)
	candidate := fixture.byText["Please remember that reports stay local"]
	promoted, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, candidate,
		fixture.reviewRecords[candidate.CandidateID], nil, candidate.Text,
	))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 4, 0, 0, 0, time.UTC)
	payload := ledger.InlinePayload("utf-8", "text/plain", "A later instruction requires review.")
	if _, err := fixture.store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "later-evidence-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
			DeviceID: fixture.store.DeviceID(), ThreadID: "later-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := GetStatus(fixture.store, promoted.Revision.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if status.SourceReviewCurrent || status.ExportEligible {
		t.Fatalf("old generation stayed eligible after new evidence: %+v", status)
	}
	verification := Verify(fixture.store)
	if len(verification.Issues) != 1 || !strings.Contains(verification.Issues[0], "current evidence") {
		t.Fatalf("old generation was not reported: %+v", verification)
	}
	other := fixture.byText["Please remember that Skills changes require approval"]
	if _, err := Apply(fixture.store, promotionRequest(
		ActionPromote, "", "", fixture.generation, other,
		fixture.reviewRecords[other.CandidateID], nil, other.Text,
	)); err == nil || !strings.Contains(err.Error(), "current evidence ledger prefix") {
		t.Fatalf("old candidate generation was accepted for a new promotion: %v", err)
	}
	if _, err := Apply(fixture.store, Request{
		SchemaVersion: RequestSchemaVersion, Approver: humanApprover(), Action: ActionRevoke,
		MemoryID: promoted.Revision.MemoryID, ExpectedRevisionID: promoted.Revision.RevisionID,
		Reason: "Revoke memory derived before the latest evidence.",
	}); err != nil {
		t.Fatal(err)
	}
	if verification = Verify(fixture.store); len(verification.Issues) != 0 {
		t.Fatalf("revocation did not close current-evidence issue: %+v", verification)
	}
}

func TestConcurrentPromotionHasOneWinnerAndTamperingFailsSemanticReplay(t *testing.T) {
	fixture := newPromotionFixture(t)
	candidate := fixture.byText["Please remember that reports stay local"]
	request := promotionRequest(
		ActionPromote, "", "", fixture.generation, candidate,
		fixture.reviewRecords[candidate.CandidateID], nil, candidate.Text,
	)
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, err := Apply(fixture.store, request)
			errorsSeen <- err
		}()
	}
	close(start)
	succeeded := 0
	for index := 0; index < 2; index++ {
		if err := <-errorsSeen; err == nil {
			succeeded++
		} else if !strings.Contains(err.Error(), "locked") && !strings.Contains(err.Error(), "existing") {
			t.Fatalf("unexpected concurrent promotion failure: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent promotion successes = %d, want 1", succeeded)
	}

	path := filepath.Join(fixture.store.Root(), filepath.FromSlash(promotionLedgerPath))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record Record
	if err := json.Unmarshal(data[:len(data)-1], &record); err != nil {
		t.Fatal(err)
	}
	record.Event.Revision.Text = "Keep reports on the local device."
	record.Event.Revision.TextSHA256 = textDigest(record.Event.Revision.Text)
	record.Event.Revision.Scan.RedactedTextSHA256 = record.Event.Revision.TextSHA256
	record.Event.Revision.RevisionID, err = revisionID(record.Event.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := requestFromRevision(record.Event)
	if err != nil {
		t.Fatal(err)
	}
	record.Event.RequestSHA256, err = hashRequest(reconstructed)
	if err != nil {
		t.Fatal(err)
	}
	record, err = makeRecord(record.Event, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	tampered = append(tampered, '\n')
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	verification := Verify(fixture.store)
	if len(verification.Issues) == 0 || !strings.Contains(verification.Issues[0], "not reproducible") {
		t.Fatalf("semantically forged promotion verified: %+v", verification)
	}
}

func TestDecodePromotionRequestRejectsTrailingData(t *testing.T) {
	if _, err := DecodeRequest(strings.NewReader(`{} {}`)); err == nil ||
		!strings.Contains(err.Error(), "more than one") {
		t.Fatalf("trailing promotion request value was accepted: %v", err)
	}
}

type promotionFixture struct {
	store         *ledger.Store
	generation    string
	byText        map[string]candidates.Candidate
	reviewRecords map[string]string
	secretText    string
	secretValue   string
}

func newPromotionFixture(t *testing.T) promotionFixture {
	t.Helper()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	secretValue := "ghp_" + strings.Repeat("A1", 18)
	secretText := "Please remember token=" + secretValue
	statements := []episodes.Statement{
		promotionStatement("initial", "goal", "Please remember that reports stay local", "initial-event", base),
		promotionStatement("updated", "correction", "Reports stay local", "updated-event", base.Add(time.Second)),
		promotionStatement("secret", "goal", secretText, "secret-event", base.Add(2*time.Second)),
		promotionStatement("rule", "goal", "Please remember that Skills changes require approval", "rule-event", base.Add(3*time.Second)),
	}
	appendPromotionEvidence(t, store, base, statements)
	episodeGeneration := writePromotionEpisodes(t, store, []episodes.Episode{{
		SchemaVersion: episodes.EpisodeSchemaVersion,
		EpisodeID:     promotionID("episode", "one"), Agent: ledger.AgentCodex,
		ThreadID: "promotion-thread", StartedAt: base, EndedAt: base.Add(time.Minute),
		FirstEventID: "initial-event", LastEventID: "rule-event",
		EventCounts:  map[string]int{"user_message": len(statements)},
		Completeness: episodes.EpisodeCompleteness{Status: ledger.CompletenessComplete},
		Statements:   statements, Privacy: "local_only",
	}}, "episodes-promotion-fixture")
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
	generation := filepath.Base(built.GenerationPath)
	reviewRecords := map[string]string{}
	for _, statement := range statements {
		candidate := byText[statement.Text]
		basis := review.BasisExplicitRemember
		if statement.Kind == "correction" {
			basis = review.BasisUserCorrection
		}
		result, err := review.Apply(store, generation, review.Request{
			SchemaVersion: review.RequestSchemaVersion,
			Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
			Transitions: []review.TransitionRequest{{
				CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
				ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
				Scope: &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
				Basis: []review.Basis{basis}, Reason: "Evidence and scope reviewed.",
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		reviewRecords[candidate.CandidateID] = result.RecordSHA256
	}
	return promotionFixture{
		store: store, generation: generation, byText: byText, reviewRecords: reviewRecords,
		secretText: secretText, secretValue: secretValue,
	}
}

func appendPromotionEvidence(
	t *testing.T, store *ledger.Store, base time.Time, statements []episodes.Statement,
) {
	t.Helper()
	events := make([]ledger.Event, 0, len(statements))
	for index, statement := range statements {
		payload := ledger.InlinePayload("utf-8", "text/plain", statement.Text)
		events = append(events, ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: statement.EvidenceEventIDs[0],
			Kind: ledger.KindUserMessage, ObservedAt: base.Add(time.Duration(index) * time.Second),
			RecordedAt: base.Add(time.Duration(index) * time.Second),
			Source: ledger.Source{
				Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
				DeviceID: store.DeviceID(), ThreadID: "promotion-thread",
			},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Privacy: ledger.Privacy{Classification: "local_only"},
		})
	}
	if _, err := store.AppendBatch(events); err != nil {
		t.Fatal(err)
	}
}

func writePromotionEpisodes(
	t *testing.T, store *ledger.Store, values []episodes.Episode, name string,
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
	for _, episode := range values {
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
	evidence := store.Verify()
	if len(evidence.Issues) != 0 {
		t.Fatalf("fixture evidence did not verify: %+v", evidence)
	}
	manifest := episodes.Manifest{
		SchemaVersion: episodes.ManifestSchemaVersion, DerivationVersion: episodes.DerivationVersion,
		Privacy: "local_only", SourceRecords: evidence.RecordsChecked,
		SourceLastRecordHash: evidence.LastRecordHash,
		Episodes:             len(values), TimelineFile: "timeline.jsonl",
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

func promotionStatement(
	name, kind, text, eventID string, observed time.Time,
) episodes.Statement {
	return episodes.Statement{
		StatementID: promotionID("statement", name), Kind: kind, Text: text,
		EvidenceEventIDs: []string{eventID}, FirstSeenAt: observed, LastSeenAt: observed,
	}
}

func promotionID(prefix, value string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + value))
	return prefix + "-" + hex.EncodeToString(digest[:])
}

func humanApprover() Approver {
	return Approver{Kind: "human", ID: "owner"}
}

func promotionRequest(
	action Action, memoryID, parent, generation string, candidate candidates.Candidate,
	reviewRecord string, redactions []secretscan.Redaction, expectedText string,
) Request {
	return Request{
		SchemaVersion: RequestSchemaVersion, Approver: humanApprover(), Action: action,
		MemoryID: memoryID, ExpectedRevisionID: parent,
		Candidate: &CandidateReference{
			Generation: generation, CandidateID: candidate.CandidateID,
			CandidateContentSHA256:  candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewRecord,
		},
		Redactions: redactions, ExpectedTextSHA256: textDigest(expectedText),
		Reason: "The reviewed candidate is safe to promote.",
	}
}

func exactRedactions(content string, report secretscan.Report) []secretscan.Redaction {
	findings := append([]secretscan.Finding(nil), report.Findings...)
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].StartByte != findings[right].StartByte {
			return findings[left].StartByte < findings[right].StartByte
		}
		return findings[left].EndByte < findings[right].EndByte
	})
	ranges := make([][2]int, 0, len(findings))
	for _, finding := range findings {
		if len(ranges) == 0 || finding.StartByte > ranges[len(ranges)-1][1] {
			ranges = append(ranges, [2]int{finding.StartByte, finding.EndByte})
			continue
		}
		if finding.EndByte > ranges[len(ranges)-1][1] {
			ranges[len(ranges)-1][1] = finding.EndByte
		}
	}
	redactions := make([]secretscan.Redaction, 0, len(ranges))
	for _, item := range ranges {
		digest := sha256.Sum256([]byte(content[item[0]:item[1]]))
		redactions = append(redactions, secretscan.Redaction{
			StartByte: item[0], EndByte: item[1], SourceSHA256: hex.EncodeToString(digest[:]),
			Replacement: "[REDACTED:secret]",
		})
	}
	return redactions
}
