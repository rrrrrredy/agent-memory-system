package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	manifestArchivePath = "manifest.json"
	storeArchivePrefix  = "store/"
)

func Create(store *ledger.Store, options CreateOptions) (result CreateResult, returnedErr error) {
	result = CreateResult{SchemaVersion: CreateResultSchemaVersion, Privacy: PrivacyEncryptedEvidence}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(options.Output) == "" {
		return result, errors.New("backup output path is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	recipients, err := parseRecipients(options.Recipients)
	if err != nil {
		return result, err
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return result, fmt.Errorf("resolve backup output: %w", err)
	}
	if pathsOverlap(store.Root(), output) {
		return result, errors.New("backup output and local evidence root must be separate")
	}
	if err := ensurePathOutsideGit(output); err != nil {
		return result, fmt.Errorf("backup output: %w", err)
	}
	if _, err := os.Lstat(output); err == nil {
		return result, errors.New("backup output already exists; refusing to overwrite it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("inspect backup output: %w", err)
	}
	verification := store.Verify()
	if len(verification.Issues) != 0 {
		return result, errors.New("local evidence verification failed before backup")
	}
	files, totalBytes, err := collectSourceFiles(store.Root())
	if err != nil {
		return result, err
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		CreatedAt:     options.Now().UTC(), SourceDeviceID: store.DeviceID(),
		SourceRecords:        verification.RecordsChecked,
		SourceLastRecordHash: verification.LastRecordHash,
		Files:                files, TotalFiles: len(files), TotalBytes: totalBytes,
		ArchiveFormat: "tar+gzip+age", Encryption: "age-v1-native",
		Privacy: PrivacyEncryptedEvidence,
	}
	manifest.BackupID, err = expectedBackupID(manifest)
	if err != nil {
		return result, err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, fmt.Errorf("encode backup manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')

	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return result, fmt.Errorf("create backup output directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".agentmem-backup-*.tmp")
	if err != nil {
		return result, fmt.Errorf("create backup temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := writeEncryptedArchive(temporary, store.Root(), manifest, manifestData, recipients); err != nil {
		return result, err
	}
	if err := temporary.Sync(); err != nil {
		return result, fmt.Errorf("sync backup archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return result, fmt.Errorf("close backup archive: %w", err)
	}
	if options.beforeFinalCheck != nil {
		options.beforeFinalCheck()
	}
	after, afterBytes, err := collectSourceFiles(store.Root())
	if err != nil {
		return result, fmt.Errorf("recheck backup source: %w", err)
	}
	if afterBytes != totalBytes || !sameFileEntries(files, after) {
		return result, errors.New("local evidence changed during backup; archive was not committed")
	}
	afterVerification := store.Verify()
	if len(afterVerification.Issues) != 0 ||
		afterVerification.RecordsChecked != verification.RecordsChecked ||
		afterVerification.LastRecordHash != verification.LastRecordHash {
		return result, errors.New("local evidence ledger changed during backup; archive was not committed")
	}
	archiveHash, archiveBytes, err := hashRegularFile(temporaryPath)
	if err != nil {
		return result, fmt.Errorf("hash backup archive: %w", err)
	}
	if err := commitArchiveNoReplace(temporaryPath, output); err != nil {
		return result, err
	}
	committed = true
	result.BackupID = manifest.BackupID
	result.CreatedAt = manifest.CreatedAt
	result.ArchiveSHA256 = archiveHash
	result.ArchiveBytes = archiveBytes
	result.SourceDeviceID = manifest.SourceDeviceID
	result.SourceRecords = manifest.SourceRecords
	result.SourceLastRecordHash = manifest.SourceLastRecordHash
	result.Files = manifest.TotalFiles
	result.PlaintextBytes = manifest.TotalBytes
	result.Recipients = len(recipients)
	return result, nil
}

func writeEncryptedArchive(destination *os.File, sourceRoot string, manifest Manifest,
	manifestData []byte, recipients []age.Recipient) error {
	encrypted, err := age.Encrypt(destination, recipients...)
	if err != nil {
		return fmt.Errorf("initialize backup encryption: %w", err)
	}
	compressed, err := gzip.NewWriterLevel(encrypted, gzip.BestSpeed)
	if err != nil {
		_ = encrypted.Close()
		return fmt.Errorf("initialize backup compression: %w", err)
	}
	compressed.Header.ModTime = time.Time{}
	archive := tar.NewWriter(compressed)
	closeAll := func(operationErr error) error {
		return errors.Join(operationErr, archive.Close(), compressed.Close(), encrypted.Close())
	}
	if err := archive.WriteHeader(&tar.Header{
		Name: manifestArchivePath, Mode: 0o600, Size: int64(len(manifestData)),
		ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
	}); err != nil {
		return closeAll(fmt.Errorf("write backup manifest header: %w", err))
	}
	if _, err := archive.Write(manifestData); err != nil {
		return closeAll(fmt.Errorf("write backup manifest: %w", err))
	}
	for _, entry := range manifest.Files {
		path := filepath.Join(sourceRoot, filepath.FromSlash(entry.Path))
		pathInfo, err := os.Lstat(path)
		if err != nil || !pathInfo.Mode().IsRegular() {
			return closeAll(errors.New("backup source changed before it was archived: " + pathDigest(entry.Path)))
		}
		file, err := os.Open(path)
		if err != nil {
			return closeAll(fmt.Errorf("open backup source file %s: %w", pathDigest(entry.Path), err))
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) ||
			info.Size() != entry.Bytes {
			_ = file.Close()
			return closeAll(errors.New("backup source changed before it was archived: " + pathDigest(entry.Path)))
		}
		if err := archive.WriteHeader(&tar.Header{
			Name: storeArchivePrefix + entry.Path, Mode: 0o600, Size: entry.Bytes,
			ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}); err != nil {
			_ = file.Close()
			return closeAll(fmt.Errorf("write backup file header %s: %w", pathDigest(entry.Path), err))
		}
		hasher := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(archive, hasher), file, entry.Bytes)
		var trailing [1]byte
		trailingBytes, trailingErr := file.Read(trailing[:])
		closeErr := file.Close()
		if copyErr != nil || written != entry.Bytes || trailingBytes != 0 ||
			trailingErr != nil && !errors.Is(trailingErr, io.EOF) || closeErr != nil {
			return closeAll(errors.New("backup source changed while it was archived: " + pathDigest(entry.Path)))
		}
		if hex.EncodeToString(hasher.Sum(nil)) != entry.SHA256 {
			return closeAll(errors.New("backup source hash changed while it was archived: " + pathDigest(entry.Path)))
		}
	}
	if err := archive.Close(); err != nil {
		_ = compressed.Close()
		_ = encrypted.Close()
		return fmt.Errorf("close backup tar stream: %w", err)
	}
	if err := compressed.Close(); err != nil {
		_ = encrypted.Close()
		return fmt.Errorf("close backup compression stream: %w", err)
	}
	if err := encrypted.Close(); err != nil {
		return fmt.Errorf("close backup encryption stream: %w", err)
	}
	return nil
}

func collectSourceFiles(root string) ([]FileEntry, int64, error) {
	entries := []FileEntry{}
	var total int64
	err := filepath.WalkDir(root, func(path string, directory fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if excludedVolatilePath(relative, directory.IsDir()) {
			if directory.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if volatileLockPath(relative) {
			return errors.New("a local evidence operation lock is present; backup requires a quiescent store")
		}
		if directory.IsDir() {
			return nil
		}
		info, err := directory.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("local evidence contains a link or non-regular file: " + pathDigest(relative))
		}
		digest, bytes, err := hashRegularFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, FileEntry{Path: relative, SHA256: digest, Bytes: bytes})
		total += bytes
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("collect backup source: %w", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	if len(entries) == 0 || entries[0].Path != "store.json" {
		found := false
		for _, entry := range entries {
			if entry.Path == "store.json" {
				found = true
				break
			}
		}
		if !found {
			return nil, 0, errors.New("backup source has no store.json")
		}
	}
	return entries, total, nil
}

func excludedVolatilePath(path string, directory bool) bool {
	if path == "evidence/tmp" || strings.HasPrefix(path, "evidence/tmp/") {
		return true
	}
	if directory && (strings.HasPrefix(path, "state/episode-derive-") ||
		strings.HasPrefix(path, "state/candidate-derive-") ||
		strings.Contains(path, "/.episode-generation-") ||
		strings.Contains(path, "/.candidate-generation-") ||
		strings.Contains(path, "/.corpus-tmp-") || strings.Contains(path, "/.run-tmp-")) {
		return true
	}
	return false
}

func volatileLockPath(path string) bool {
	return strings.HasPrefix(path, "state/") && strings.HasSuffix(path, ".lock")
}

func expectedBackupID(manifest Manifest) (string, error) {
	copy := manifest
	copy.BackupID = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode backup identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "backup-" + hex.EncodeToString(digest[:]), nil
}

func hashRegularFile(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = file.Close()
		return "", 0, errors.New("regular file changed before it could be hashed")
	}
	hasher := sha256.New()
	bytes, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", 0, errors.Join(copyErr, closeErr)
	}
	return hex.EncodeToString(hasher.Sum(nil)), bytes, nil
}

func sameFileEntries(left, right []FileEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func commitArchiveNoReplace(temporary, output string) error {
	if err := os.Link(temporary, output); err != nil {
		if _, statErr := os.Lstat(output); statErr == nil {
			return errors.New("backup output appeared during creation; refusing to overwrite it")
		}
		return fmt.Errorf("commit backup archive without replacement: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		_ = os.Remove(output)
		return fmt.Errorf("remove committed backup temporary link: %w", err)
	}
	return nil
}
