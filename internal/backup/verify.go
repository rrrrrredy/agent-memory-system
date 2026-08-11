package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const maxManifestBytes = 64 << 20

func Verify(options VerifyOptions) (VerificationResult, error) {
	identities, err := loadIdentities(options.IdentityPaths)
	if err != nil {
		return newVerificationResult(), err
	}
	maxBytes, maxFiles, err := restoreLimits(options.MaxPlaintextBytes, options.MaxFiles)
	if err != nil {
		return newVerificationResult(), err
	}
	_, result, err := inspectArchive(options.Archive, identities, "", maxBytes, maxFiles)
	if err != nil {
		return result, err
	}
	if len(result.Issues) != 0 {
		return result, errors.New("encrypted evidence backup verification failed")
	}
	return result, nil
}

func newVerificationResult() VerificationResult {
	return VerificationResult{
		SchemaVersion: VerifyResultSchemaVersion,
		Issues:        []VerificationIssue{},
		Privacy:       PrivacyEncryptedEvidence,
	}
}

func inspectArchive(archivePath string, identities []age.Identity, destination string,
	maxPlaintextBytes int64, maxFiles int) (
	Manifest, VerificationResult, error,
) {
	result := newVerificationResult()
	if strings.TrimSpace(archivePath) == "" {
		return Manifest{}, result, errors.New("backup archive path is required")
	}
	info, err := os.Lstat(archivePath)
	if err != nil {
		return Manifest{}, result, fmt.Errorf("inspect backup archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, result, errors.New("backup archive must be a regular file, not a link or device")
	}
	result.ArchiveSHA256, result.ArchiveBytes, err = hashRegularFile(archivePath)
	if err != nil {
		return Manifest{}, result, fmt.Errorf("hash backup archive: %w", err)
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, result, fmt.Errorf("open backup archive: %w", err)
	}
	decrypted, err := age.Decrypt(file, identities...)
	if err != nil {
		_ = file.Close()
		return Manifest{}, result, errors.New("decrypt backup archive: no supplied identity could open it")
	}
	buffered := bufio.NewReader(decrypted)
	compressed, err := gzip.NewReader(buffered)
	if err != nil {
		_ = file.Close()
		return Manifest{}, result, errors.New("backup plaintext does not contain the expected compressed archive")
	}
	compressed.Multistream(false)
	archive := tar.NewReader(compressed)
	header, err := archive.Next()
	if err != nil {
		_ = compressed.Close()
		_ = file.Close()
		return Manifest{}, result, errors.New("backup archive has no manifest")
	}
	if header.Name != manifestArchivePath || !regularTarHeader(header) ||
		header.Size < 2 || header.Size > maxManifestBytes {
		_ = compressed.Close()
		_ = file.Close()
		return Manifest{}, result, errors.New("backup archive has an invalid manifest entry")
	}
	manifestData, err := io.ReadAll(io.LimitReader(archive, header.Size))
	if err != nil || int64(len(manifestData)) != header.Size {
		_ = compressed.Close()
		_ = file.Close()
		return Manifest{}, result, errors.New("backup manifest is truncated")
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		_ = compressed.Close()
		_ = file.Close()
		return Manifest{}, result, errors.New("backup manifest is invalid")
	}
	var trailingManifest any
	if err := decoder.Decode(&trailingManifest); !errors.Is(err, io.EOF) {
		_ = compressed.Close()
		_ = file.Close()
		return Manifest{}, result, errors.New("backup manifest has trailing content")
	}
	if err := validateManifest(manifest, maxPlaintextBytes, maxFiles); err != nil {
		_ = compressed.Close()
		_ = file.Close()
		result.Issues = append(result.Issues, VerificationIssue{Code: "invalid_manifest", Message: err.Error()})
		return manifest, result, nil
	}
	result.BackupID = manifest.BackupID
	result.SourceDeviceID = manifest.SourceDeviceID
	result.SourceRecords = manifest.SourceRecords
	result.SourceLastRecordHash = manifest.SourceLastRecordHash

	expected := make(map[string]FileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		expected[entry.Path] = entry
	}
	seen := map[string]struct{}{}
	storeManifestVerified := false
	ledgerSeen := false
	ledgerReport := ledger.VerificationReport{Issues: []string{}}
	fatalArchiveIssue := false
	for {
		header, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			result.Issues = append(result.Issues, VerificationIssue{
				Code: "archive_read_failed", Message: "encrypted archive could not be read to completion",
			})
			fatalArchiveIssue = true
			break
		}
		if !regularTarHeader(header) || !strings.HasPrefix(header.Name, storeArchivePrefix) {
			result.Issues = append(result.Issues, VerificationIssue{
				Code: "unexpected_archive_entry", PathSHA256: pathDigest(header.Name),
				Message: "archive contains a non-regular or undeclared entry",
			})
			fatalArchiveIssue = true
			break
		}
		relative := strings.TrimPrefix(header.Name, storeArchivePrefix)
		entry, declared := expected[relative]
		_, duplicate := seen[relative]
		if !declared || duplicate || !validArchivePath(relative) || header.Size != entry.Bytes {
			result.Issues = append(result.Issues, VerificationIssue{
				Code: "invalid_file_entry", PathSHA256: pathDigest(relative),
				Message: "archive file is undeclared, duplicated, unsafe, or has the wrong size",
			})
			fatalArchiveIssue = true
			break
		}
		seen[relative] = struct{}{}
		hasher := sha256.New()
		limited := io.LimitReader(archive, entry.Bytes)
		var destinationFile *os.File
		var writer io.Writer = hasher
		if destination != "" {
			target := filepath.Join(destination, filepath.FromSlash(relative))
			if !pathWithin(target, destination) {
				result.Issues = append(result.Issues, VerificationIssue{
					Code: "unsafe_restore_path", PathSHA256: pathDigest(relative),
					Message: "archive file would escape the restore directory",
				})
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return manifest, result, fmt.Errorf("create restore directory: %w", err)
			}
			destinationFile, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return manifest, result, fmt.Errorf("create restored file %s: %w", pathDigest(relative), err)
			}
			writer = io.MultiWriter(hasher, destinationFile)
		}
		reader := io.TeeReader(limited, writer)
		switch relative {
		case "evidence/events.jsonl":
			ledgerSeen = true
			ledgerReport = ledger.VerifyReader(reader, func(reference ledger.BlobRef) string {
				entry, exists := expected[reference.RelativePath]
				if !exists || entry.SHA256 != reference.SHA256 || entry.Bytes != reference.Bytes {
					return "blob is absent from the encrypted backup manifest"
				}
				return ""
			})
		case "store.json":
			data, readErr := io.ReadAll(reader)
			if readErr == nil {
				storeManifestVerified = validStoreManifest(data, manifest.SourceDeviceID)
			}
		default:
			_, err = io.Copy(io.Discard, reader)
		}
		if destinationFile != nil {
			syncErr := destinationFile.Sync()
			closeErr := destinationFile.Close()
			if err == nil {
				err = errors.Join(syncErr, closeErr)
			}
		}
		if err != nil {
			return manifest, result, fmt.Errorf("read encrypted backup file %s: %w", pathDigest(relative), err)
		}
		if hex.EncodeToString(hasher.Sum(nil)) != entry.SHA256 {
			result.Issues = append(result.Issues, VerificationIssue{
				Code: "file_hash_mismatch", PathSHA256: pathDigest(relative),
				Message: "archive file content does not match the authenticated manifest",
			})
		}
		result.FilesChecked++
		result.PlaintextBytes += entry.Bytes
	}
	var drainErr, trailingErr error
	var trailing int64
	if !fatalArchiveIssue {
		_, drainErr = io.Copy(io.Discard, compressed)
	}
	closeCompressionErr := compressed.Close()
	if !fatalArchiveIssue {
		trailing, trailingErr = io.Copy(io.Discard, buffered)
	}
	closeFileErr := file.Close()
	if drainErr != nil || closeCompressionErr != nil || trailingErr != nil || closeFileErr != nil || trailing != 0 {
		result.Issues = append(result.Issues, VerificationIssue{
			Code: "archive_authentication_failed", Message: "encrypted archive did not end cleanly",
		})
	}
	for _, entry := range manifest.Files {
		if _, exists := seen[entry.Path]; !exists {
			result.Issues = append(result.Issues, VerificationIssue{
				Code: "missing_file", PathSHA256: pathDigest(entry.Path),
				Message: "a manifest file is missing from the archive",
			})
		}
	}
	if !storeManifestVerified {
		result.Issues = append(result.Issues, VerificationIssue{
			Code: "invalid_store_manifest", Message: "restored store manifest is missing or inconsistent",
		})
	}
	if !ledgerSeen && manifest.SourceRecords != 0 {
		result.Issues = append(result.Issues, VerificationIssue{
			Code: "missing_ledger", Message: "backup claims evidence records but contains no evidence ledger",
		})
	} else if ledgerSeen && (len(ledgerReport.Issues) != 0 ||
		ledgerReport.RecordsChecked != manifest.SourceRecords ||
		ledgerReport.LastRecordHash != manifest.SourceLastRecordHash) {
		result.Issues = append(result.Issues, VerificationIssue{
			Code: "invalid_ledger", Message: "evidence ledger integrity or manifest binding failed",
		})
	} else {
		result.LedgerVerified = true
	}
	if result.FilesChecked != manifest.TotalFiles || result.PlaintextBytes != manifest.TotalBytes {
		result.Issues = append(result.Issues, VerificationIssue{
			Code: "archive_totals_mismatch", Message: "archive file totals differ from the manifest",
		})
	}
	result.Issues = uniqueSortedIssues(result.Issues)
	return manifest, result, nil
}

