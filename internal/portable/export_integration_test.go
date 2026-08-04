package portable

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

func TestExportFromVerifiedPromotionIsIdempotentAndRevocableAfterNewEvidence(t *testing.T) {
	store, promoted := newExportFixture(t)
	repository := filepath.Join(t.TempDir(), "private-memory")
	if err := InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	first, err := Export(store, repository, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionsWritten != 1 || first.RevisionsUnchanged != 0 || len(first.FilesWritten) != 1 {
		t.Fatalf("unexpected first export: %+v", first)
	}
	second, err := Export(store, repository, ExportOptions{MemoryIDs: []string{promoted.Revision.MemoryID}})
	if err != nil {
		t.Fatal(err)
	}
	if second.RevisionsWritten != 0 || second.RevisionsUnchanged != 1 {
		t.Fatalf("idempotent export changed repository: %+v", second)
	}
	data, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(first.FilesWritten[0])))
	if err != nil {
		t.Fatal(err)
	}
	for _, localOnly := range []string{
		"portable-export-event", "episodes-portable-export-fixture",
		promoted.RecordSHA256, promoted.Revision.Source.ReviewRecordSHA256,
	} {
		if strings.Contains(string(data), localOnly) {
			t.Fatalf("portable file leaked local proof %q", localOnly)
		}
	}

	now := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)
	payload := ledger.InlinePayload("utf-8", "text/plain", "An unrelated later instruction")
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "later-evidence-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
			DeviceID: store.DeviceID(), ThreadID: "portable-export-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(store, repository, ExportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not eligible") {
		t.Fatalf("stale active promotion was exported: %v", err)
	}
	revoked, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionRevoke, MemoryID: promoted.Revision.MemoryID,
		ExpectedRevisionID: promoted.Revision.RevisionID,
		Reason:             "The memory is no longer active.",
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := Export(store, repository, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if third.RevisionsWritten != 1 || third.RevisionsUnchanged != 1 ||
		third.RevisionsProjected != 2 {
		t.Fatalf("revocation did not close the portable chain: %+v", third)
	}
	report := VerifyRepository(repository)
	if len(report.Issues) != 0 || report.RevokedMemories != 1 || report.ActiveMemories != 0 {
		t.Fatalf("revoked portable repository did not verify: %+v", report)
	}
	if revoked.Revision.ParentRevisionID != promoted.Revision.RevisionID {
		t.Fatalf("unexpected local revocation parent: %+v", revoked.Revision)
	}
}

func TestExportLockRejectsConcurrentWriterAndRecovers(t *testing.T) {
	store, _ := newExportFixture(t)
	repository := filepath.Join(t.TempDir(), "private-memory")
	if err := InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireRepositoryLock(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Export(store, repository, ExportOptions{}); err == nil ||
		!strings.Contains(err.Error(), "repository is locked") {
		t.Fatalf("concurrent exporter was not rejected: %v", err)
	}
	if report := VerifyRepository(repository); len(report.Issues) != 0 ||
		report.RevisionsChecked != 0 {
		t.Fatalf("lock contention changed the repository: %+v", report)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	result, err := Export(store, repository, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevisionsWritten != 1 {
		t.Fatalf("export did not recover after lock release: %+v", result)
	}
}

func newExportFixture(t *testing.T) (*ledger.Store, promotion.ApplyResult) {
	t.Helper()
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	text := "Please remember that reports stay local"
	payload := ledger.InlinePayload("utf-8", "text/plain", text)
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "portable-export-event",
		Kind: ledger.KindUserMessage, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "test", AdapterVersion: "v1",
			DeviceID: store.DeviceID(), ThreadID: "portable-export-thread",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		t.Fatal(err)
	}
	episodeGeneration := writeExportEpisode(t, store, now, text)
	built, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeGeneration, ShardCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := readExportCandidate(t, built.GenerationPath, text)
	generation := filepath.Base(built.GenerationPath)
	reviewed, err := review.Apply(store, generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
			Scope: &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis: []review.Basis{review.BasisExplicitRemember}, Reason: "The memory was reviewed.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(text))
	promoted, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation, CandidateID: candidate.CandidateID,
			CandidateContentSHA256:  candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewed.RecordSHA256,
		},
		ExpectedTextSHA256: hex.EncodeToString(digest[:]),
		Reason:             "The reviewed memory is safe to promote.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, promoted
}

func writeExportEpisode(t *testing.T, store *ledger.Store, observed time.Time, text string) string {
	t.Helper()
	path := filepath.Join(store.Root(), "derived", "generations", "episodes-portable-export-fixture")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	episodeHash := sha256.Sum256([]byte("portable-export-episode"))
	statementHash := sha256.Sum256([]byte("portable-export-statement"))
	episode := episodes.Episode{
		SchemaVersion: episodes.EpisodeSchemaVersion,
		EpisodeID:     "episode-" + hex.EncodeToString(episodeHash[:]),
		Agent:         ledger.AgentCodex, ThreadID: "portable-export-thread",
		StartedAt: observed, EndedAt: observed.Add(time.Minute),
		FirstEventID: "portable-export-event", LastEventID: "portable-export-event",
		EventCounts:  map[string]int{"user_message": 1},
		Completeness: episodes.EpisodeCompleteness{Status: ledger.CompletenessComplete},
		Statements: []episodes.Statement{{
			StatementID: "statement-" + hex.EncodeToString(statementHash[:]),
			Kind:        "goal", Text: text, EvidenceEventIDs: []string{"portable-export-event"},
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
	episodeDigest := sha256.Sum256(episodeData)
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
		EpisodesSHA256: hex.EncodeToString(episodeDigest[:]),
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

func readExportCandidate(t *testing.T, generationPath, text string) candidates.Candidate {
	t.Helper()
	file, err := os.Open(filepath.Join(generationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var candidate candidates.Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if candidate.Text == text {
			return candidate
		}
	}
	t.Fatal("export fixture candidate was not derived")
	return candidates.Candidate{}
}
