package evaluation

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	CorpusManifestSchemaVersion   = "legacy-corpus-manifest/v1alpha2"
	CorpusFreezeResultSchema      = "legacy-corpus-freeze-result/v1alpha1"
	CorpusVerifySchemaVersion     = "legacy-corpus-verification/v1alpha1"
	EvaluationInputSchemaVersion  = "learning-evaluation-input/v1alpha2"
	EvaluationReportSchema        = "learning-evaluation-report/v1alpha2"
	EvaluationVersion             = "learning-evaluation/v1alpha2"
	EvaluationAttestationSchema   = "learning-evaluation-attestation/v1alpha2"
	AttestationResultSchema       = "learning-evaluation-attestation-result/v1alpha1"
	TaskAttemptRequestSchema      = "task-attempt-request/v1alpha2"
	TaskAttemptContractSchema     = "task-attempt-contract/v1alpha2"
	TaskAttemptVerdictSchema      = "task-attempt-verdict/v1alpha2"
	TaskAttemptReceiptSchema      = "task-attempt-receipt/v1alpha2"
	TaskAttemptResultSchema       = "task-attempt-record-result/v1alpha1"
	TaskAttemptVerificationSchema = "task-attempt-verification/v1alpha1"
	TaskAttemptDraftSchema        = "task-attempt-draft/v1alpha1"
	TaskAttemptPreregisterSchema  = "task-attempt-preregistration/v1alpha1"
	TaskAttemptObservationSchema  = "task-attempt-observation-result/v1alpha1"
	EvaluationPopulationSchema    = "learning-evaluation-population/v1alpha2"
	OracleRegistrySchema          = "task-oracle-registry/v1alpha2"
	OracleReplayInputSchema       = "task-oracle-replay-input/v1alpha2"
	LegacyReviewPackSchema        = "legacy-regression-review-pack/v1alpha2"
	LegacyReviewPackResultSchema  = "legacy-regression-review-pack-result/v1alpha2"
	LegacyReviewQueueSchema       = "legacy-regression-review-queue/v1alpha1"
	LegacyReviewQueueResultSchema = "legacy-regression-review-queue-result/v1alpha1"
)

type ArtifactRole string

const (
	RoleLegacyIndex  ArtifactRole = "legacy_index"
	RoleLegacyCard   ArtifactRole = "legacy_card"
	RoleLegacyLatest ArtifactRole = "legacy_latest_card"
)

type CorpusArtifact struct {
	ArtifactID       string         `json:"artifact_id"`
	Role             ArtifactRole   `json:"role"`
	RelativePath     string         `json:"relative_path"`
	SourcePathSHA256 string         `json:"source_path_sha256"`
	SnapshotEventID  string         `json:"snapshot_event_id"`
	Blob             ledger.BlobRef `json:"blob"`
}

type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type RolloutReference struct {
	SourcePathSHA256          string           `json:"source_path_sha256"`
	SessionIDs                []string         `json:"session_ids"`
	IndexReferences           int              `json:"index_references"`
	Status                    string           `json:"status"`
	SnapshotEventIDs          []string         `json:"snapshot_event_ids,omitempty"`
	MissingGapEventIDs        []string         `json:"missing_gap_event_ids,omitempty"`
	SnapshotBlobs             []ledger.BlobRef `json:"snapshot_blobs,omitempty"`
	CoveredRanges             []ByteRange      `json:"covered_ranges,omitempty"`
	ProjectedEvents           int              `json:"projected_events"`
	IncompleteProjectedEvents int              `json:"incomplete_projected_events"`
	GapEvents                 int              `json:"gap_events"`
}

type CorpusCounts struct {
	IndexEntries               int   `json:"index_entries"`
	Cards                      int   `json:"cards"`
	IndexedCards               int   `json:"indexed_cards"`
	UnindexedCards             int   `json:"unindexed_cards"`
	MissingCards               int   `json:"missing_cards"`
	UniqueSessions             int   `json:"unique_sessions"`
	RolloutReferences          int   `json:"rollout_references"`
	CapturedRollouts           int   `json:"captured_rollouts"`
	PartialRollouts            int   `json:"partial_rollouts"`
	MissingRollouts            int   `json:"missing_rollouts"`
	AccountedMissingRollouts   int   `json:"accounted_missing_rollouts,omitempty"`
	UnaccountedMissingRollouts int   `json:"unaccounted_missing_rollouts,omitempty"`
	CardBytes                  int64 `json:"card_bytes"`
	RolloutSnapshotBytes       int64 `json:"rollout_snapshot_bytes"`
	IncompleteProjectedEvents  int   `json:"incomplete_projected_events"`
	ProjectionGapEvents        int   `json:"projection_gap_events"`
	CardsWithLatestUser        int   `json:"cards_with_latest_user"`
	CardsWithLatestAssistant   int   `json:"cards_with_latest_assistant"`
}

type CorpusIssue struct {
	Code             string `json:"code"`
	ArtifactID       string `json:"artifact_id,omitempty"`
	SourcePathSHA256 string `json:"source_path_sha256,omitempty"`
	Count            int    `json:"count,omitempty"`
}

type CorpusManifest struct {
	SchemaVersion              string             `json:"schema_version"`
	CorpusID                   string             `json:"corpus_id"`
	CorpusContentSHA256        string             `json:"corpus_content_sha256"`
	Name                       string             `json:"name"`
	CreatedAt                  time.Time          `json:"created_at"`
	SourceRootSHA256           string             `json:"source_root_sha256"`
	IndexEntryLimit            int                `json:"index_entry_limit,omitempty"`
	SourceLedgerLastRecordHash string             `json:"source_ledger_last_record_hash,omitempty"`
	Artifacts                  []CorpusArtifact   `json:"artifacts"`
	Rollouts                   []RolloutReference `json:"rollouts"`
	Counts                     CorpusCounts       `json:"counts"`
	Issues                     []CorpusIssue      `json:"issues"`
	Privacy                    string             `json:"privacy"`
}

