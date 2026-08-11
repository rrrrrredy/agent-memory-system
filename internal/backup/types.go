package backup

import "time"

const (
	ManifestSchemaVersion      = "evidence-backup-manifest/v1alpha1"
	KeygenResultSchemaVersion  = "evidence-backup-keygen-result/v1alpha1"
	CreateResultSchemaVersion  = "evidence-backup-create-result/v1alpha1"
	VerifyResultSchemaVersion  = "evidence-backup-verification/v1alpha1"
	RestoreResultSchemaVersion = "evidence-backup-restore-result/v1alpha1"
	PrivacyEncryptedEvidence   = "encrypted_local_evidence"
	DefaultMaxPlaintextBytes   = int64(4) << 40
	DefaultMaxFiles            = 10_000_000
)

type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	SchemaVersion        string      `json:"schema_version"`
	BackupID             string      `json:"backup_id"`
	CreatedAt            time.Time   `json:"created_at"`
	SourceDeviceID       string      `json:"source_device_id"`
	SourceRecords        int         `json:"source_records"`
	SourceLastRecordHash string      `json:"source_last_record_hash,omitempty"`
	Files                []FileEntry `json:"files"`
	TotalFiles           int         `json:"total_files"`
	TotalBytes           int64       `json:"total_bytes"`
	ArchiveFormat        string      `json:"archive_format"`
	Encryption           string      `json:"encryption"`
	Privacy              string      `json:"privacy"`
}

type KeygenResult struct {
	SchemaVersion string `json:"schema_version"`
	KeyType       string `json:"key_type"`
	Recipient     string `json:"recipient"`
	IdentityPath  string `json:"identity_path"`
	Privacy       string `json:"privacy"`
}

type CreateOptions struct {
	Output           string
	Recipients       []string
	Now              func() time.Time
	beforeFinalCheck func()
}

type CreateResult struct {
	SchemaVersion        string    `json:"schema_version"`
	BackupID             string    `json:"backup_id"`
	CreatedAt            time.Time `json:"created_at"`
	ArchiveSHA256        string    `json:"archive_sha256"`
	ArchiveBytes         int64     `json:"archive_bytes"`
	SourceDeviceID       string    `json:"source_device_id"`
	SourceRecords        int       `json:"source_records"`
	SourceLastRecordHash string    `json:"source_last_record_hash,omitempty"`
	Files                int       `json:"files"`
	PlaintextBytes       int64     `json:"plaintext_bytes"`
	Recipients           int       `json:"recipients"`
	Privacy              string    `json:"privacy"`
}

type VerificationIssue struct {
	Code       string `json:"code"`
	PathSHA256 string `json:"path_sha256,omitempty"`
	Message    string `json:"message"`
}

type VerificationResult struct {
	SchemaVersion        string              `json:"schema_version"`
	BackupID             string              `json:"backup_id,omitempty"`
	ArchiveSHA256        string              `json:"archive_sha256"`
	ArchiveBytes         int64               `json:"archive_bytes"`
	SourceDeviceID       string              `json:"source_device_id,omitempty"`
	SourceRecords        int                 `json:"source_records"`
	SourceLastRecordHash string              `json:"source_last_record_hash,omitempty"`
	FilesChecked         int                 `json:"files_checked"`
	PlaintextBytes       int64               `json:"plaintext_bytes"`
	LedgerVerified       bool                `json:"ledger_verified"`
	Issues               []VerificationIssue `json:"issues"`
	Privacy              string              `json:"privacy"`
}

type VerifyOptions struct {
	Archive           string
	IdentityPaths     []string
	MaxPlaintextBytes int64
	MaxFiles          int
}

type RestoreOptions struct {
	Archive           string
	IdentityPaths     []string
	Target            string
	MaxPlaintextBytes int64
	MaxFiles          int
	Now               func() time.Time
}

type RestoreResult struct {
	SchemaVersion          string              `json:"schema_version"`
	BackupID               string              `json:"backup_id,omitempty"`
	ArchiveSHA256          string              `json:"archive_sha256"`
	SourceDeviceID         string              `json:"source_device_id,omitempty"`
	RestoredDeviceID       string              `json:"restored_device_id,omitempty"`
	DeviceIDRotated        bool                `json:"device_id_rotated"`
	FilesRestored          int                 `json:"files_restored"`
	PlaintextBytes         int64               `json:"plaintext_bytes"`
	SourceRecords          int                 `json:"source_records"`
	RestoredRecords        int                 `json:"restored_records"`
	RestoredLastRecordHash string              `json:"restored_last_record_hash,omitempty"`
	Issues                 []VerificationIssue `json:"issues"`
	Privacy                string              `json:"privacy"`
}
