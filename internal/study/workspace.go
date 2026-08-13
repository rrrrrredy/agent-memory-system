package study

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
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

type workspaceTreeEntry struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

type workspaceTree struct {
	Entries           []workspaceTreeEntry
	SHA256            string
	Files             int
	UncompressedBytes int64
}

type countedReader struct {
	reader io.Reader
	bytes  int64
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
	tree, err := inspectWorkspaceArchive(store, snapshot.Archive, "")
	if err != nil {
		return snapshot, err
	}
	if tree.Files != snapshot.Files || tree.UncompressedBytes != snapshot.UncompressedBytes {
		return snapshot, errors.New("workspace archive measurements differ from the source snapshot")
	}
	snapshot.TreeSHA256 = tree.SHA256
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
		!validSHA256(snapshot.Archive.SHA256) || snapshot.Archive.Bytes < 1 || !validSHA256(snapshot.TreeSHA256) ||
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
	tree, err := inspectWorkspaceArchive(store, snapshot.Archive, "")
	if err != nil {
		return err
	}
	if tree.Files != snapshot.Files || tree.UncompressedBytes != snapshot.UncompressedBytes || tree.SHA256 != snapshot.TreeSHA256 {
		return errors.New("workspace archive measurements differ from the sealed snapshot")
	}
	return nil
}

