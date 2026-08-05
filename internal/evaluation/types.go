package evaluation

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	CorpusManifestSchemaVersion  = "legacy-corpus-manifest/v1alpha1"
	CorpusFreezeResultSchema     = "legacy-corpus-freeze-result/v1alpha1"
	CorpusVerifySchemaVersion    = "legacy-corpus-verification/v1alpha1"
	EvaluationInputSchemaVersion = "learning-evaluation-input/v1alpha1"
	EvaluationReportSchema       = "learning-evaluation-report/v1alpha1"
	EvaluationVersion            = "learning-evaluation/v1alpha1"
	EvaluationAttestationSchema  = "learning-evaluation-attestation/v1alpha1"
	AttestationResultSchema      = "learning-evaluation-attestation-result/v1alpha1"
	LegacyReviewPackSchema       = "legacy-regression-review-pack/v1alpha2"
	LegacyReviewPackResultSchema = "legacy-regression-review-pack-result/v1alpha2"
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
	SourceLedgerLastRecordHash string             `json:"source_ledger_last_record_hash,omitempty"`
	Artifacts                  []CorpusArtifact   `json:"artifacts"`
	Rollouts                   []RolloutReference `json:"rollouts"`
	Counts                     CorpusCounts       `json:"counts"`
	Issues                     []CorpusIssue      `json:"issues"`
	Privacy                    string             `json:"privacy"`
}

type FreezeOptions struct {
	Name string
	Now  func() time.Time
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
	Compaction    *CompactionMeasurement    `json:"compaction,omitempty"`
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
	CaptureUnitNone           CaptureUnit = "none"
	CaptureUnitEvidenceEvents CaptureUnit = "evidence_events"
	CaptureUnitLegacyRollouts CaptureUnit = "legacy_rollouts"
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
	SemanticKeySHA256              string `json:"semantic_key_sha256"`
	EligibleFollowupOpportunities  int    `json:"eligible_followup_opportunities"`
	RepeatedCorrections            int    `json:"repeated_corrections"`
	RepeatedCorrectionsAfterMemory int    `json:"repeated_corrections_after_memory"`
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
	Success         bool    `json:"success"`
	Score           float64 `json:"score"`
	Errors          int     `json:"errors"`
	UserCorrections int     `json:"user_corrections"`
	TotalTokens     int     `json:"total_tokens"`
}

type PairedOutcomeMeasurement struct {
	PairID    string           `json:"pair_id"`
	Baseline  TrialMeasurement `json:"baseline"`
	Treatment TrialMeasurement `json:"treatment"`
}

type EvaluationThresholds struct {
	MinimumCaptureCoverage        *float64 `json:"minimum_capture_coverage,omitempty"`
	MaximumFalseMemoryRate        *float64 `json:"maximum_false_memory_rate,omitempty"`
	MaximumUnknownMemoryRate      *float64 `json:"maximum_unknown_memory_rate,omitempty"`
	MaximumRepeatedCorrectionRate *float64 `json:"maximum_repeated_correction_rate,omitempty"`
	MinimumDriftPrecision         *float64 `json:"minimum_drift_precision,omitempty"`
	MinimumDriftRecall            *float64 `json:"minimum_drift_recall,omitempty"`
	MaximumMeanRetrievalTokens    *float64 `json:"maximum_mean_retrieval_tokens,omitempty"`
	MinimumMeanOutcomeScoreDelta  *float64 `json:"minimum_mean_outcome_score_delta,omitempty"`
	MaximumHarmfulOutcomes        *float64 `json:"maximum_harmful_outcomes,omitempty"`
}

type EvaluationInput struct {
	SchemaVersion string               `json:"schema_version"`
	SuiteID       string               `json:"suite_id"`
	RunID         string               `json:"run_id"`
	CreatedAt     time.Time            `json:"created_at"`
	SystemVersion string               `json:"system_version"`
	CorpusID      string               `json:"corpus_id,omitempty"`
	Cases         []EvaluationCase     `json:"cases"`
	Thresholds    EvaluationThresholds `json:"thresholds"`
	Privacy       string               `json:"privacy"`
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
	PortableRoot string
	Now          func() time.Time
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
