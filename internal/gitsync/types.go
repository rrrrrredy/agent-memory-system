package gitsync

import "github.com/rrrrrredy/agent-memory-system/internal/portable"

const (
	VerificationSchemaVersion = "git-sync-verification/v1alpha1"
	BootstrapSchemaVersion    = "git-sync-bootstrap-result/v1alpha1"
	ResultSchemaVersion       = "git-sync-result/v1alpha1"
	DefaultRemote             = "origin"
	DefaultBranch             = "main"
)

type Issue struct {
	Code               string   `json:"code"`
	Commit             string   `json:"commit,omitempty"`
	Path               string   `json:"path,omitempty"`
	PathSHA256         string   `json:"path_sha256,omitempty"`
	Paths              []string `json:"paths,omitempty"`
	MemoryID           string   `json:"memory_id,omitempty"`
	RevisionID         string   `json:"revision_id,omitempty"`
	RelatedMemoryIDs   []string `json:"related_memory_ids,omitempty"`
	RelatedRevisionIDs []string `json:"related_revision_ids,omitempty"`
	Message            string   `json:"message"`
	RecoverBy          string   `json:"recover_by,omitempty"`
}

type VerificationReport struct {
	SchemaVersion    string                      `json:"schema_version"`
	GitRepository    bool                        `json:"git_repository"`
	Branch           string                      `json:"branch,omitempty"`
	Head             string                      `json:"head,omitempty"`
	TrackedFiles     int                         `json:"tracked_files"`
	CommitsChecked   int                         `json:"commits_checked"`
	WorkingTreeClean bool                        `json:"working_tree_clean"`
	Portable         portable.VerificationReport `json:"portable"`
	Issues           []Issue                     `json:"issues"`
	Privacy          string                      `json:"privacy"`
}

type BootstrapOptions struct {
	RepositoryRoot string
	RemoteName     string
	RemoteURL      string
	InstallHooks   bool
}

type BootstrapResult struct {
	SchemaVersion    string `json:"schema_version"`
	Branch           string `json:"branch"`
	Head             string `json:"head"`
	RemoteConfigured bool   `json:"remote_configured"`
	HooksInstalled   bool   `json:"hooks_installed"`
	Privacy          string `json:"privacy"`
}

type SyncOptions struct {
	RepositoryRoot string
	RemoteName     string
	NonInteractive bool
}

type Result struct {
	SchemaVersion      string  `json:"schema_version"`
	Outcome            string  `json:"outcome"`
	Branch             string  `json:"branch"`
	LocalCommitBefore  string  `json:"local_commit_before,omitempty"`
	LocalCommitAfter   string  `json:"local_commit_after,omitempty"`
	RemoteCommit       string  `json:"remote_commit,omitempty"`
	LocalCommitCreated bool    `json:"local_commit_created"`
	Fetched            bool    `json:"fetched"`
	FastForwarded      bool    `json:"fast_forwarded"`
	MergeCreated       bool    `json:"merge_created"`
	Pushed             bool    `json:"pushed"`
	Issues             []Issue `json:"issues"`
	Privacy            string  `json:"privacy"`
}