type FreezeOptions struct {
	Name            string
	IndexEntryLimit int
	Now             func() time.Time
}

type LegacyCaptureInputOptions struct {
	SuiteID         string
	RunID           string
	SystemVersion   string
	MinimumCoverage float64
	Now             func() time.Time
}

type LegacyReviewPackOptions struct {
	SamplePerStratum int
}

type LegacyReviewPack struct {
	SchemaVersion           string                              `json:"schema_version"`
	PackID                  string                              `json:"pack_id"`
	CorpusID                string                              `json:"corpus_id"`
	CorpusContentSHA256     string                              `json:"corpus_content_sha256"`
	CandidateGeneration     string                              `json:"candidate_generation"`
	CandidateManifestSHA256 string                              `json:"candidate_manifest_sha256"`
	CandidatesSHA256        string                              `json:"candidates_sha256"`
	EpisodeGeneration       string                              `json:"episode_generation"`
	EpisodesSHA256          string                              `json:"episodes_sha256"`
	SourceEvidencePrefix    candidates.EvidencePrefix           `json:"source_evidence_prefix"`
	SamplePerStratum        int                                 `json:"sample_per_stratum"`
	CorpusCounts            CorpusCounts                        `json:"corpus_counts"`
	CandidatePopulation     map[string]int                      `json:"candidate_population"`
	CompactionPopulation    map[string]int                      `json:"compaction_population"`
	CandidateSamples        map[string][]CandidateReviewSample  `json:"candidate_samples"`
	CompactionSamples       map[string][]CompactionReviewSample `json:"compaction_samples"`
	Privacy                 string                              `json:"privacy"`
}

type CandidateReviewSample struct {
	SelectionRankSHA256                string                       `json:"selection_rank_sha256"`
	CandidateID                        string                       `json:"candidate_id"`
	CandidateContentSHA256             string                       `json:"candidate_content_sha256"`
	SemanticKeySHA256                  string                       `json:"semantic_key_sha256"`
	Kind                               candidates.CandidateKind     `json:"kind"`
	Text                               string                       `json:"text"`
	Polarity                           candidates.CandidatePolarity `json:"polarity"`
	Scope                              candidates.CandidateScope    `json:"scope"`
	CorpusSupportTypes                 []candidates.SupportType     `json:"corpus_support_types"`
	GlobalSupportTypes                 []candidates.SupportType     `json:"global_support_types"`
	CorpusObservations                 []candidates.Observation     `json:"corpus_observations"`
	GlobalEpisodeCount                 int                          `json:"global_episode_count"`
	GlobalEvidenceEventCount           int                          `json:"global_evidence_event_count"`
	FirstSeenAt                        time.Time                    `json:"first_seen_at"`
	LastSeenAt                         time.Time                    `json:"last_seen_at"`
	Validation                         candidates.Validation        `json:"validation"`
	ConflictGroupID                    string                       `json:"conflict_group_id,omitempty"`
	ConflictingCandidateIDs            []string                     `json:"conflicting_candidate_ids,omitempty"`
	RelatedCandidateIDs                []string                     `json:"related_candidate_ids,omitempty"`
	RequiresExplicitRuleChangeApproval bool                         `json:"requires_explicit_rule_change_approval"`
	Privacy                            string                       `json:"privacy"`
}

type CompactionReviewSample struct {
	SelectionRankSHA256     string                     `json:"selection_rank_sha256"`
	EpisodeID               string                     `json:"episode_id"`
	Agent                   ledger.Agent               `json:"agent"`
	CheckpointID            string                     `json:"checkpoint_id"`
	EventIDs                []string                   `json:"event_ids"`
	ObservedAt              time.Time                  `json:"observed_at"`
	RepresentationEventIDs  []string                   `json:"representation_event_ids,omitempty"`
	RepresentationAvailable bool                       `json:"representation_available"`
	Status                  episodes.ContinuityStatus  `json:"status"`
	Issues                  []episodes.DerivationIssue `json:"issues,omitempty"`
	TotalChecks             int                        `json:"total_checks"`
	CheckPopulation         map[string]int             `json:"check_population"`
	Checks                  []CompactionCheckReview    `json:"checks"`
	Privacy                 string                     `json:"privacy"`
}

type CompactionCheckReview struct {
	SelectionRankSHA256 string                   `json:"selection_rank_sha256"`
	Check               episodes.ContinuityCheck `json:"check"`
	Statement           *episodes.Statement      `json:"statement,omitempty"`
}

type LegacyReviewPackResult struct {
	SchemaVersion        string         `json:"schema_version"`
	PackID               string         `json:"pack_id"`
	PackPath             string         `json:"pack_path"`
	PackSHA256           string         `json:"pack_sha256"`
	CandidateSamples     int            `json:"candidate_samples"`
	UniqueCandidates     int            `json:"unique_candidates"`
	CompactionSamples    int            `json:"compaction_samples"`
	CandidatePopulation  map[string]int `json:"candidate_population"`
	CompactionPopulation map[string]int `json:"compaction_population"`
	Reused               bool           `json:"reused"`
	Privacy              string         `json:"privacy"`
}

type LegacyReviewQueueOptions struct {
	CandidateLimit  int
	CompactionLimit int
}

