package portable

import (
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const (
	RepositorySchemaVersion    = "portable-memory-repository/v1alpha1"
	InitResultSchemaVersion    = "portable-memory-init-result/v1alpha1"
	RevisionSchemaVersion      = "portable-memory-revision/v1alpha1"
	ExportResultSchemaVersion  = "portable-memory-export-result/v1alpha1"
	VerificationSchemaVersion  = "portable-memory-verification/v1alpha2"
	LoadoutSchemaVersion       = "portable-memory-loadout/v1alpha1"
	LoadoutCreateSchemaVersion = "portable-memory-loadout-create-result/v1alpha1"
	RepositoryLayout           = "immutable-markdown-revisions"
	PortablePrivacy            = "private_git"
	MaxTextBytes               = 64 * 1024
)

type Action string

const (
	ActionPromote   Action = "promote"
	ActionSupersede Action = "supersede"
	ActionRevoke    Action = "revoke"
)

type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

type Revision struct {
	SchemaVersion                      string                   `json:"schema_version"`
	MemoryID                           string                   `json:"memory_id"`
	RevisionID                         string                   `json:"revision_id"`
	ParentRevisionID                   string                   `json:"parent_revision_id,omitempty"`
	Action                             Action                   `json:"action"`
	Status                             Status                   `json:"status"`
	Kind                               candidates.CandidateKind `json:"kind"`
	ScopeKind                          review.ScopeKind         `json:"scope_kind"`
	ScopeValue                         string                   `json:"scope_value"`
	EvidenceBasis                      []review.Basis           `json:"evidence_basis,omitempty"`
	Text                               string                   `json:"text,omitempty"`
	TextSHA256                         string                   `json:"text_sha256,omitempty"`
	RequiresExplicitRuleChangeApproval bool                     `json:"requires_explicit_rule_change_approval"`
	RuleChangeAuthorization            string                   `json:"rule_change_authorization"`
	Privacy                            string                   `json:"privacy"`
}

type LoadoutMemoryReference struct {
	MemoryID   string `json:"memory_id"`
	RevisionID string `json:"revision_id"`
}

type Loadout struct {
	SchemaVersion string                   `json:"schema_version"`
	LoadoutID     string                   `json:"loadout_id"`
	Name          string                   `json:"name"`
	Description   string                   `json:"description,omitempty"`
	Agents        []ledger.Agent           `json:"agents"`
	ScopeKind     review.ScopeKind         `json:"scope_kind"`
	ScopeValue    string                   `json:"scope_value"`
	Memories      []LoadoutMemoryReference `json:"memories"`
	TokenBudget   int                      `json:"token_budget"`
	ByteBudget    int                      `json:"byte_budget"`
	Privacy       string                   `json:"privacy"`
}

type LoadoutCreateOptions struct {
	Name        string
	Description string
	Agents      []ledger.Agent
	Scope       review.Scope
	MemoryIDs   []string
	TokenBudget int
	ByteBudget  int
}

type LoadoutCreateResult struct {
	SchemaVersion string  `json:"schema_version"`
	Loadout       Loadout `json:"loadout"`
	RelativePath  string  `json:"relative_path"`
	Written       bool    `json:"written"`
	Privacy       string  `json:"privacy"`
}

type ExportOptions struct {
	MemoryIDs []string
}

type InitResult struct {
	SchemaVersion string `json:"schema_version"`
	Initialized   bool   `json:"initialized"`
	Privacy       string `json:"privacy"`
}

type ExportResult struct {
	SchemaVersion      string   `json:"schema_version"`
	MemoriesSelected   int      `json:"memories_selected"`
	RevisionsProjected int      `json:"revisions_projected"`
	RevisionsWritten   int      `json:"revisions_written"`
	RevisionsUnchanged int      `json:"revisions_unchanged"`
	FilesWritten       []string `json:"files_written"`
	Privacy            string   `json:"privacy"`
}

type VerificationIssue struct {
	Code               string   `json:"code"`
	LoadoutID          string   `json:"loadout_id,omitempty"`
	MemoryID           string   `json:"memory_id,omitempty"`
	RevisionID         string   `json:"revision_id,omitempty"`
	RelatedMemoryIDs   []string `json:"related_memory_ids,omitempty"`
	RelatedRevisionIDs []string `json:"related_revision_ids,omitempty"`
	Message            string   `json:"message"`
}

type VerificationReport struct {
	SchemaVersion    string              `json:"schema_version"`
	FilesChecked     int                 `json:"files_checked"`
	RevisionsChecked int                 `json:"revisions_checked"`
	MemoriesChecked  int                 `json:"memories_checked"`
	ActiveMemories   int                 `json:"active_memories"`
	RevokedMemories  int                 `json:"revoked_memories"`
	LoadoutsChecked  int                 `json:"loadouts_checked"`
	Issues           []VerificationIssue `json:"issues"`
	Privacy          string              `json:"privacy"`
}