func reserveIsolatedWorkspace(store *ledger.Store, forbiddenRoots ...string) (string, func() error, error) {
	root, err := os.MkdirTemp("", "agentmem-study-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() error { return os.RemoveAll(root) }
	cleanupFailure := func(cause error) error { return errors.Join(cause, cleanup()) }
	if err := os.Chmod(root, 0o700); err != nil {
		return "", nil, cleanupFailure(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, cleanupFailure(err)
	}
	root = resolved
	forbiddenRoots = append([]string{store.Root()}, forbiddenRoots...)
	for _, forbidden := range forbiddenRoots {
		if strings.TrimSpace(forbidden) == "" {
			continue
		}
		resolvedForbidden, resolveErr := filepath.EvalSymlinks(filepath.Clean(forbidden))
		if resolveErr != nil {
			return "", nil, cleanupFailure(resolveErr)
		}
		if pathsOverlap(root, resolvedForbidden) {
			return "", nil, cleanupFailure(errors.New("isolated study workspace overlaps a protected data root"))
		}
	}
	if insideGitWorktree(root) {
		return "", nil, cleanupFailure(errors.New("isolated study workspace must not be inside a Git worktree"))
	}
	return filepath.Join(root, "workspace"), cleanup, nil
}

func insideGitWorktree(item string) bool {
	current := filepath.Clean(item)
	for {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil || !os.IsNotExist(err) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func materializeWorkspace(store *ledger.Store, task PlannedTask, destination string) error {
	if err := verifyWorkspaceSnapshot(store, task.WorkspaceSnapshot, task.WorkingDirectory); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	tree, err := inspectWorkspaceArchive(store, task.WorkspaceSnapshot.Archive, destination)
	if err != nil {
		return err
	}
	if tree.Files != task.WorkspaceSnapshot.Files || tree.UncompressedBytes != task.WorkspaceSnapshot.UncompressedBytes ||
		tree.SHA256 != task.WorkspaceSnapshot.TreeSHA256 {
		return errors.New("materialized workspace differs from the sealed snapshot")
	}
	return verifyMaterializedWorkspace(destination, task.WorkspaceSnapshot)
}

func inspectWorkspaceArchive(store *ledger.Store, reference ledger.BlobRef, destination string) (workspaceTree, error) {
	input, err := store.OpenBlob(reference)
	if err != nil {
		return workspaceTree{}, err
	}
	hasher := sha256.New()
	measured := &countedReader{reader: io.TeeReader(input, hasher)}
	tree, walkErr := walkWorkspaceArchive(tar.NewReader(measured), destination)
	if walkErr == nil {
		_, walkErr = io.Copy(io.Discard, measured)
	}
	closeErr := input.Close()
	if walkErr != nil {
		return workspaceTree{}, walkErr
	}
	if closeErr != nil {
		return workspaceTree{}, closeErr
	}
	if measured.bytes != reference.Bytes || hex.EncodeToString(hasher.Sum(nil)) != reference.SHA256 {
		return workspaceTree{}, errors.New("workspace archive bytes differ from their content address")
	}
	return tree, nil
}

func (reader *countedReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.bytes += int64(count)
	return count, err
}

func walkWorkspaceArchive(archive *tar.Reader, destination string) (workspaceTree, error) {
	seen := map[string]struct{}{}
	tree := workspaceTree{Entries: []workspaceTreeEntry{}}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return workspaceTree{}, err
		}
		name := path.Clean(strings.TrimSuffix(header.Name, "/"))
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
			return workspaceTree{}, errors.New("workspace archive contains an unsafe path")
		}
		if _, duplicate := seen[name]; duplicate {
			return workspaceTree{}, errors.New("workspace archive contains a duplicate path")
		}
		seen[name] = struct{}{}
		target := ""
		if destination != "" {
			target = filepath.Join(destination, filepath.FromSlash(name))
			if !pathWithin(destination, target) {
				return workspaceTree{}, errors.New("workspace archive escaped its materialization root")
			}
		}
		switch header.Typeflag {
		case tar.TypeDir:
			tree.Entries = append(tree.Entries, workspaceTreeEntry{Kind: "directory", Path: name})
			if destination != "" {
				if err := os.MkdirAll(target, 0o700); err != nil {
					return workspaceTree{}, err
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 {
				return workspaceTree{}, errors.New("workspace archive contains a negative file size")
			}
			tree.Files++
			tree.UncompressedBytes += header.Size
			if tree.Files > maximumWorkspaceFiles || tree.UncompressedBytes > maximumWorkspaceBytes {
				return workspaceTree{}, errors.New("workspace archive exceeds the safety limit")
			}
			fileHasher := sha256.New()
			if destination == "" {
				if _, err := io.CopyN(fileHasher, archive, header.Size); err != nil {
					return workspaceTree{}, err
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
					return workspaceTree{}, err
				}
				mode := fs.FileMode(0o600) | (fs.FileMode(header.Mode) & 0o100)
				file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
				if err != nil {
					return workspaceTree{}, err
				}
				_, copyErr := io.CopyN(io.MultiWriter(file, fileHasher), archive, header.Size)
				closeErr := file.Close()
				if copyErr != nil {
					return workspaceTree{}, copyErr
				}
				if closeErr != nil {
					return workspaceTree{}, closeErr
				}
			}
			tree.Entries = append(tree.Entries, workspaceTreeEntry{Kind: "file", Path: name,
				SHA256: hex.EncodeToString(fileHasher.Sum(nil)), Bytes: header.Size})
		default:
			return workspaceTree{}, errors.New("workspace archive contains an unsupported entry type")
		}
	}
	return finalizeWorkspaceTree(tree)
}

func verifyMaterializedWorkspace(root string, snapshot WorkspaceSnapshot) error {
	tree, err := inspectMaterializedWorkspace(root)
	if err != nil {
		return err
	}
	if tree.Files != snapshot.Files || tree.UncompressedBytes != snapshot.UncompressedBytes || tree.SHA256 != snapshot.TreeSHA256 {
		return errors.New("materialized workspace tree differs from the sealed snapshot")
	}
	return nil
}

func inspectMaterializedWorkspace(root string) (workspaceTree, error) {
	tree := workspaceTree{Entries: []workspaceTreeEntry{}}
	err := filepath.WalkDir(root, func(itemPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, itemPath)
		if err != nil || relative == "." {
			return err
		}
		name := filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("materialized workspace contains an unsupported entry")
		}
		if info.IsDir() {
			tree.Entries = append(tree.Entries, workspaceTreeEntry{Kind: "directory", Path: name})
			return nil
		}
		file, err := os.Open(itemPath)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		bytesRead, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		tree.Files++
		tree.UncompressedBytes += bytesRead
		if tree.Files > maximumWorkspaceFiles || tree.UncompressedBytes > maximumWorkspaceBytes {
			return errors.New("materialized workspace exceeds the safety limit")
		}
		tree.Entries = append(tree.Entries, workspaceTreeEntry{Kind: "file", Path: name,
			SHA256: hex.EncodeToString(hasher.Sum(nil)), Bytes: bytesRead})
		return nil
	})
	if err != nil {
		return workspaceTree{}, err
	}
	return finalizeWorkspaceTree(tree)
}

func finalizeWorkspaceTree(tree workspaceTree) (workspaceTree, error) {
	sort.Slice(tree.Entries, func(i, j int) bool {
		if tree.Entries[i].Path == tree.Entries[j].Path {
			return tree.Entries[i].Kind < tree.Entries[j].Kind
		}
		return tree.Entries[i].Path < tree.Entries[j].Path
	})
	data, err := json.Marshal(tree.Entries)
	if err != nil {
		return workspaceTree{}, err
	}
	tree.SHA256 = digest(data)
	return tree, nil
}

func workspaceBinding(snapshot WorkspaceSnapshot) agentbridge.WorkspaceBinding {
	return agentbridge.WorkspaceBinding{Archive: snapshot.Archive, TreeSHA256: snapshot.TreeSHA256,
		Format: snapshot.Format, Policy: snapshot.Policy, Files: snapshot.Files,
		UncompressedBytes: snapshot.UncompressedBytes}
}

func validIsolatedWorkspacePath(store *ledger.Store, workspace string) bool {
	if store == nil || !filepath.IsAbs(workspace) || filepath.Base(workspace) != "workspace" ||
		pathsOverlap(workspace, store.Root()) {
		return false
	}
	return strings.HasPrefix(filepath.Base(filepath.Dir(workspace)), "agentmem-study-")
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