type LegacyReviewQueue struct {
	SchemaVersion       string                      `json:"schema_version"`
	QueueID             string                      `json:"queue_id"`
	PackID              string                      `json:"pack_id"`
	PackSHA256          string                      `json:"pack_sha256"`
	CorpusID            string                      `json:"corpus_id"`
	CandidateGeneration string                      `json:"candidate_generation"`
	EpisodeGeneration   string                      `json:"episode_generation"`
	CandidateLimit      int                         `json:"candidate_limit"`
	CompactionLimit     int                         `json:"compaction_limit"`
	CandidateItems      []CandidateReviewQueueItem  `json:"candidate_items"`
	CompactionItems     []CompactionReviewQueueItem `json:"compaction_items"`
	Privacy             string                      `json:"privacy"`
}

type CandidateReviewQueueItem struct {
	ItemID  string                `json:"item_id"`
	Stratum string                `json:"stratum"`
	Sample  CandidateReviewSample `json:"sample"`
}

type CompactionReviewQueueItem struct {
	ItemID  string                 `json:"item_id"`
	Stratum string                 `json:"stratum"`
	Sample  CompactionReviewSample `json:"sample"`
}

type LegacyReviewQueueResult struct {
	SchemaVersion   string `json:"schema_version"`
	QueueID         string `json:"queue_id"`
	QueuePath       string `json:"queue_path"`
	MarkdownPath    string `json:"markdown_path"`
	CandidateItems  int    `json:"candidate_items"`
	CompactionItems int    `json:"compaction_items"`
	Reused          bool   `json:"reused"`
	Privacy         string `json:"privacy"`
}

// VerifiedLegacyReviewSource is the fully checked source bundle used to derive
// blind Agent-assessment inputs. The byte slices bind the exact immutable
// artifacts rather than only their decoded representation.
type VerifiedLegacyReviewSource struct {
	Queue      LegacyReviewQueue
	QueueBytes []byte
	Pack       LegacyReviewPack
	PackBytes  []byte
}

type FreezeResult struct {
	SchemaVersion     string        `json:"schema_version"`
	CorpusID          string        `json:"corpus_id"`
	ManifestPath      string        `json:"manifest_path"`
	ManifestSHA256    string        `json:"manifest_sha256"`
	FreezeEventID     string        `json:"freeze_event_id"`
	SnapshotsAppended int           `json:"snapshots_appended"`
	SnapshotsReused   int           `json:"snapshots_reused"`
	Counts            CorpusCounts  `json:"counts"`
	Issues            []CorpusIssue `json:"issues"`
	Reused            bool          `json:"reused"`
	Privacy           string        `json:"privacy"`
}

type CorpusVerificationReport struct {
	SchemaVersion           string   `json:"schema_version"`
	CorpusID                string   `json:"corpus_id"`
	ArtifactsChecked        int      `json:"artifacts_checked"`
	RolloutsChecked         int      `json:"rollouts_checked"`
	SnapshotEventsChecked   int      `json:"snapshot_events_checked"`
	MissingGapEventsChecked int      `json:"missing_gap_events_checked,omitempty"`
	Issues                  []string `json:"issues"`
	Privacy                 string   `json:"privacy"`
}

type CaseCategory string

const (
	CategoryCaptureCoverage    CaseCategory = "capture_coverage"
	CategoryFalseMemory        CaseCategory = "false_memory"
	CategoryRepeatedCorrection CaseCategory = "repeated_correction"
	CategoryCompactionDrift    CaseCategory = "compaction_drift"
	CategoryRetrievalCost      CaseCategory = "retrieval_cost"
	CategoryPairedOutcome      CaseCategory = "paired_outcome"
)

type EvidenceReference struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

type EvaluationCase struct {
	CaseID        string                    `json:"case_id"`
	Category      CaseCategory              `json:"category"`
	Agent         ledger.Agent              `json:"agent"`
	Evidence      []EvidenceReference       `json:"evidence"`
	Capture       *CaptureMeasurement       `json:"capture,omitempty"`
	Memory        *MemoryMeasurement        `json:"memory,omitempty"`
	Correction    *CorrectionMeasurement    `json:"correction,omitempty"`
	Compaction    *CompactionMeasurement    `json:"compaction,omitempty"`
	Retrieval     *RetrievalMeasurement     `json:"retrieval,omitempty"`
	PairedOutcome *PairedOutcomeMeasurement `json:"paired_outcome,omitempty"`
}

type Attestor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type EvaluationAttestation struct {
	SchemaVersion string                    `json:"schema_version"`
	AttestationID string                    `json:"attestation_id"`
	CaseID        string                    `json:"case_id"`
	Category      CaseCategory              `json:"category"`
	Agent         ledger.Agent              `json:"agent"`
	Attestor      Attestor                  `json:"attestor"`
	AttestedAt    time.Time                 `json:"attested_at"`
	Reason        string                    `json:"reason"`
	Capture       *CaptureMeasurement       `json:"capture,omitempty"`
	Memory        *MemoryMeasurement        `json:"memory,omitempty"`
	Correction    *CorrectionMeasurement    `json:"correction,omitempty"`
	Compaction    *CompactionExpectation    `json:"compaction,omitempty"`
	Retrieval     *RetrievalMeasurement     `json:"retrieval,omitempty"`
	PairedOutcome *PairedOutcomeMeasurement `json:"paired_outcome,omitempty"`
}

type AttestationResult struct {
	SchemaVersion string `json:"schema_version"`
	AttestationID string `json:"attestation_id"`
	EventID       string `json:"event_id"`
	RecordHash    string `json:"record_hash"`
	Reused        bool   `json:"reused"`
	Privacy       string `json:"privacy"`
}

