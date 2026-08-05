package agentassessment

import "github.com/rrrrrredy/agent-memory-system/internal/ledger"

const (
	ProjectionSchema       = "legacy-agent-assessment-projection/v1alpha1"
	ProjectionResultSchema = "legacy-agent-assessment-projection-result/v1alpha1"
	BlindPayloadSchema     = "legacy-agent-assessment-payload/v1alpha1"
	ExtractionVersion      = "agent-assessment-projection/v1alpha1"
	SubmissionSchema       = "legacy-agent-assessment-submission/v1alpha1"
	AssessmentSchema       = "legacy-agent-assessment/v1alpha1"
	AssessmentResultSchema = "legacy-agent-assessment-result/v1alpha1"
)

type SourcePrefix struct {
	Records        int    `json:"records"`
	LastRecordHash string `json:"last_record_hash"`
}

// Projection is the local binding manifest. It is not the payload supplied to
// an external Agent because it retains stable source bindings and completeness
// data needed by the verifier.
type Projection struct {
	SchemaVersion           string           `json:"schema_version"`
	ProjectionID            string           `json:"projection_id"`
	ProjectionContentSHA256 string           `json:"projection_content_sha256"`
	PayloadID               string           `json:"payload_id"`
	PayloadContentSHA256    string           `json:"payload_content_sha256"`
	QueueID                 string           `json:"queue_id"`
	QueueSHA256             string           `json:"queue_sha256"`
	PackID                  string           `json:"pack_id"`
	PackSHA256              string           `json:"pack_sha256"`
	CorpusID                string           `json:"corpus_id"`
	CorpusContentSHA256     string           `json:"corpus_content_sha256"`
	CandidateManifestSHA256 string           `json:"candidate_manifest_sha256"`
	CandidatesSHA256        string           `json:"candidates_sha256"`
	EpisodesSHA256          string           `json:"episodes_sha256"`
	SourceEvidencePrefix    SourcePrefix     `json:"source_evidence_prefix"`
	ExtractionVersion       string           `json:"extraction_version"`
	EvidenceBlocks          []EvidenceBlock  `json:"evidence_blocks"`
	CandidateItems          []CandidateItem  `json:"candidate_items"`
	CompactionItems         []CompactionItem `json:"compaction_items"`
	Privacy                 string           `json:"privacy"`
}

type EvidenceBlock struct {
	BlockID          string                    `json:"block_id"`
	RecordSHA256     string                    `json:"record_sha256"`
	Sequence         int                       `json:"sequence"`
	Kind             ledger.EventKind          `json:"kind"`
	Agent            ledger.Agent              `json:"agent"`
	Completeness     ledger.CompletenessStatus `json:"completeness"`
	PayloadSHA256    string                    `json:"payload_sha256"`
	PayloadBytes     int64                     `json:"payload_bytes"`
	ExtractedSHA256  string                    `json:"extracted_sha256"`
	Text             string                    `json:"text"`
	UntrustedContent bool                      `json:"untrusted_content"`
}

type CandidateItem struct {
	ItemID           string   `json:"item_id"`
	SubjectSHA256    string   `json:"subject_sha256"`
	Text             string   `json:"text"`
	UntrustedContent bool     `json:"untrusted_content"`
	EvidenceBlockIDs []string `json:"evidence_block_ids"`
	EvidenceComplete bool     `json:"evidence_complete"`
	EvidenceGapCodes []string `json:"evidence_gap_codes,omitempty"`
}

type CompactionItem struct {
	ItemID                     string           `json:"item_id"`
	CheckpointEvidenceBlockIDs []string         `json:"checkpoint_evidence_block_ids"`
	CheckpointEvidenceComplete bool             `json:"checkpoint_evidence_complete"`
	AllUnitsProjected          bool             `json:"all_units_projected"`
	ProjectionGapCodes         []string         `json:"projection_gap_codes,omitempty"`
	Units                      []CompactionUnit `json:"units"`
}

type CompactionUnit struct {
	UnitID                         string   `json:"unit_id"`
	SubjectSHA256                  string   `json:"subject_sha256"`
	Statement                      string   `json:"statement"`
	UntrustedContent               bool     `json:"untrusted_content"`
	SourceEvidenceBlockIDs         []string `json:"source_evidence_block_ids"`
	RepresentationEvidenceBlockIDs []string `json:"representation_evidence_block_ids"`
	CorrectionEvidenceBlockIDs     []string `json:"correction_evidence_block_ids,omitempty"`
	EvidenceComplete               bool     `json:"evidence_complete"`
	EvidenceGapCodes               []string `json:"evidence_gap_codes,omitempty"`
}

// BlindPayload is the only artifact intended for optional external
// assessment. It omits source IDs, stable source hashes, existing labels, and
// completeness decisions.
type BlindPayload struct {
	SchemaVersion        string                `json:"schema_version"`
	PayloadID            string                `json:"payload_id"`
	PayloadContentSHA256 string                `json:"payload_content_sha256"`
	EvidenceBlocks       []BlindEvidenceBlock  `json:"evidence_blocks"`
	CandidateItems       []BlindCandidateItem  `json:"candidate_items"`
	CompactionItems      []BlindCompactionItem `json:"compaction_items"`
	ArtifactStorage      string                `json:"artifact_storage"`
	DataClassification   string                `json:"data_classification"`
}

