package reviewpacket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const packetRollout = `{"timestamp":"2026-08-13T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555"}}
{"timestamp":"2026-08-13T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Please remember that generated reports stay local."}}
{"timestamp":"2026-08-13T00:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"Understood."}}
`

func TestPacketBindsDisplayedCandidateAndSurvivesSeparateReviewLedger(t *testing.T) {
	store, generation := packetFixture(t)
	built, err := Build(store, BuildOptions{
		Generation: generation.Name,
		Statuses:   []candidates.ReviewStatus{candidates.StatusReviewReady},
		Now:        func() time.Time { return time.Date(2026, 8, 13, 1, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !built.Written || built.Items != 1 || built.Generation != generation.Name {
		t.Fatalf("unexpected packet build result: %+v", built)
	}
	packet, err := Open(store, built.PacketID)
	if err != nil {
		t.Fatal(err)
	}
	if packet.PacketID != built.PacketID || len(packet.Items) != 1 {
		t.Fatalf("unexpected packet: %+v", packet)
	}
	candidateID := packet.Items[0].Candidate.CandidateID
	resolvedPacket, resolvedItem, err := ResolveCandidate(store, built.PacketID, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPacket.PacketID != packet.PacketID || resolvedItem.TextSHA256 != packet.Items[0].TextSHA256 {
		t.Fatalf("resolved candidate changed: packet=%+v item=%+v", resolvedPacket, resolvedItem)
	}

	if _, err := review.Apply(store, generation.Name, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: review.ReviewerKindCallerAttestation, ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID:            candidateID,
			CandidateContentSHA256: resolvedItem.Candidate.ContentSHA256,
			ExpectedStatus:         review.StatusPending,
			Action:                 review.ActionValidate,
			Scope:                  &review.Scope{Kind: review.ScopeProject, Value: "agent-memory-system"},
			Basis:                  []review.Basis{review.BasisExplicitRemember},
			Reason:                 "The exact packet text and its user evidence were reviewed.",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveCandidate(store, built.PacketID, candidateID); err != nil {
		t.Fatalf("separate review ledger invalidated the displayed packet: %v", err)
	}
}

func TestPacketFailsClosedAfterEvidenceAdvanceOrTamper(t *testing.T) {
	t.Run("evidence advance", func(t *testing.T) {
		store, generation := packetFixture(t)
		built, err := Build(store, BuildOptions{
			Generation: generation.Name,
			Statuses:   []candidates.ReviewStatus{candidates.StatusReviewReady},
		})
		if err != nil {
			t.Fatal(err)
		}
		packet, err := Open(store, built.PacketID)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC)
		payload := ledger.InlinePayload("utf-8", "text/plain", "new instruction")
		if _, err := store.Append(ledger.Event{
			SchemaVersion: ledger.SchemaVersion,
			EventID:       "packet-late-event",
			Kind:          ledger.KindUserMessage,
			ObservedAt:    now,
			RecordedAt:    now,
			Source: ledger.Source{
				Agent:          ledger.AgentCodex,
				Adapter:        "test",
				AdapterVersion: "v1",
				DeviceID:       store.DeviceID(),
				ThreadID:       "packet-late-thread",
			},
			Payload:      &payload,
			Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Privacy:      ledger.Privacy{Classification: "local_only"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ResolveCandidate(store, built.PacketID, packet.Items[0].Candidate.CandidateID); err == nil ||
			!strings.Contains(err.Error(), "current evidence ledger prefix") {
			t.Fatalf("stale review packet was accepted: %v", err)
		}
	})

	t.Run("canonical bytes", func(t *testing.T) {
		store, generation := packetFixture(t)
		built, err := Build(store, BuildOptions{
			Generation: generation.Name,
			Statuses:   []candidates.ReviewStatus{candidates.StatusReviewReady},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.Root(), filepath.FromSlash(built.RelativePath))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(store, built.PacketID); err == nil || !strings.Contains(err.Error(), "canonical JSON") {
			t.Fatalf("non-canonical review packet was accepted: %v", err)
		}
	})
}

func packetFixture(t *testing.T) (*ledger.Store, candidates.Generation) {
	t.Helper()
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "rollout-2026-08-13T00-00-00-11111111-2222-3333-4444-555555555555.jsonl")
	if err := os.WriteFile(source, []byte(packetRollout), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 0, 10, 0, 0, time.UTC)
	if _, err := codex.ImportPath(store, source, codex.Options{Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	episodeResult, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidateResult, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeResult.GenerationPath,
		ShardCount:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := candidates.OpenGeneration(store, candidateResult.GenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	return store, generation
}