type CaptureMeasurement struct {
	Unit             CaptureUnit `json:"unit"`
	Expected         int         `json:"expected"`
	Complete         int         `json:"complete"`
	Partial          int         `json:"partial"`
	Missing          int         `json:"missing"`
	AccountedMissing int         `json:"accounted_missing,omitempty"`
}

type CaptureUnit string

const (
	CaptureUnitNone            CaptureUnit = "none"
	CaptureUnitEvidenceEvents  CaptureUnit = "evidence_events"
	CaptureUnitLegacyRollouts  CaptureUnit = "legacy_rollouts"
	CaptureUnitSourceInventory CaptureUnit = "source_inventory_items"
)

type MemoryLabel string

const (
	MemorySupported   MemoryLabel = "supported"
	MemoryIncorrect   MemoryLabel = "incorrect"
	MemoryUnsupported MemoryLabel = "unsupported"
	MemoryStale       MemoryLabel = "stale"
	MemoryUnknown     MemoryLabel = "unknown"
)

type MemoryMeasurement struct {
	MemoryID  string      `json:"memory_id"`
	Label     MemoryLabel `json:"label"`
	Active    bool        `json:"active"`
	Retrieved bool        `json:"retrieved"`
}

type CorrectionMeasurement struct {
	SemanticKeySHA256              string   `json:"semantic_key_sha256"`
	InitialCorrectionAttemptID     string   `json:"initial_correction_attempt_id"`
	AttemptIDs                     []string `json:"attempt_ids"`
	EligibleFollowupOpportunities  int      `json:"eligible_followup_opportunities"`
	RepeatedCorrections            int      `json:"repeated_corrections"`
	RepeatedCorrectionsAfterMemory int      `json:"repeated_corrections_after_memory"`
}

type DriftLabel string

const (
	DriftPreserved            DriftLabel = "preserved"
	DriftDetected             DriftLabel = "drift"
	DriftInsufficientEvidence DriftLabel = "insufficient_evidence"
)

type CompactionMeasurement struct {
	CheckpointID string     `json:"checkpoint_id"`
	Expected     DriftLabel `json:"expected"`
	Observed     DriftLabel `json:"observed"`
}
type CompactionExpectation struct {
	CheckpointID string     `json:"checkpoint_id"`
	Expected     DriftLabel `json:"expected"`
}

type OutcomeLabel string

const (
	OutcomeHelpful OutcomeLabel = "helpful"
	OutcomeNeutral OutcomeLabel = "neutral"
	OutcomeHarmful OutcomeLabel = "harmful"
	OutcomeUnknown OutcomeLabel = "unknown"
)

type RetrievalMeasurement struct {
	RetrievalID     string       `json:"retrieval_id"`
	EstimatedTokens int          `json:"estimated_tokens"`
	UTF8Bytes       int          `json:"utf8_bytes"`
	SelectedItems   int          `json:"selected_items"`
	Adopted         bool         `json:"adopted"`
	Outcome         OutcomeLabel `json:"outcome"`
}

type TrialMeasurement struct {
	Success             bool    `json:"success"`
	Score               float64 `json:"score"`
	Errors              int     `json:"errors"`
	UserCorrections     int     `json:"user_corrections"`
	TokenCountEvaluated bool    `json:"token_count_evaluated"`
	TotalTokens         int     `json:"total_tokens"`
}

type PairedOutcomeMeasurement struct {
	PairID             string           `json:"pair_id"`
	BaselineAttemptID  string           `json:"baseline_attempt_id"`
	TreatmentAttemptID string           `json:"treatment_attempt_id"`
	Baseline           TrialMeasurement `json:"baseline"`
	Treatment          TrialMeasurement `json:"treatment"`
}

type QualityProfile string

const (
	QualityProfileComponent            QualityProfile = "component"
	QualityProfileContinuousLearning   QualityProfile = "continuous_learning"
	EvaluationAuthorityMeasurementOnly                = "measurement_only"
	ContinuousLearningPolicyV1                        = "continuous-learning-policy/v1"
)

type EvaluationPopulation struct {
	SchemaVersion            string                  `json:"schema_version"`
	PolicyID                 string                  `json:"policy_id"`
	EpisodeDerivationVersion string                  `json:"episode_derivation_version"`
	EpisodesSHA256           string                  `json:"episodes_sha256"`
	TimelineSHA256           string                  `json:"timeline_sha256"`
	LedgerRecordCount        int                     `json:"ledger_record_count"`
	LedgerLastRecordHash     string                  `json:"ledger_last_record_hash"`
	PortableStateSHA256      string                  `json:"portable_state_sha256"`
	PortableStateBlob        *ledger.BlobRef         `json:"portable_state_blob"`
	OracleRegistrySHA256     string                  `json:"oracle_registry_sha256"`
	OracleRegistryBlob       *ledger.BlobRef         `json:"oracle_registry_blob"`
	SystemArtifactSHA256     string                  `json:"system_artifact_sha256"`
	SystemUnderTestSHA256    map[ledger.Agent]string `json:"system_under_test_sha256"`
	CaptureSnapshotSHA256    string                  `json:"capture_snapshot_sha256"`
	CaptureSnapshotBlob      *ledger.BlobRef         `json:"capture_snapshot_blob"`
	CorpusID                 string                  `json:"corpus_id"`
	CorpusContentSHA256      string                  `json:"corpus_content_sha256"`
	Prerequisites            EfficacyPrerequisites   `json:"efficacy_prerequisites"`
	CaseSetSHA256            string                  `json:"case_set_sha256"`
	CategoryCounts           map[CaseCategory]int    `json:"category_counts"`
	RequiredAgents           []ledger.Agent          `json:"required_agents"`
	UnpairedAttemptIDs       []string                `json:"unpaired_attempt_ids"`
	PopulationIssues         []string                `json:"population_issues"`
}

