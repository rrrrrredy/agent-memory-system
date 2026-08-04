package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ensureIdentityOutsideEvidenceStore(path string) error {
	projected, err := resolveProjectedPath(path)
	if err != nil {
		return fmt.Errorf("resolve identity path: %w", err)
	}
	for current := filepath.Dir(projected); ; current = filepath.Dir(current) {
		data, readErr := os.ReadFile(filepath.Join(current, "store.json"))
		if readErr == nil {
			var manifest struct {
				SchemaVersion string `json:"schema_version"`
			}
			if json.Unmarshal(data, &manifest) == nil &&
				manifest.SchemaVersion == "agent-memory-store/v1alpha1" {
				return errors.New("backup identity must be outside the local evidence store")
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("inspect identity path ancestors: %w", readErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func ensurePathOutsideGit(path string) error {
	projected, err := resolveProjectedPath(path)
	if err != nil {
		return err
	}
	current := projected
	if info, statErr := os.Lstat(current); statErr == nil && !info.IsDir() {
		current = filepath.Dir(current)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect path: %w", statErr)
	}
	for current = filepath.Clean(current); ; current = filepath.Dir(current) {
		_, statErr := os.Lstat(filepath.Join(current, ".git"))
		if statErr == nil {
			return errors.New("backup material must be outside every Git worktree")
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect path ancestors: %w", statErr)
		}
		bare, err := looksLikeBareGit(current)
		if err != nil {
			return err
		}
		if bare {
			return errors.New("backup material must be outside every bare Git repository")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func looksLikeBareGit(path string) (bool, error) {
	head, headErr := os.Lstat(filepath.Join(path, "HEAD"))
	objects, objectsErr := os.Lstat(filepath.Join(path, "objects"))
	refs, refsErr := os.Lstat(filepath.Join(path, "refs"))
	for _, err := range []error{headErr, objectsErr, refsErr} {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect bare Git ancestors: %w", err)
		}
	}
	return headErr == nil && head.Mode().IsRegular() &&
		objectsErr == nil && objects.IsDir() && refsErr == nil && refs.IsDir(), nil
}

func resolveProjectedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missing := []string{}
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", statErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathsOverlap(left, right string) bool {
	leftResolved, leftErr := resolveProjectedPath(left)
	rightResolved, rightErr := resolveProjectedPath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return pathWithin(leftResolved, rightResolved) || pathWithin(rightResolved, leftResolved)
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathDigest(value string) string {
	digest := sha256.Sum256([]byte(filepath.ToSlash(value)))
	return hex.EncodeToString(digest[:])
}

func validArchivePath(value string) bool {
	if value == "" || len(value) > 8192 || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.Contains(strings.SplitN(value, "/", 2)[0], ":") ||
		strings.ContainsRune(value, '\x00') {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
