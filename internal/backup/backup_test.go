package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestEncryptedBackupRoundTripAndRestoreReceipt(t *testing.T) {
	fixture := newBackupFixture(t)
	created, err := Create(fixture.store, CreateOptions{
		Output: fixture.archive, Recipients: []string{fixture.recipient},
		Now: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.SourceRecords != 2 || created.Files < 3 || created.Recipients != 1 ||
		created.ArchiveSHA256 == "" || created.BackupID == "" {
		t.Fatalf("unexpected create result: %+v", created)
	}
	verified, err := Verify(VerifyOptions{
		Archive: fixture.archive, IdentityPaths: []string{fixture.identity},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verified.LedgerVerified || verified.SourceRecords != 2 ||
		verified.SourceLastRecordHash != created.SourceLastRecordHash ||
		verified.ArchiveSHA256 != created.ArchiveSHA256 || len(verified.Issues) != 0 {
		t.Fatalf("unexpected verification: %+v", verified)
	}
	target := filepath.Join(fixture.base, "restored")
	restored, err := Restore(RestoreOptions{
		Archive: fixture.archive, IdentityPaths: []string{fixture.identity}, Target: target,
		Now: func() time.Time { return fixture.now.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.SourceRecords != 2 || restored.RestoredRecords != 3 || restored.DeviceIDRotated ||
		restored.SourceDeviceID != restored.RestoredDeviceID || len(restored.Issues) != 0 {
		t.Fatalf("unexpected restore result: %+v", restored)
	}
	store, err := ledger.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	report := store.Verify()
	if len(report.Issues) != 0 || report.RecordsChecked != 3 ||
		report.LastRecordHash != restored.RestoredLastRecordHash {
		t.Fatalf("restored ledger is invalid: %+v", report)
	}
	foundRestore := false
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Source.Adapter == "evidence-backup-restore" {
			foundRestore = record.Event.Source.SourceEventID == created.BackupID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundRestore {
		t.Fatal("restore operation was not appended to local evidence")
	}
	blob, err := store.PutBlob(strings.NewReader("evidence appended after recovery"))
	if err != nil || blob.Bytes == 0 {
		t.Fatalf("restored evidence store was not writable: %+v, %v", blob, err)
	}
	if _, err := Restore(RestoreOptions{
		Archive: fixture.archive, IdentityPaths: []string{fixture.identity}, Target: target,
	}); err == nil {
		t.Fatal("restore overwrote an existing evidence directory")
	}
}

func TestEmptyStoreBackupVerifiesWithoutLedgerFile(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "source"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := GenerateIdentity(filepath.Join(base, "keys", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "backups", "empty.age")
	if _, err := Create(store, CreateOptions{
		Output: archive, Recipients: []string{key.Recipient},
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Verify(VerifyOptions{Archive: archive, IdentityPaths: []string{key.IdentityPath}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.LedgerVerified || result.SourceRecords != 0 || len(result.Issues) != 0 {
		t.Fatalf("empty backup did not verify: %+v", result)
	}
}

func TestBackupRejectsWrongIdentityTamperingAndOverwrite(t *testing.T) {
	fixture := newBackupFixture(t)
	if _, err := Create(fixture.store, CreateOptions{
		Output: fixture.archive, Recipients: []string{fixture.recipient},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(fixture.store, CreateOptions{
		Output: fixture.archive, Recipients: []string{fixture.recipient},
	}); err == nil {
		t.Fatal("backup archive was overwritten")
	}
	other, err := GenerateIdentity(filepath.Join(fixture.base, "other", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(VerifyOptions{
		Archive: fixture.archive, IdentityPaths: []string{other.IdentityPath},
	}); err == nil {
		t.Fatal("wrong identity decrypted a backup")
	}
	data, err := os.ReadFile(fixture.archive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0x80
	tampered := filepath.Join(fixture.base, "backups", "tampered.age")
	if err := os.WriteFile(tampered, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(VerifyOptions{
		Archive: tampered, IdentityPaths: []string{fixture.identity},
	}); err == nil {
		t.Fatal("tampered backup verified")
	}
}

func TestBackupRequiresQuiescentStoreAndNonGitDestinations(t *testing.T) {
	fixture := newBackupFixture(t)
	lock := filepath.Join(fixture.store.Root(), "state", "evidence-writer.lock")
	if err := os.WriteFile(lock, []byte("active\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(fixture.store, CreateOptions{
		Output: fixture.archive, Recipients: []string{fixture.recipient},
	}); err == nil {
		t.Fatal("backup ignored an evidence writer lock")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	gitRoot := filepath.Join(fixture.base, "git")
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(fixture.store, CreateOptions{
		Output: filepath.Join(gitRoot, "raw.age"), Recipients: []string{fixture.recipient},
	}); err == nil {
		t.Fatal("encrypted raw evidence backup was placed in a Git worktree")
	}
	if _, err := GenerateIdentity(filepath.Join(gitRoot, "identity.txt")); err == nil {
		t.Fatal("backup identity was placed in a Git worktree")
	}
	bareRoot := filepath.Join(fixture.base, "bare.git")
	if err := os.MkdirAll(filepath.Join(bareRoot, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bareRoot, "refs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareRoot, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(fixture.store, CreateOptions{
		Output: filepath.Join(bareRoot, "raw.age"), Recipients: []string{fixture.recipient},
	}); err == nil {
		t.Fatal("encrypted raw evidence backup was placed in a bare Git repository")
	}
}

func TestBackupAbortsWhenEvidenceChangesDuringCreation(t *testing.T) {
	fixture := newBackupFixture(t)
	_, err := Create(fixture.store, CreateOptions{
		Output: fixture.archive, Recipients: []string{fixture.recipient},
		beforeFinalCheck: func() {
			payload := ledger.InlinePayload("utf-8", "text/plain", "concurrent evidence")
			_, appendErr := fixture.store.Append(testEvent(t, fixture.store, "concurrent",
				ledger.KindAgentMessage, fixture.now.Add(time.Minute), &payload))
			if appendErr != nil {
				t.Fatal(appendErr)
			}
		},
	})
	if err == nil {
		t.Fatal("backup committed after its evidence source changed")
	}
	if _, statErr := os.Stat(fixture.archive); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed backup left a final archive: %v", statErr)
	}
}

func TestRestoreRejectsArchivePathTraversal(t *testing.T) {
	base := t.TempDir()
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(base, "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "malicious.age")
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(100, 0).UTC(),
		SourceDeviceID: "device-malicious", Files: []FileEntry{{
			Path: "../outside", SHA256: strings.Repeat("0", 64), Bytes: 0,
		}},
		TotalFiles: 1, ArchiveFormat: "tar+gzip+age", Encryption: "age-v1-native",
		Privacy: PrivacyEncryptedEvidence,
	}
	manifest.BackupID, err = expectedBackupID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestArchive(t, archive, identity.Recipient(), manifest, map[string][]byte{"../outside": {}})
	target := filepath.Join(base, "restored")
	if _, err := Restore(RestoreOptions{
		Archive: archive, IdentityPaths: []string{identityPath}, Target: target,
	}); err == nil {
		t.Fatal("path-traversal archive restored")
	}
	if _, err := os.Stat(filepath.Join(base, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path traversal wrote outside restore staging: %v", err)
	}
}

func TestGenerateIdentityIsNativeAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "backup-identity")
	result, err := GenerateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.KeyType != "mlkem768x25519" || !strings.HasPrefix(result.Recipient, "age1pq1") {
		t.Fatalf("unexpected key generation result: %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "AGE-SECRET-KEY-PQ-1") ||
		strings.Contains(string(data), result.Recipient) {
		t.Fatal("identity file has unexpected content")
	}
	if _, err := GenerateIdentity(path); err == nil {
		t.Fatal("existing identity was overwritten")
	}
	if _, err := loadIdentities([]string{path}); err != nil {
		t.Fatalf("extensionless identity could not be loaded: %v", err)
	}
}

func TestGenerateIdentityRejectsEvidenceStore(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateIdentity(filepath.Join(store.Root(), "recovery", "identity.txt")); err == nil {
		t.Fatal("backup identity was stored inside the evidence it would encrypt")
	}
}

func TestLoadIdentitiesRejectsGitAndEvidenceStore(t *testing.T) {
	base := t.TempDir()
	key, err := GenerateIdentity(filepath.Join(base, "keys", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.ReadFile(key.IdentityPath)
	if err != nil {
		t.Fatal(err)
	}
	gitRoot := filepath.Join(base, "git")
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitIdentity := filepath.Join(gitRoot, "identity.txt")
	if err := os.WriteFile(gitIdentity, identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIdentities([]string{gitIdentity}); err == nil {
		t.Fatal("loaded a private recovery identity from a Git worktree")
	}
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	storeIdentity := filepath.Join(store.Root(), "recovery", "identity.txt")
	if err := os.MkdirAll(filepath.Dir(storeIdentity), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storeIdentity, identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIdentities([]string{storeIdentity}); err == nil {
		t.Fatal("loaded a private recovery identity from an evidence store")
	}
}

func TestManifestRestoreLimitsAndPortablePaths(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: time.Unix(300, 0).UTC(),
		SourceDeviceID: "device-limits",
		Files:          []FileEntry{{Path: "store.json", SHA256: strings.Repeat("0", 64), Bytes: 2}},
		TotalFiles:     1, TotalBytes: 2, ArchiveFormat: "tar+gzip+age",
		Encryption: "age-v1-native", Privacy: PrivacyEncryptedEvidence,
	}
	var err error
	manifest.BackupID, err = expectedBackupID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManifest(manifest, 1, 10); err == nil {
		t.Fatal("manifest exceeded the configured plaintext limit")
	}
	if validArchivePath("C:/outside") || validArchivePath("../outside") ||
		validArchivePath("safe\\outside") {
		t.Fatal("non-portable or unsafe archive path was accepted")
	}
}

type backupFixture struct {
	base, archive, identity, recipient string
	store                              *ledger.Store
	now                                time.Time
}

func newBackupFixture(t *testing.T) backupFixture {
	t.Helper()
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "source"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0).UTC()
	message := ledger.InlinePayload("utf-8", "text/plain", "synthetic private instruction")
	if _, err := store.Append(testEvent(t, store, "message", ledger.KindUserMessage, now, &message)); err != nil {
		t.Fatal(err)
	}
	blob, err := store.PutBlob(strings.NewReader("synthetic private tool output"))
	if err != nil {
		t.Fatal(err)
	}
	payload := ledger.Payload{
		Encoding: "utf-8", MediaType: "text/plain", Blob: &blob,
		SHA256: blob.SHA256, Bytes: blob.Bytes,
	}
	if _, err := store.Append(testEvent(t, store, "tool", ledger.KindToolResult,
		now.Add(time.Second), &payload)); err != nil {
		t.Fatal(err)
	}
	key, err := GenerateIdentity(filepath.Join(base, "keys", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return backupFixture{
		base: base, archive: filepath.Join(base, "backups", "evidence.age"),
		identity: key.IdentityPath, recipient: key.Recipient, store: store, now: now.Add(time.Minute),
	}
}

func testEvent(t *testing.T, store *ledger.Store, suffix string, kind ledger.EventKind,
	now time.Time, payload *ledger.Payload) ledger.Event {
	t.Helper()
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "backup-test-" + suffix,
		Kind: kind, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "backup-test", AdapterVersion: "backup-test/v1",
			DeviceID: store.DeviceID(), ThreadID: "thread-backup-test",
		},
		Payload: payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
}

func writeTestArchive(t *testing.T, path string, recipient age.Recipient, manifest Manifest,
	files map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := age.Encrypt(file, recipient)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(encrypted)
	archive := tar.NewWriter(compressed)
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.WriteHeader(&tar.Header{
		Name: manifestArchivePath, Mode: 0o600, Size: int64(len(manifestData)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(manifestData); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		data := files[name]
		digest := sha256.Sum256(data)
		_ = hex.EncodeToString(digest[:])
		if err := archive.WriteHeader(&tar.Header{
			Name: storeArchivePrefix + name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []io.Closer{archive, compressed, encrypted, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