// EfficacyPrerequisites records whether each independent denominator or
// execution boundary required for an efficacy claim is actually bound. A
// continuous-learning report remains measurement-only while any field is
// false, regardless of metric values.
type EfficacyPrerequisites struct {
	FrozenCorpusVerified            bool `json:"frozen_corpus_verified"`
	IndependentCaptureInventory     bool `json:"independent_capture_inventory"`
	NormalizedProjectionCoverage    bool `json:"normalized_projection_coverage"`
	CompletePortablePopulation      bool `json:"complete_portable_population"`
	PreregisteredAttemptUniverse    bool `json:"preregistered_attempt_universe"`
	PairedTrialPlanSealed           bool `json:"paired_trial_plan_sealed"`
	ExecutionSupervisorReceipts     bool `json:"execution_supervisor_receipts"`
	VerifiedAgentExecution          bool `json:"verified_agent_execution"`
	SystemArtifactManifest          bool `json:"system_artifact_manifest"`
	IndependentCompactionDetector   bool `json:"independent_compaction_detector"`
	SealedCompactionGroundTruth     bool `json:"sealed_compaction_ground_truth"`
	FrozenCompactionDriftControl    bool `json:"frozen_compaction_drift_control"`
	FrozenCompactionPreserveControl bool `json:"frozen_compaction_preserve_control"`
	BlindOracleProtocol             bool `json:"blind_oracle_protocol"`
	HermeticOracleExecution         bool `json:"hermetic_oracle_execution"`
}

type EvaluationThresholds struct {
	MinimumCaptureCoverage         *float64 `json:"minimum_capture_coverage,omitempty"`
	MaximumFalseMemoryRate         *float64 `json:"maximum_false_memory_rate,omitempty"`
	MaximumUnknownMemoryRate       *float64 `json:"maximum_unknown_memory_rate,omitempty"`
	MaximumRepeatedCorrectionRate  *float64 `json:"maximum_repeated_correction_rate,omitempty"`
	MinimumDriftPrecision          *float64 `json:"minimum_drift_precision,omitempty"`
	MinimumDriftRecall             *float64 `json:"minimum_drift_recall,omitempty"`
	MaximumMeanRetrievalTokens     *float64 `json:"maximum_mean_retrieval_tokens,omitempty"`
	MinimumMeanOutcomeScoreDelta   *float64 `json:"minimum_mean_outcome_score_delta,omitempty"`
	MaximumMeanCorrectionDelta     *float64 `json:"maximum_mean_correction_delta,omitempty"`
	MaximumHarmfulOutcomes         *float64 `json:"maximum_harmful_outcomes,omitempty"`
	MinimumCorrectionOpportunities *float64 `json:"minimum_correction_opportunities,omitempty"`
	MinimumPairedOutcomePairs      *float64 `json:"minimum_paired_outcome_pairs,omitempty"`
}

type EvaluationInput struct {
	SchemaVersion  string                `json:"schema_version"`
	SuiteID        string                `json:"suite_id"`
	RunID          string                `json:"run_id"`
	CreatedAt      time.Time             `json:"created_at"`
	SystemVersion  string                `json:"system_version"`
	QualityProfile QualityProfile        `json:"quality_profile"`
	PolicyID       string                `json:"policy_id,omitempty"`
	Population     *EvaluationPopulation `json:"population,omitempty"`
	CorpusID       string                `json:"corpus_id,omitempty"`
	Cases          []EvaluationCase      `json:"cases"`
	Thresholds     EvaluationThresholds  `json:"thresholds"`
	Privacy        string                `json:"privacy"`
}

type RatioMetric struct {
	Numerator   int      `json:"numerator"`
	Denominator int      `json:"denominator"`
	Value       *float64 `json:"value,omitempty"`
	Status      string   `json:"status"`
}

type CaptureMetrics struct {
	Unit              CaptureUnit  `json:"unit"`
	Expected          int          `json:"expected"`
	Complete          int          `json:"complete"`
	Partial           int          `json:"partial"`
	Missing           int          `json:"missing"`
	AccountedMissing  int          `json:"accounted_missing,omitempty"`
	Coverage          RatioMetric  `json:"coverage"`
	ObservedCoverage  RatioMetric  `json:"observed_coverage"`
	AccountedCoverage *RatioMetric `json:"accounted_coverage,omitempty"`
}

type FalseMemoryMetrics struct {
	Total       int         `json:"total"`
	Supported   int         `json:"supported"`
	False       int         `json:"false"`
	Unknown     int         `json:"unknown"`
	FalseRate   RatioMetric `json:"false_rate"`
	UnknownRate RatioMetric `json:"unknown_rate"`
}

type CorrectionMetrics struct {
	EligibleFollowupOpportunities  int         `json:"eligible_followup_opportunities"`
	RepeatedCorrections            int         `json:"repeated_corrections"`
	RepeatedCorrectionsAfterMemory int         `json:"repeated_corrections_after_memory"`
	RepeatedCorrectionRate         RatioMetric `json:"repeated_correction_rate"`
}

type DriftMetrics struct {
	TruePositive  int         `json:"true_positive"`
	FalsePositive int         `json:"false_positive"`
	TrueNegative  int         `json:"true_negative"`
	FalseNegative int         `json:"false_negative"`
	Excluded      int         `json:"excluded"`
	Precision     RatioMetric `json:"precision"`
	Recall        RatioMetric `json:"recall"`
}

