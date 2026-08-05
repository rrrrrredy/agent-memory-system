package schemas_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestReviewPackAndQueueInstancesMatchPublishedSchemas(t *testing.T) {
	hash := strings.Repeat("a", 64)
	now := time.Unix(800, 0).UTC()
	observation := candidates.Observation{
		EpisodeID: "episode-" + hash, StatementID: "statement-" + hash,
		StatementKind: "constraint", EvidenceEventIDs: []string{"event-one"},
		SupportTypes: []candidates.SupportType{candidates.SupportUserCorrection},
		FirstSeenAt:  now, LastSeenAt: now,
	}
	candidateSample := evaluation.CandidateReviewSample{
		SelectionRankSHA256: hash, CandidateID: "candidate-" + hash,
		CandidateContentSHA256: hash, SemanticKeySHA256: hash,
		Kind: candidates.KindCorrection, Text: "Keep evidence local.",
		Polarity:           candidates.PolarityAffirmative,
		Scope:              candidates.CandidateScope{Agent: ledger.AgentCodex, Status: candidates.ScopeUnconfirmed},
		CorpusSupportTypes: []candidates.SupportType{candidates.SupportUserCorrection},
		GlobalSupportTypes: []candidates.SupportType{candidates.SupportUserCorrection},
		CorpusObservations: []candidates.Observation{observation},
		GlobalEpisodeCount: 1, GlobalEvidenceEventCount: 1,
		FirstSeenAt: now, LastSeenAt: now,
		Validation: candidates.Validation{
			Status: candidates.StatusReviewReady, Reasons: []string{"user_correction"},
			ScopeConfirmationRequired: true, AutomaticPromotionEligible: false,
		},
		RequiresExplicitRuleChangeApproval: false, Privacy: "local_only",
	}
	statement := episodes.Statement{
		StatementID: "statement-" + hash, Kind: "constraint", Text: "Keep evidence local.",
		EvidenceEventIDs: []string{"event-one"}, FirstSeenAt: now, LastSeenAt: now,
	}
	compactionSample := evaluation.CompactionReviewSample{
		SelectionRankSHA256: hash, EpisodeID: "episode-" + hash, Agent: ledger.AgentCodex,
		CheckpointID: "compaction-checkpoint-" + hash, EventIDs: []string{"event-two"},
		ObservedAt: now, RepresentationAvailable: true, Status: episodes.ContinuityPreserved,
		TotalChecks: 1,
		CheckPopulation: map[string]int{
			string(episodes.StatementPreserved):                 1,
			string(episodes.StatementCorrectionAfterCompaction): 0,
			string(episodes.StatementMissingFromRepresentation): 0,
		},
		Checks: []evaluation.CompactionCheckReview{{
			SelectionRankSHA256: hash,
			Check: episodes.ContinuityCheck{
				StatementID: statement.StatementID, Coverage: 1, Status: episodes.StatementPreserved,
			},
			Statement: &statement,
		}},
		Privacy: "local_only",
	}
	candidatePopulation := map[string]int{
		evaluation.CandidateStratumExplicitRemember:     0,
		evaluation.CandidateStratumUserCorrection:       1,
		evaluation.CandidateStratumStableRepetition:     0,
		evaluation.CandidateStratumCompactionCorrection: 0,
		evaluation.CandidateStratumSemanticConflict:     0,
		evaluation.CandidateStratumUntrustedInstruction: 0,
	}
	compactionPopulation := map[string]int{
		evaluation.CompactionStratumDriftEvidence:        0,
		evaluation.CompactionStratumAtRisk:               0,
		evaluation.CompactionStratumPreserved:            1,
		evaluation.CompactionStratumInsufficientEvidence: 0,
	}
	candidateSamples := map[string][]evaluation.CandidateReviewSample{
		evaluation.CandidateStratumExplicitRemember:     {},
		evaluation.CandidateStratumUserCorrection:       {candidateSample},
		evaluation.CandidateStratumStableRepetition:     {},
		evaluation.CandidateStratumCompactionCorrection: {},
		evaluation.CandidateStratumSemanticConflict:     {},
		evaluation.CandidateStratumUntrustedInstruction: {},
	}
	compactionSamples := map[string][]evaluation.CompactionReviewSample{
		evaluation.CompactionStratumDriftEvidence:        {},
		evaluation.CompactionStratumAtRisk:               {},
		evaluation.CompactionStratumPreserved:            {compactionSample},
		evaluation.CompactionStratumInsufficientEvidence: {},
	}
	pack := evaluation.LegacyReviewPack{
		SchemaVersion: evaluation.LegacyReviewPackSchema, PackID: "review-pack-" + hash,
		CorpusID: "corpus-" + hash, CorpusContentSHA256: hash,
		CandidateGeneration: "candidates-v1", CandidateManifestSHA256: hash, CandidatesSHA256: hash,
		EpisodeGeneration: "episodes-v1", EpisodesSHA256: hash,
		SourceEvidencePrefix: candidates.EvidencePrefix{Records: 1, LastRecordHash: hash},
		SamplePerStratum:     20, CandidatePopulation: candidatePopulation,
		CompactionPopulation: compactionPopulation, CandidateSamples: candidateSamples,
		CompactionSamples: compactionSamples, Privacy: "local_only",
	}
	itemID := "review-item-" + hash
	queue := evaluation.LegacyReviewQueue{
		SchemaVersion: evaluation.LegacyReviewQueueSchema, QueueID: "review-queue-" + hash,
		PackID: pack.PackID, PackSHA256: hash, CorpusID: pack.CorpusID,
		CandidateGeneration: pack.CandidateGeneration, EpisodeGeneration: pack.EpisodeGeneration,
		CandidateLimit: 20, CompactionLimit: 20,
		CandidateItems: []evaluation.CandidateReviewQueueItem{{
			ItemID: itemID, Stratum: evaluation.CandidateStratumUserCorrection,
			Sample: candidateSample,
		}},
		CompactionItems: []evaluation.CompactionReviewQueueItem{{
			ItemID: itemID, Stratum: evaluation.CompactionStratumPreserved,
			Sample: compactionSample,
		}},
		Privacy: "local_only",
	}
	packResult := evaluation.LegacyReviewPackResult{
		SchemaVersion: evaluation.LegacyReviewPackResultSchema, PackID: pack.PackID,
		PackPath: "local-pack.json", PackSHA256: hash, CandidateSamples: 1,
		UniqueCandidates: 1, CompactionSamples: 1, CandidatePopulation: candidatePopulation,
		CompactionPopulation: compactionPopulation, Privacy: "local_only",
	}
	queueResult := evaluation.LegacyReviewQueueResult{
		SchemaVersion: evaluation.LegacyReviewQueueResultSchema, QueueID: queue.QueueID,
		QueuePath: "local-queue.json", MarkdownPath: "local-review.md", CandidateItems: 1,
		CompactionItems: 1, Privacy: "local_only",
	}

	instances := map[string]any{
		"legacy-regression-review-pack.schema.json":         pack,
		"legacy-regression-review-pack-result.schema.json":  packResult,
		"legacy-regression-review-queue.schema.json":        queue,
		"legacy-regression-review-queue-result.schema.json": queueResult,
	}
	for schemaName, instance := range instances {
		t.Run(schemaName, func(t *testing.T) {
			validatePublishedInstance(t, schemaName, instance)
		})
	}
}