func validateManifest(manifest Manifest, maxPlaintextBytes int64, maxFiles int) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.BackupID == "" ||
		manifest.CreatedAt.IsZero() || strings.TrimSpace(manifest.SourceDeviceID) == "" ||
		manifest.SourceRecords < 0 || manifest.TotalFiles != len(manifest.Files) ||
		manifest.TotalBytes < 0 || manifest.ArchiveFormat != "tar+gzip+age" ||
		manifest.Encryption != "age-v1-native" || manifest.Privacy != PrivacyEncryptedEvidence {
		return errors.New("backup manifest envelope is invalid")
	}
	if manifest.SourceRecords == 0 && manifest.SourceLastRecordHash != "" ||
		manifest.SourceRecords > 0 && !validHash(manifest.SourceLastRecordHash) {
		return errors.New("backup manifest ledger identity is invalid")
	}
	var total int64
	previous := ""
	storeSeen := false
	for _, entry := range manifest.Files {
		if !validArchivePath(entry.Path) || !validHash(entry.SHA256) || entry.Bytes < 0 ||
			previous != "" && entry.Path <= previous || excludedVolatilePath(entry.Path, false) ||
			volatileLockPath(entry.Path) || entry.Path == "store.json" && entry.Bytes > 1<<20 {
			return errors.New("backup manifest file list is invalid")
		}
		previous = entry.Path
		total += entry.Bytes
		if total < 0 {
			return errors.New("backup manifest byte total overflowed")
		}
		if entry.Path == "store.json" {
			storeSeen = true
		}
	}
	if !storeSeen || total != manifest.TotalBytes {
		return errors.New("backup manifest totals or store entry are invalid")
	}
	if manifest.TotalBytes > maxPlaintextBytes || manifest.TotalFiles > maxFiles {
		return errors.New("backup manifest exceeds the configured restore limits")
	}
	expected, err := expectedBackupID(manifest)
	if err != nil || expected != manifest.BackupID {
		return errors.New("backup manifest identity does not match its content")
	}
	return nil
}

