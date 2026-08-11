package capturesupervisor

const LockRecoveryResultSchemaVersion = "capture-supervisor-lock-recovery-result/v1alpha1"

type LockRecoveryResult struct {
	SchemaVersion string `json:"schema_version"`
	LockCleared   bool   `json:"lock_cleared"`
	Privacy       string `json:"privacy"`
}
