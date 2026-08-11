package evaluation

const OracleRegistryInitResultSchemaVersion = "task-oracle-registry-init/v1alpha1"

type OracleRegistryInitResult struct {
	SchemaVersion string `json:"schema_version"`
	File          string `json:"file"`
	EntrySHA256   string `json:"entry_sha256"`
	Privacy       string `json:"privacy"`
}
