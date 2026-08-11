package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func Restore(options RestoreOptions) (result RestoreResult, returnedErr error) {
	result = RestoreResult{
		SchemaVersion: RestoreResultSchemaVersion,
		Issues:        []VerificationIssue{},
		Privacy:       PrivacyEncryptedEvidence,
	}
	if strings.TrimSpace(options.Target) == "" {
		return result, errors.New("restore target is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	identities, err := loadIdentities(options.IdentityPaths)
	if err != nil {
		return result, err
	}
	maxBytes, maxFiles, err := restoreLimits(options.MaxPlaintextBytes, options.MaxFiles)
	if err != nil {
		return result, err
	}
	target, err := filepath.Abs(options.Target)
	if err != nil {
		return result, fmt.Errorf("resolve restore target: %w", err)
	}
	if err := ensurePathOutsideGit(target); err != nil {
		return result, fmt.Errorf("restore target: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return result, errors.New("restore target already exists; refusing to overwrite local evidence")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("inspect restore target: %w", err)
	}
	archive, err := filepath.Abs(options.Archive)
	if err != nil {
		return result, fmt.Errorf("resolve backup archive: %w", err)
	}
	if pathsOverlap(archive, target) {
		return result, errors.New("backup archive and restore target must be separate")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return result, fmt.Errorf("create restore parent directory: %w", err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), ".agentmem-restore-*")
	if err != nil {
		return result, fmt.Errorf("create restore staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	manifest, verification, err := inspectArchive(archive, identities, staging, maxBytes, maxFiles)
	result.BackupID = verification.BackupID
	result.ArchiveSHA256 = verification.ArchiveSHA256
	result.SourceDeviceID = verification.SourceDeviceID
	result.RestoredDeviceID = verification.SourceDeviceID
	result.FilesRestored = verification.FilesChecked
	result.PlaintextBytes = verification.PlaintextBytes
	result.SourceRecords = verification.SourceRecords
	result.Issues = verification.Issues
	if err != nil {
		return result, err
	}
	if len(verification.Issues) != 0 {
		return result, errors.New("encrypted evidence backup verification failed before restore")
	}
	store, err := ledger.Open(staging)
	if err != nil {
		return result, fmt.Errorf("open restored evidence: %w", err)
	}
	if store.DeviceID() != manifest.SourceDeviceID {
		return result, errors.New("restored evidence identity differs from the backup manifest")
	}
	now := options.Now().UTC()
	payloadData, err := json.Marshal(struct {
		SchemaVersion string    `json:"schema_version"`
		BackupID      string    `json:"backup_id"`
		ArchiveSHA256 string    `json:"archive_sha256"`
		RestoredAt    time.Time `json:"restored_at"`
	}{
		SchemaVersion: "evidence-backup-restore-event/v1alpha1",
		BackupID:      manifest.BackupID, ArchiveSHA256: verification.ArchiveSHA256,
		RestoredAt: now,
	})
	if err != nil {
		return result, fmt.Errorf("encode restore event: %w", err)
	}
	eventID, err := ledger.NewEventID(now)
	if err != nil {
		return result, err
	}
	payload := ledger.InlinePayload("utf-8", "application/json", string(payloadData))
	if _, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       eventID,
		Kind:          ledger.KindSystemEvent,
		ObservedAt:    now,
		RecordedAt:    now,
		Source: ledger.Source{
			Agent: ledger.AgentUnknown, Adapter: "evidence-backup-restore",
			AdapterVersion: ManifestSchemaVersion, DeviceID: store.DeviceID(),
			ThreadID: "system:evidence-backup", SourceEventID: manifest.BackupID,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}); err != nil {
		return result, fmt.Errorf("append restore evidence event: %w", err)
	}
	postRestore := store.Verify()
	if len(postRestore.Issues) != 0 || postRestore.RecordsChecked != manifest.SourceRecords+1 {
		return result, errors.New("restored evidence failed post-restore verification")
	}
	if _, err := os.Lstat(target); err == nil {
		return result, errors.New("restore target appeared during recovery; refusing to replace it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("recheck restore target: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return result, fmt.Errorf("commit restored evidence: %w", err)
	}
	committed = true
	result.RestoredRecords = postRestore.RecordsChecked
	result.RestoredLastRecordHash = postRestore.LastRecordHash
	return result, nil
}
