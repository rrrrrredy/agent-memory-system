package agentassessment

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func writeImmutableArtifact(
	store *ledger.Store, category, identity, name string, data []byte, maximum int64,
) (string, bool, error) {
	if store == nil {
		return "", false, errors.New("store is required")
	}
	if filepath.Base(category) != category || filepath.Base(identity) != identity ||
		filepath.Base(name) != name || category == "" || identity == "" || name == "" {
		return "", false, errors.New("Agent assessment artifact path is unsafe")
	}
	if int64(len(data)) > maximum {
		return "", false, errors.New("Agent assessment artifact exceeds the safety limit")
	}
	root, err := secureStoreRoot(store)
	if err != nil {
		return "", false, err
	}
	base, err := ensureSecureDirectory(root,
		filepath.Join("derived", "evaluations", category))
	if err != nil {
		return "", false, err
	}
	destination := filepath.Join(base, identity)
	path := filepath.Join(destination, name)
	if _, err := os.Lstat(path); err == nil {
		existing, readErr := readSecureFile(root, path, maximum)
		if readErr != nil {
			return "", false, readErr
		}
		if !bytes.Equal(existing, data) {
			return "", false, errors.New("existing Agent assessment artifact does not match its identity")
		}
		return path, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", false, errors.New("existing Agent assessment artifact directory is incomplete")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	temporary, err := os.MkdirTemp(base, ".agent-assessment-tmp-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(temporary)
	if err := validateExistingPath(root, temporary, true); err != nil {
		return "", false, err
	}
	temporaryPath := filepath.Join(temporary, name)
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return "", false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", false, err
	}
	if err := file.Close(); err != nil {
		return "", false, err
	}
	baseAgain, err := ensureSecureDirectory(root,
		filepath.Join("derived", "evaluations", category))
	if err != nil || baseAgain != base {
		return "", false, errors.New("Agent assessment artifact directory changed before commit")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", false, errors.New("Agent assessment artifact destination appeared before commit")
		}
		return "", false, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return "", false, err
	}
	if _, err := readSecureFile(root, path, maximum); err != nil {
		return "", false, err
	}
	return path, false, nil
}

func secureStoreRoot(store *ledger.Store) (string, error) {
	if err := store.ValidateLocation(); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(store.Root())
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if unsafePathInfo(info) || !info.IsDir() {
		return "", errors.New("evidence root must be a real local directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func ensureSecureDirectory(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("artifact path must be relative")
	}
	target := filepath.Join(root, filepath.Clean(relative))
	if !pathWithin(target, root) {
		return "", errors.New("artifact path escapes the evidence root")
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if unsafePathInfo(info) || !info.IsDir() {
			return "", errors.New("artifact directory contains a link, reparse point, or special file")
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !pathWithin(resolved, root) {
			return "", errors.New("artifact directory resolves outside the evidence root")
		}
	}
	return target, nil
}

func readSecureFile(root, path string, maximum int64) ([]byte, error) {
	if err := validateExistingPath(root, path, false); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if opened.Size() < 0 || opened.Size() > maximum {
		return nil, errors.New("Agent assessment artifact exceeds the safety limit")
	}
	data := make([]byte, opened.Size())
	if _, err := io.ReadFull(file, data); err != nil {
		return nil, err
	}
	named, err := os.Lstat(path)
	if err != nil || unsafePathInfo(named) || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		return nil, errors.New("Agent assessment artifact changed while it was read")
	}
	if err := validateExistingPath(root, path, false); err != nil {
		return nil, err
	}
	return data, nil
}

func validateExistingPath(root, path string, finalDirectory bool) error {
	if !pathWithin(path, root) {
		return errors.New("artifact path is outside the evidence root")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if unsafePathInfo(info) {
			return errors.New("artifact path contains a link or reparse point")
		}
		last := index == len(parts)-1
		if (!last || finalDirectory) && !info.IsDir() {
			return errors.New("artifact path component must be a directory")
		}
		if last && !finalDirectory && !info.Mode().IsRegular() {
			return errors.New("artifact path must end in a regular file")
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil || !pathWithin(resolved, root) {
			return fmt.Errorf("artifact path resolves outside the evidence root")
		}
	}
	return nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