type RetrievalMetrics struct {
	Deliveries       int      `json:"deliveries"`
	SelectedItems    int      `json:"selected_items"`
	Adopted          int      `json:"adopted"`
	Helpful          int      `json:"helpful"`
	Neutral          int      `json:"neutral"`
	Harmful          int      `json:"harmful"`
	Unknown          int      `json:"unknown"`
	TotalTokens      int      `json:"total_tokens"`
	MeanTokens       *float64 `json:"mean_tokens,omitempty"`
	P95Tokens        *float64 `json:"p95_tokens,omitempty"`
	TotalUTF8Bytes   int      `json:"total_utf8_bytes"`
	HelpfulPerKToken *float64 `json:"helpful_per_1000_tokens,omitempty"`
}

type OutcomeMetrics struct {
	Pairs               int      `json:"pairs"`
	Wins                int      `json:"wins"`
	Ties                int      `json:"ties"`
	Losses              int      `json:"losses"`
	BaselineSuccesses   int      `json:"baseline_successes"`
	TreatmentSuccesses  int      `json:"treatment_successes"`
	TokenPairs          int      `json:"token_pairs"`
	MeanScoreDelta      *float64 `json:"mean_score_delta,omitempty"`
	SuccessRateDelta    *float64 `json:"success_rate_delta,omitempty"`
	MeanErrorDelta      *float64 `json:"mean_error_delta,omitempty"`
	MeanCorrectionDelta *float64 `json:"mean_correction_delta,omitempty"`
	MeanTotalTokenDelta *float64 `json:"mean_total_token_delta,omitempty"`
}

type GateResult struct {
	Name       string   `json:"name"`
	Comparison string   `json:"comparison"`
	Threshold  float64  `json:"threshold"`
	Actual     *float64 `json:"actual,omitempty"`
	Status     string   `json:"status"`
	Reason     string   `json:"reason,omitempty"`
}

type EvaluationReport struct {
	SchemaVersion             string             `json:"schema_version"`
	EvaluationVersion         string             `json:"evaluation_version"`
	SuiteID                   string             `json:"suite_id"`
	RunID                     string             `json:"run_id"`
	InputSHA256               string             `json:"input_sha256"`
	InputBlob                 *ledger.BlobRef    `json:"input_blob,omitempty"`
	SystemVersion             string             `json:"system_version"`
	QualityProfile            QualityProfile     `json:"quality_profile"`
	Authority                 string             `json:"authority"`
	CorpusID                  string             `json:"corpus_id,omitempty"`
	CasesChecked              int                `json:"cases_checked"`
	EvidenceReferencesChecked int                `json:"evidence_references_checked"`
	AttestedReferences        int                `json:"attested_references"`
	Capture                   CaptureMetrics     `json:"capture"`
	FalseMemory               FalseMemoryMetrics `json:"false_memory"`
	Corrections               CorrectionMetrics  `json:"corrections"`
	CompactionDrift           DriftMetrics       `json:"compaction_drift"`
	Retrieval                 RetrievalMetrics   `json:"retrieval"`
	Outcomes                  OutcomeMetrics     `json:"outcomes"`
	Gates                     []GateResult       `json:"gates"`
	ReleaseReady              bool               `json:"release_ready"`
	Issues                    []string           `json:"issues"`
	ReportSHA256              string             `json:"report_sha256"`
	Privacy                   string             `json:"privacy"`
}

type RunResult struct {
	Report        EvaluationReport `json:"report"`
	ReportPath    string           `json:"report_path"`
	ReportEventID string           `json:"report_event_id"`
	Reused        bool             `json:"reused"`
}

type RunOptions struct {
	PortableRoot   string
	OracleRegistry string
	Now            func() time.Time
}

type RunVerificationOptions struct {
	PortableRoot   string
	OracleRegistry string
}

type RunVerificationReport struct {
	SchemaVersion     string   `json:"schema_version"`
	SuiteID           string   `json:"suite_id"`
	RunID             string   `json:"run_id"`
	CasesChecked      int      `json:"cases_checked"`
	ReferencesChecked int      `json:"references_checked"`
	Issues            []string `json:"issues"`
	Privacy           string   `json:"privacy"`
}

type TaskCondition string

const (
	TaskConditionBaseline TaskCondition = "baseline"
	TaskConditionMemory   TaskCondition = "memory"
)

type TaskOracle struct {
	Kind                string `json:"kind"`
	ID                  string `json:"id"`
	Version             string `json:"version"`
	RegistryEntrySHA256 string `json:"registry_entry_sha256,omitempty"`
	VerdictEventID      string `json:"verdict_event_id"`
}

type TaskAttemptRequest struct {
	SchemaVersion            string                      `json:"schema_version"`
	TaskID                   string                      `json:"task_id"`
	AttemptID                string                      `json:"attempt_id"`
	TrialPlanID              string                      `json:"trial_plan_id,omitempty"`
	TrialPairID              string                      `json:"trial_pair_id,omitempty"`
	Agent                    ledger.Agent                `json:"agent"`
	SemanticKeySHA256        string                      `json:"semantic_key_sha256,omitempty"`
	TaskSpecSHA256           string                      `json:"task_spec_sha256"`
	AcceptanceCriteriaSHA256 string                      `json:"acceptance_criteria_sha256"`
	ExecutionConfigSHA256    string                      `json:"execution_config_sha256"`
	SystemArtifactSHA256     string                      `json:"system_artifact_sha256,omitempty"`
	SystemUnderTestSHA256    string                      `json:"system_under_test_sha256,omitempty"`
	SystemUnderTestBlob      *ledger.BlobRef             `json:"system_under_test_blob,omitempty"`
	TaskSpecBlob             *ledger.BlobRef             `json:"task_spec_blob,omitempty"`
	AcceptanceCriteriaBlob   *ledger.BlobRef             `json:"acceptance_criteria_blob,omitempty"`
	ExecutionConfigBlob      *ledger.BlobRef             `json:"execution_config_blob,omitempty"`
	Condition                TaskCondition               `json:"condition"`
	WindowStartEventID       string                      `json:"window_start_event_id"`
	WindowEndEventID         string                      `json:"window_end_event_id"`
	RetrievalReceiptID       string                      `json:"retrieval_receipt_id,omitempty"`
	InjectionID              string                      `json:"injection_id,omitempty"`
	AdoptionID               string                      `json:"adoption_id,omitempty"`
	MemoryReferences         []retrieval.MemoryReference `json:"memory_references"`
	Oracle                   TaskOracle                  `json:"oracle"`
	Privacy                  string                      `json:"privacy"`
}

