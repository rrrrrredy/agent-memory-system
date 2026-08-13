package study

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const (
	maximumWorkspaceFiles = 100000
	maximumWorkspaceBytes = int64(4 * 1024 * 1024 * 1024)
)

type workspaceEntry struct {
	path     string
	relative string
	info     fs.FileInfo
}

func snapshotWorkspace(store *ledger.Store, root string) (WorkspaceSnapshot, error) {
	snapshot := WorkspaceSnapshot{SchemaVersion: WorkspaceSnapshotSchema, Format: WorkspaceArchiveFormat,
		Policy: WorkspaceSnapshotPolicy, SourcePathSHA256: digest([]byte(root))}
	if store == nil {
		return snapshot, errors.New("local evidence store is required")
	}
	if pathsOverlap(root, store.Root()) {
		return snapshot, errors.New("study workspace must not contain or be contained by the evidence store")
	}
	entries := []workspaceEntry{}
	err := filepath.WalkDir(root, func(itemPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, itemPath)
		if err != nil || relative == "." {
			return err
		}
		if excludedWorkspacePath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("workspace contains unsupported entry %s", filepath.ToSlash(relative))
		}
		relative = filepath.ToSlash(relative)
		if len(relative) > 4096 || strings.ContainsRune(relative, 0) {
			return errors.New("workspace entry path is invalid")
		}
		entries = append(entries, workspaceEntry{path: itemPath, relative: relative, info: info})
		if info.Mode().IsRegular() {
			snapshot.Files++
			snapshot.UncompressedBytes += info.Size()
			if snapshot.Files > maximumWorkspaceFiles || snapshot.UncompressedBytes > maximumWorkspaceBytes {
				return errors.New("workspace exceeds the snapshot safety limit")
			}
		}
		return nil
	})
	if err != nil {
		return snapshot, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].relative < entries[j].relative })
	reader, writer := io.Pipe()
	archiveErrors := make(chan error, 1)
	go func() {
		archiveErrors <- writeWorkspaceArchive(writer, entries)
		close(archiveErrors)
	}()
	reference, putErr := store.PutBlob(reader)
	if putErr != nil {
		_ = reader.CloseWithError(putErr)
	}
	archiveErr := <-archiveErrors
	if putErr != nil {
		return snapshot, putErr
	}
	if archiveErr != nil {
		return snapshot, archiveErr
	}
	snapshot.Archive = reference
	return snapshot, validateWorkspaceSnapshotEnvelope(snapshot, root)
}

func writeWorkspaceArchive(writer *io.PipeWriter, entries []workspaceEntry) error {
	archive := tar.NewWriter(writer)
	fail := func(err error) error {
		_ = archive.Close()
		_ = writer.CloseWithError(err)
		return err
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.relative, Mode: int64(entry.info.Mode().Perm()), Uid: 0, Gid: 0,
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX}
		if entry.info.IsDir() {
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = entry.info.Size()
		}
		if err := archive.WriteHeader(header); err != nil {
			return fail(err)
		}
		if !entry.info.Mode().IsRegular() {
			continue
		}
		file, err := os.Open(entry.path)
		if err != nil {
			return fail(err)
		}
		_, copyErr := io.CopyN(archive, file, entry.info.Size())
		tail := make([]byte, 1)
		count, tailErr := file.Read(tail)
		current, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return fail(statErr)
		}
		if count != 0 || !errors.Is(tailErr, io.EOF) || current.Size() != entry.info.Size() {
			_ = file.Close()
			return fail(errors.New("workspace file changed while its snapshot was being sealed"))
		}
		closeErr := file.Close()
		if copyErr != nil {
			return fail(copyErr)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
	}
	if err := archive.Close(); err != nil {
		return fail(err)
	}
	return writer.Close()
}

func validateWorkspaceSnapshotEnvelope(snapshot WorkspaceSnapshot, sourcePath string) error {
	if snapshot.SchemaVersion != WorkspaceSnapshotSchema || snapshot.Format != WorkspaceArchiveFormat ||
		snapshot.Policy != WorkspaceSnapshotPolicy || snapshot.SourcePathSHA256 != digest([]byte(sourcePath)) ||
		!validSHA256(snapshot.Archive.SHA256) || snapshot.Archive.Bytes < 1 ||
		strings.TrimSpace(snapshot.Archive.RelativePath) == "" || snapshot.Files < 0 ||
		snapshot.Files > maximumWorkspaceFiles || snapshot.UncompressedBytes < 0 ||
		snapshot.UncompressedBytes > maximumWorkspaceBytes {
		return errors.New("longitudinal workspace snapshot envelope is invalid")
	}
	return nil
}

