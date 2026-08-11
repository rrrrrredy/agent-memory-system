package ledger

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrEvidenceRootInGitWorktree marks an unsafe raw-evidence destination.
var ErrEvidenceRootInGitWorktree = errors.New("local evidence root must be outside every Git worktree")

func ensureEvidenceRootOutsideGit(root string) error {
	projected, err := resolveProjectedPath(root)
	if err != nil {
		return fmt.Errorf("resolve local evidence root: %w", err)
	}
	for current := filepath.Clean(projected); ; current = filepath.Dir(current) {
		_, statErr := os.Lstat(filepath.Join(current, ".git"))
		if statErr == nil {
			return ErrEvidenceRootInGitWorktree
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect local evidence ancestors: %w", statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
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