type TaskAttemptContract struct {
	SchemaVersion            string          `json:"schema_version"`
	TaskID                   string          `json:"task_id"`
	AttemptID                string          `json:"attempt_id"`
	TrialPlanID              string          `json:"trial_plan_id,omitempty"`
	TrialPairID              string          `json:"trial_pair_id,omitempty"`
	Agent                    ledger.Agent    `json:"agent"`
	SemanticKeySHA256        string          `json:"semantic_key_sha256"`
	TaskSpecSHA256           string          `json:"task_spec_sha256"`
	AcceptanceCriteriaSHA256 string          `json:"acceptance_criteria_sha256"`
	ExecutionConfigSHA256    string          `json:"execution_config_sha256"`
	SystemArtifactSHA256     string          `json:"system_artifact_sha256"`
	SystemUnderTestSHA256    string          `json:"system_under_test_sha256,omitempty"`
	SystemUnderTestBlob      *ledger.BlobRef `json:"system_under_test_blob,omitempty"`
	TaskSpecBlob             *ledger.BlobRef `json:"task_spec_blob,omitempty"`
	AcceptanceCriteriaBlob   *ledger.BlobRef `json:"acceptance_criteria_blob,omitempty"`
	ExecutionConfigBlob      *ledger.BlobRef `json:"execution_config_blob,omitempty"`
	Condition                TaskCondition   `json:"condition"`
	WindowEndEventID         string          `json:"window_end_event_id"`
	Oracle                   TaskOracle      `json:"oracle"`
	Privacy                  string          `json:"privacy"`
}

type TaskAttemptDraft struct {
	SchemaVersion     string        `json:"schema_version"`
	TaskID            string        `json:"task_id"`
	AttemptID         string        `json:"attempt_id"`
	Agent             ledger.Agent  `json:"agent"`
	SemanticKeySHA256 string        `json:"semantic_key_sha256"`
	Condition         TaskCondition `json:"condition"`
	Privacy           string        `json:"privacy"`
}

type TaskAttemptPreregisterOptions struct {
	ThreadID           string
	SessionID          string
	TaskSpec           []byte
	AcceptanceCriteria []byte
	ExecutionConfig    []byte
	SystemUnderTest    []byte
	OracleRegistryPath string
	Now                func() time.Time
}

type TaskAttemptPreregistration struct {
	SchemaVersion string             `json:"schema_version"`
	Request       TaskAttemptRequest `json:"request"`
	EventID       string             `json:"event_id"`
	RecordHash    string             `json:"record_hash"`
	Privacy       string             `json:"privacy"`
}

type TaskAttemptObservationResult struct {
	SchemaVersion string `json:"schema_version"`
	EventID       string `json:"event_id"`
	RecordHash    string `json:"record_hash"`
	PayloadSHA256 string `json:"payload_sha256"`
	Reused        bool   `json:"reused"`
	Privacy       string `json:"privacy"`
}

type TaskVerdict string

const (
	TaskVerdictPass TaskVerdict = "pass"
	TaskVerdictFail TaskVerdict = "fail"
)

type ResultLabel string

const (
	ResultLabelSuccess ResultLabel = "success"
	ResultLabelError   ResultLabel = "error"
	ResultLabelNeutral ResultLabel = "neutral"
)

type UserMessageLabel string

const (
	UserMessageCorrection    UserMessageLabel = "correction"
	UserMessageNotCorrection UserMessageLabel = "not_correction"
)

type LabeledResultEvent struct {
	EventID string      `json:"event_id"`
	Label   ResultLabel `json:"label"`
}

type LabeledUserMessage struct {
	EventID string           `json:"event_id"`
	Label   UserMessageLabel `json:"label"`
}

type TaskAttemptVerdict struct {
	SchemaVersion       string               `json:"schema_version"`
	TaskID              string               `json:"task_id"`
	AttemptID           string               `json:"attempt_id"`
	Verdict             TaskVerdict          `json:"verdict"`
	Score               float64              `json:"score"`
	TokenCountEvaluated bool                 `json:"token_count_evaluated"`
	TotalTokens         int                  `json:"total_tokens"`
	ResultEvents        []LabeledResultEvent `json:"result_events"`
	UserMessages        []LabeledUserMessage `json:"user_messages"`
	TaskSpecSHA256      string               `json:"task_spec_sha256"`
	CriteriaSHA256      string               `json:"acceptance_criteria_sha256"`
	ConfigSHA256        string               `json:"execution_config_sha256"`
	Privacy             string               `json:"privacy"`
}

type BoundEventReference struct {
	EventID       string `json:"event_id"`
	RecordHash    string `json:"record_hash"`
	PayloadSHA256 string `json:"payload_sha256"`
}

