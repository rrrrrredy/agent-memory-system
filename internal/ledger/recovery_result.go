package ledger

const WriterRecoveryResultSchemaVersion = "evidence-writer-recovery-result/v1alpha1"

type WriterRecoveryResult struct {
	SchemaVersion     string             `json:"schema_version"`
	WriterLockCleared bool               `json:"writer_lock_cleared"`
	Verification      VerificationReport `json:"verification"`
	Privacy           string             `json:"privacy"`
}
