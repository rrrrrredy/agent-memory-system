package gitsync

const HookInstallResultSchemaVersion = "git-sync-hook-install-result/v1alpha1"

type HookInstallResult struct {
	SchemaVersion string `json:"schema_version"`
	Installed     bool   `json:"installed"`
	Privacy       string `json:"privacy"`
}