type BlindEvidenceBlock struct {
	BlockID          string `json:"block_id"`
	Order            int    `json:"order"`
	Role             string `json:"role"`
	Text             string `json:"text"`
	UntrustedContent bool   `json:"untrusted_content"`
}

type BlindCandidateItem struct {
	ItemID           string   `json:"item_id"`
	Text             string   `json:"text"`
	UntrustedContent bool     `json:"untrusted_content"`
	EvidenceBlockIDs []string `json:"evidence_block_ids"`
}

type BlindCompactionItem struct {
	ItemID                     string                `json:"item_id"`
	CheckpointEvidenceBlockIDs []string              `json:"checkpoint_evidence_block_ids"`
	Units                      []BlindCompactionUnit `json:"units"`
}

type BlindCompactionUnit struct {
	UnitID                         string   `json:"unit_id"`
	Statement                      string   `json:"statement"`
	UntrustedContent               bool     `json:"untrusted_content"`
	SourceEvidenceBlockIDs         []string `json:"source_evidence_block_ids"`
	RepresentationEvidenceBlockIDs []string `json:"representation_evidence_block_ids"`
	CorrectionEvidenceBlockIDs     []string `json:"correction_evidence_block_ids,omitempty"`
}

type ProjectionResult struct {
	SchemaVersion   string `json:"schema_version"`
	ProjectionID    string `json:"projection_id"`
	ProjectionPath  string `json:"projection_path"`
	PayloadID       string `json:"payload_id"`
	PayloadPath     string `json:"payload_path"`
	QueueID         string `json:"queue_id"`
	CandidateItems  int    `json:"candidate_items"`
	CompactionItems int    `json:"compaction_items"`
	CompactionUnits int    `json:"compaction_units"`
	EvidenceBlocks  int    `json:"evidence_blocks"`
	Reused          bool   `json:"reused"`
	Privacy         string `json:"privacy"`
}

type Submission struct {
	SchemaVersion         string                 `json:"schema_version"`
	PayloadID             string                 `json:"payload_id"`
	CandidateAssessments  []CandidateAssessment  `json:"candidate_assessments"`
	CompactionAssessments []CompactionAssessment `json:"compaction_assessments"`
}

type CandidateAssessment struct {
	ItemID           string   `json:"item_id"`
	Judgment         string   `json:"judgment"`
	ReasonCodes      []string `json:"reason_codes"`
	EvidenceBlockIDs []string `json:"evidence_block_ids"`
}

type CompactionAssessment struct {
	ItemID           string   `json:"item_id"`
	UnitID           string   `json:"unit_id"`
	Judgment         string   `json:"judgment"`
	ReasonCodes      []string `json:"reason_codes"`
	EvidenceBlockIDs []string `json:"evidence_block_ids"`
}

type Assessor struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	ClaimedProvider string `json:"claimed_provider"`
	ClaimedModel    string `json:"claimed_model"`
}

type HarnessProvenance struct {
	Version                   string `json:"version"`
	PromptSHA256              string `json:"prompt_sha256"`
	Source                    string `json:"source"`
	IsolationStatus           string `json:"isolation_status"`
	ToolsRegistered           *bool  `json:"tools_registered"`
	DataDisclosureClaim       string `json:"data_disclosure_claim"`
	ExtractedSubmissionSHA256 string `json:"extracted_submission_sha256"`
}

type Assessment struct {
	SchemaVersion           string                 `json:"schema_version"`
	AssessmentID            string                 `json:"assessment_id"`
	AssessmentContentSHA256 string                 `json:"assessment_content_sha256"`
	ProjectionID            string                 `json:"projection_id"`
	ProjectionContentSHA256 string                 `json:"projection_content_sha256"`
	ProjectionFileSHA256    string                 `json:"projection_file_sha256"`
	PayloadID               string                 `json:"payload_id"`
	PayloadContentSHA256    string                 `json:"payload_content_sha256"`
	PayloadFileSHA256       string                 `json:"payload_file_sha256"`
	Assessor                Assessor               `json:"assessor"`
	Harness                 HarnessProvenance      `json:"harness"`
	AssessedAt              string                 `json:"assessed_at"`
	Authority               string                 `json:"authority"`
	CandidateAssessments    []CandidateAssessment  `json:"candidate_assessments"`
	CompactionAssessments   []CompactionAssessment `json:"compaction_assessments"`
	ArtifactStorage         string                 `json:"artifact_storage"`
}

type AssessmentOptions struct {
	AssessorID          string
	ClaimedProvider     string
	ClaimedModel        string
	HarnessVersion      string
	PromptSHA256        string
	DataDisclosureClaim string
	AssessedAt          string
}

type AssessmentResult struct {
	SchemaVersion   string `json:"schema_version"`
	AssessmentID    string `json:"assessment_id"`
	AssessmentPath  string `json:"assessment_path"`
	ProjectionID    string `json:"projection_id"`
	PayloadID       string `json:"payload_id"`
	CandidateItems  int    `json:"candidate_items"`
	CompactionUnits int    `json:"compaction_units"`
	Reused          bool   `json:"reused"`
	Authority       string `json:"authority"`
	ArtifactStorage string `json:"artifact_storage"`
}