func verifyWorkspaceSnapshot(store *ledger.Store, snapshot WorkspaceSnapshot, sourcePath string) error {
	if err := validateWorkspaceSnapshotEnvelope(snapshot, sourcePath); err != nil {
		return err
	}
	input, err := store.OpenBlob(snapshot.Archive)
	if err != nil {
		return err
	}
	files, bytesRead, err := walkWorkspaceArchive(tar.NewReader(input), "")
	closeErr := input.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if files != snapshot.Files || bytesRead != snapshot.UncompressedBytes {
		return errors.New("workspace archive measurements differ from the sealed snapshot")
	}
	return nil
}

func materializedWorkspaceDestination(store *ledger.Store, plan Plan, task PlannedTask) string {
	root := filepath.Join(store.Root(), "state", "study-workspaces")
	return filepath.Join(root, strings.TrimPrefix(plan.StudyID, "study-"), task.TaskID, "workspace")
}

func materializeWorkspace(store *ledger.Store, plan Plan, task PlannedTask) (string, error) {
	if err := verifyWorkspaceSnapshot(store, task.WorkspaceSnapshot, task.WorkingDirectory); err != nil {
		return "", err
	}
	root := filepath.Join(store.Root(), "state", "study-workspaces")
	destination := materializedWorkspaceDestination(store, plan, task)
	base := filepath.Dir(destination)
	if !pathWithin(root, destination) {
		return "", errors.New("materialized workspace path escaped the evidence state directory")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(base, ".materialize-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	input, err := store.OpenBlob(task.WorkspaceSnapshot.Archive)
	if err != nil {
		return "", err
	}
	files, bytesRead, extractErr := walkWorkspaceArchive(tar.NewReader(input), temporary)
	closeErr := input.Close()
	if extractErr != nil {
		return "", extractErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if files != task.WorkspaceSnapshot.Files || bytesRead != task.WorkspaceSnapshot.UncompressedBytes {
		return "", errors.New("materialized workspace measurements differ from the sealed snapshot")
	}
	if err := os.RemoveAll(destination); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func walkWorkspaceArchive(archive *tar.Reader, destination string) (int, int64, error) {
	seen := map[string]struct{}{}
	files := 0
	var bytesRead int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, err
		}
		name := path.Clean(strings.TrimSuffix(header.Name, "/"))
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
			return 0, 0, errors.New("workspace archive contains an unsafe path")
		}
		if _, duplicate := seen[name]; duplicate {
			return 0, 0, errors.New("workspace archive contains a duplicate path")
		}
		seen[name] = struct{}{}
		target := ""
		if destination != "" {
			target = filepath.Join(destination, filepath.FromSlash(name))
			if !pathWithin(destination, target) {
				return 0, 0, errors.New("workspace archive escaped its materialization root")
			}
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if destination != "" {
				if err := os.MkdirAll(target, fs.FileMode(header.Mode)&0o777); err != nil {
					return 0, 0, err
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			files++
			bytesRead += header.Size
			if files > maximumWorkspaceFiles || bytesRead > maximumWorkspaceBytes || header.Size < 0 {
				return 0, 0, errors.New("workspace archive exceeds the safety limit")
			}
			if destination == "" {
				if _, err := io.CopyN(io.Discard, archive, header.Size); err != nil {
					return 0, 0, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return 0, 0, err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fs.FileMode(header.Mode)&0o777)
			if err != nil {
				return 0, 0, err
			}
			_, copyErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return 0, 0, copyErr
			}
			if closeErr != nil {
				return 0, 0, closeErr
			}
		default:
			return 0, 0, errors.New("workspace archive contains an unsupported entry type")
		}
	}
	return files, bytesRead, nil
}

func excludedWorkspacePath(relative string) bool {
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		switch component {
		case ".git", ".hg", ".svn", ".agentmem":
			return true
		}
	}
	return false
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
	}
	return contains(left, right) || contains(right, left)
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