func restoreLimits(maxBytes int64, maxFiles int) (int64, int, error) {
	if maxBytes == 0 {
		maxBytes = DefaultMaxPlaintextBytes
	}
	if maxFiles == 0 {
		maxFiles = DefaultMaxFiles
	}
	if maxBytes < 1 || maxFiles < 1 {
		return 0, 0, errors.New("backup verification limits must be positive")
	}
	return maxBytes, maxFiles, nil
}

func validStoreManifest(data []byte, deviceID string) bool {
	var manifest struct {
		SchemaVersion string `json:"schema_version"`
		DeviceID      string `json:"device_id"`
	}
	return json.Unmarshal(data, &manifest) == nil &&
		manifest.SchemaVersion == "agent-memory-store/v1alpha1" && manifest.DeviceID == deviceID
}

func regularTarHeader(header *tar.Header) bool {
	return header != nil && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) &&
		header.Linkname == ""
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func uniqueSortedIssues(issues []VerificationIssue) []VerificationIssue {
	unique := map[string]VerificationIssue{}
	for _, issue := range issues {
		key := issue.Code + "\x00" + issue.PathSHA256 + "\x00" + issue.Message
		unique[key] = issue
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]VerificationIssue, 0, len(keys))
	for _, key := range keys {
		result = append(result, unique[key])
	}
	return result
}