type TaskAttemptReceipt struct {
	SchemaVersion            string                      `json:"schema_version"`
	ReceiptID                string                      `json:"receipt_id"`
	RecordedAt               time.Time                   `json:"recorded_at"`
	TaskID                   string                      `json:"task_id"`
	AttemptID                string                      `json:"attempt_id"`
	TrialPlanID              string                      `json:"trial_plan_id,omitempty"`
	TrialPairID              string                      `json:"trial_pair_id,omitempty"`
	Agent                    ledger.Agent                `json:"agent"`
	SemanticKeySHA256        string                      `json:"semantic_key_sha256,omitempty"`
	TaskSpecSHA256           string                      `json:"task_spec_sha256"`
	AcceptanceCriteriaSHA256 string                      `json:"acceptance_criteria_sha256"`
	ExecutionConfigSHA256    string                      `json:"execution_config_sha256"`
	SystemArtifactSHA256     string                      `json:"system_artifact_sha256"`
	SystemUnderTestSHA256    string                      `json:"system_under_test_sha256,omitempty"`
	SystemUnderTestBlob      *ledger.BlobRef             `json:"system_under_test_blob,omitempty"`
	TaskSpecBlob             *ledger.BlobRef             `json:"task_spec_blob,omitempty"`
	AcceptanceCriteriaBlob   *ledger.BlobRef             `json:"acceptance_criteria_blob,omitempty"`
	ExecutionConfigBlob      *ledger.BlobRef             `json:"execution_config_blob,omitempty"`
	Condition                TaskCondition               `json:"condition"`
	WindowStart              BoundEventReference         `json:"window_start"`
	WindowEnd                BoundEventReference         `json:"window_end"`
	RetrievalReceiptID       string                      `json:"retrieval_receipt_id,omitempty"`
	InjectionID              string                      `json:"injection_id,omitempty"`
	AdoptionID               string                      `json:"adoption_id,omitempty"`
	MemoryReferences         []retrieval.MemoryReference `json:"memory_references"`
	Oracle                   TaskOracle                  `json:"oracle"`
	Verdict                  BoundEventReference         `json:"verdict"`
	ResultEvents             []BoundEventReference       `json:"result_events"`
	UserMessages             []BoundEventReference       `json:"user_messages"`
	Measurement              TrialMeasurement            `json:"measurement"`
	BindingStatus            string                      `json:"binding_status"`
	Authority                string                      `json:"authority"`
	RequestSHA256            string                      `json:"request_sha256"`
	Privacy                  string                      `json:"privacy"`
}

type TaskAttemptResult struct {
	SchemaVersion string             `json:"schema_version"`
	Receipt       TaskAttemptReceipt `json:"receipt"`
	EventID       string             `json:"event_id"`
	RecordHash    string             `json:"record_hash"`
	Reused        bool               `json:"reused"`
	Privacy       string             `json:"privacy"`
}

type TaskAttemptVerification struct {
	SchemaVersion string   `json:"schema_version"`
	ReceiptID     string   `json:"receipt_id"`
	EventsChecked int      `json:"events_checked"`
	Issues        []string `json:"issues"`
	Privacy       string   `json:"privacy"`
}

type OracleRegistry struct {
	SchemaVersion string                `json:"schema_version"`
	Entries       []OracleRegistryEntry `json:"entries"`
	Privacy       string                `json:"privacy"`
}

type OracleRegistryEntry struct {
	Kind             string   `json:"kind"`
	ID               string   `json:"id"`
	Version          string   `json:"version"`
	Executable       string   `json:"executable"`
	ExecutableSHA256 string   `json:"executable_sha256"`
	Arguments        []string `json:"arguments"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
}

type OracleReplayEvent struct {
	EventID    string           `json:"event_id"`
	Kind       ledger.EventKind `json:"kind"`
	Payload    []byte           `json:"payload"`
	RecordHash string           `json:"record_hash"`
}

type OracleReplayInput struct {
	SchemaVersion      string              `json:"schema_version"`
	TaskID             string              `json:"task_id"`
	AttemptID          string              `json:"attempt_id"`
	Condition          TaskCondition       `json:"condition"`
	TaskSpec           []byte              `json:"task_spec"`
	OrderedEvents      []OracleReplayEvent `json:"ordered_events"`
	AcceptanceCriteria []byte              `json:"acceptance_criteria"`
	ExecutionConfig    []byte              `json:"execution_config"`
	ResultEvents       []OracleReplayEvent `json:"result_events"`
	UserMessages       []OracleReplayEvent `json:"user_messages"`
	Privacy            string              `json:"privacy"`
}

const SystemUnderTestManifestSchema = "system-under-test-manifest/v1alpha2"

type SystemUnderTestManifest struct {
	SchemaVersion      string         `json:"schema_version"`
	Agent              ledger.Agent   `json:"agent"`
	Provider           string         `json:"provider"`
	Model              string         `json:"model"`
	SystemPromptSHA256 string         `json:"system_prompt_sha256"`
	SystemPromptBlob   ledger.BlobRef `json:"system_prompt_blob"`
	ToolRegistrySHA256 string         `json:"tool_registry_sha256"`
	ToolRegistryBlob   ledger.BlobRef `json:"tool_registry_blob"`
	HarnessSHA256      string         `json:"harness_sha256"`
	HarnessBlob        ledger.BlobRef `json:"harness_blob"`
	AdapterSHA256      string         `json:"adapter_sha256"`
	AdapterBlob        ledger.BlobRef `json:"adapter_blob"`
	Privacy            string         `json:"privacy"`
}

type SystemUnderTestArtifacts struct {
	SystemPrompt []byte
	ToolRegistry []byte
	Harness      []byte
	Adapter      []byte
}
