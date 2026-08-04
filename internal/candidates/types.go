package candidates

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	CandidateSchemaVersion = "candidate/v1alpha1"
	ManifestSchemaVersion  = "candidate-derivation-manifest/v1alpha1"
	BuildSchemaVersion     = "candidate-build-result/v1alpha1"
	DerivationVersion      = "candidates/v1alpha1"
)

type CandidateKind string

const (
	KindConstraint CandidateKind = "constraint"
	KindCorrection CandidateKind = "correction"
	KindDirective  CandidateKind = "directive"
)

type CandidatePolarity string

const (
	PolarityAffirmative CandidatePolarity = "affirmative"
	PolarityProhibitive CandidatePolarity = "prohibitive"
	PolarityUnknown     CandidatePolarity = "unknown"
)

type SupportType string

const (
	SupportUserInstruction  SupportType = "user_instruction"
	SupportUserCorrection   SupportType = "user_correction"
	SupportExplicitRemember SupportType = "explicit_remember"
	SupportCompactionDrift  SupportType = "compaction_drift"
	SupportStableRepetition SupportType = "stable_repetition"
)

type ReviewStatus string

const (
	StatusUntrusted   ReviewStatus = "untrusted"
	StatusReviewReady ReviewStatus = "review_ready"
	StatusQuarantined ReviewStatus = "quarantined"
)

type ScopeStatus string

const ScopeUnconfirmed ScopeStatus = "unconfirmed"

type BuildOptions struct {
	EpisodeGenerationPath string
	ShardCount            int
}

type BuildResult struct {
	SchemaVersion               string `json:"schema_version"`
	DerivationVersion           string `json:"derivation_version"`
	SourceEpisodeGeneration     string `json:"source_episode_generation"`
	SourceEpisodeManifestSHA256 string `json:"source_episode_manifest_sha256"`
	SourceEpisodesSHA256        string `json:"source_episodes_sha256"`
	SourceEpisodes              int    `json:"source_episodes"`
	GenerationPath              string `json:"generation_path"`
	Observations                int64  `json:"observations"`
	Candidates                  int    `json:"candidates"`
	ReviewReady                 int    `json:"review_ready"`
	Untrusted                   int    `json:"untrusted"`
	Quarantined                 int    `json:"quarantined"`
	ConflictGroups              int    `json:"conflict_groups"`
	CandidatesSHA256            string `json:"candidates_sha256"`
	Reused                      bool   `json:"reused"`
}

type Manifest struct {
	SchemaVersion               string `json:"schema_version"`
	DerivationVersion           string `json:"derivation_version"`
	Privacy                     string `json:"privacy"`
	SourceEpisodeGeneration     string `json:"source_episode_generation"`
	SourceEpisodeManifestSHA256 string `json:"source_episode_manifest_sha256"`
	SourceEpisodesSHA256        string `json:"source_episodes_sha256"`
	SourceEpisodes              int    `json:"source_episodes"`
	Observations                int64  `json:"observations"`
	Candidates                  int    `json:"candidates"`
	ReviewReady                 int    `json:"review_ready"`
	Untrusted                   int    `json:"untrusted"`
	Quarantined                 int    `json:"quarantined"`
	ConflictGroups              int    `json:"conflict_groups"`
	CandidatesFile              string `json:"candidates_file"`
	CandidatesSHA256            string `json:"candidates_sha256"`
}

type Candidate struct {
	SchemaVersion                      string            `json:"schema_version"`
	CandidateID                        string            `json:"candidate_id"`
	ContentSHA256                      string            `json:"content_sha256"`
	SemanticKeySHA256                  string            `json:"semantic_key_sha256"`
	Kind                               CandidateKind     `json:"kind"`
	Text                               string            `json:"text"`
	Polarity                           CandidatePolarity `json:"polarity"`
	Scope                              CandidateScope    `json:"scope"`
	SupportTypes                       []SupportType     `json:"support_types"`
	Observations                       []Observation     `json:"observations"`
	EpisodeCount                       int               `json:"episode_count"`
	EvidenceEventCount                 int               `json:"evidence_event_count"`
	FirstSeenAt                        time.Time         `json:"first_seen_at"`
	LastSeenAt                         time.Time         `json:"last_seen_at"`
	Validation                         Validation        `json:"validation"`
	ConflictGroupID                    string            `json:"conflict_group_id,omitempty"`
	ConflictingCandidateIDs            []string          `json:"conflicting_candidate_ids,omitempty"`
	RelatedCandidateIDs                []string          `json:"related_candidate_ids,omitempty"`
	RequiresExplicitRuleChangeApproval bool              `json:"requires_explicit_rule_change_approval"`
	Privacy                            string            `json:"privacy"`
}

type CandidateScope struct {
	Agent  ledger.Agent `json:"agent"`
	Status ScopeStatus  `json:"status"`
}

type Observation struct {
	EpisodeID          string        `json:"episode_id"`
	StatementID        string        `json:"statement_id"`
	StatementKind      string        `json:"statement_kind"`
	EvidenceEventIDs   []string      `json:"evidence_event_ids"`
	CorrectionEventIDs []string      `json:"correction_event_ids,omitempty"`
	SupportTypes       []SupportType `json:"support_types"`
	FirstSeenAt        time.Time     `json:"first_seen_at"`
	LastSeenAt         time.Time     `json:"last_seen_at"`
}

type Validation struct {
	Status                     ReviewStatus `json:"status"`
	Reasons                    []string     `json:"reasons"`
	ScopeConfirmationRequired  bool         `json:"scope_confirmation_required"`
	AutomaticPromotionEligible bool         `json:"automatic_promotion_eligible"`
}
