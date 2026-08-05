package sourcerecovery

import (
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	ManifestSchemaVersion = "source-recovery-manifest/v1alpha1"
	ResultSchemaVersion   = "source-recovery-result/v1alpha1"
	PlanResultSchema      = "source-recovery-plan-result/v1alpha1"
	AdapterName           = "source-recovery"
	AdapterVersion        = "source-recovery/v1alpha1"
)

type EntryStatus string

const (
	StatusAvailable EntryStatus = "available"
	StatusMissing   EntryStatus = "missing"
)

type Manifest struct {
	SchemaVersion string       `json:"schema_version"`
	RecoveryID    string       `json:"recovery_id,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	Agent         ledger.Agent `json:"agent"`
	Entries       []Entry      `json:"entries"`
	Privacy       string       `json:"privacy"`
}

type Entry struct {
	LogicalSourcePathSHA256 string      `json:"logical_source_path_sha256"`
	ThreadID                string      `json:"thread_id"`
	Status                  EntryStatus `json:"status"`
	RelativePath            string      `json:"relative_path,omitempty"`
	ExpectedContentSHA256   string      `json:"expected_content_sha256,omitempty"`
	ExpectedBytes           *int64      `json:"expected_bytes,omitempty"`
	Reason                  string      `json:"reason,omitempty"`
}

type Result struct {
	SchemaVersion    string               `json:"schema_version"`
	RecoveryID       string               `json:"recovery_id"`
	ManifestSHA256   string               `json:"manifest_sha256"`
	Agent            ledger.Agent         `json:"agent"`
	EntriesChecked   int                  `json:"entries_checked"`
	SourcesRecovered int                  `json:"sources_recovered"`
	SourcesMissing   int                  `json:"sources_missing"`
	ManifestReused   bool                 `json:"manifest_reused"`
	Import           *adapterjsonl.Result `json:"import,omitempty"`
	OpenCodeImport   *opencode.Result     `json:"opencode_import,omitempty"`
	GapsAppended     int                  `json:"gaps_appended"`
	GapsReused       int                  `json:"gaps_reused"`
	Privacy          string               `json:"privacy"`
}

type PlanIssue struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type PlanResult struct {
	SchemaVersion  string      `json:"schema_version"`
	CorpusID       string      `json:"corpus_id"`
	RecoveryID     string      `json:"recovery_id"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	Expected       int         `json:"expected"`
	Available      int         `json:"available"`
	Missing        int         `json:"missing"`
	Ambiguous      int         `json:"ambiguous"`
	FilesExamined  int         `json:"files_examined"`
	Issues         []PlanIssue `json:"issues"`
	Privacy        string      `json:"privacy"`
}
