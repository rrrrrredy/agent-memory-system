package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const repositoryGuardHook = `#!/bin/sh
set -eu
# agentmem-portable-repository-guard/v1
repository=$(git rev-parse --show-toplevel)
exec "${AGENTMEM_BIN:-agentmem}" sync verify --repo "$repository"
`

func InstallHooks(ctx context.Context, repositoryRoot string) error {
	runner, _, err := ensureGitRoot(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	return installHooks(ctx, runner)
}

func installHooks(ctx context.Context, runner gitRunner) error {
	configured, configuredErr := runner.run(ctx, "inspect custom Git hooks path", "config", "--get", "core.hooksPath")
	if configuredErr == nil && strings.TrimSpace(string(configured.stdout)) != "" {
		return errors.New("custom core.hooksPath is configured; integrate the repository guard manually")
	}
	if configuredErr != nil && configured.exitCode != 1 {
		return errors.New("Git hooks configuration could not be inspected")
	}
	common, err := runner.run(ctx, "locate Git hooks directory", "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	commonPath := strings.TrimSpace(string(common.stdout))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(runner.repository, commonPath)
	}
	hooksDirectory := filepath.Join(filepath.Clean(commonPath), "hooks")
	if err := os.MkdirAll(hooksDirectory, 0o700); err != nil {
		return fmt.Errorf("create Git hooks directory: %w", err)
	}
	paths := []string{
		filepath.Join(hooksDirectory, "pre-commit"),
		filepath.Join(hooksDirectory, "pre-push"),
	}
	for _, path := range paths {
		if err := checkHook(path, []byte(repositoryGuardHook)); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := installHook(path, []byte(repositoryGuardHook)); err != nil {
			return err
		}
	}
	return nil
}

func checkHook(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("Git hook %s already exists with different content", filepath.Base(path))
		}
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("inspect Git hook %s: %w", filepath.Base(path), err)
}

func installHook(path string, data []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("Git hook %s already exists with different content", filepath.Base(path))
		}
		return os.Chmod(path, 0o700)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Git hook %s: %w", filepath.Base(path), err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agentmem-hook-*.tmp")
	if err != nil {
		return fmt.Errorf("create Git hook temporary file: %w", err)
	}
	temporary := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}
	if err := file.Chmod(0o700); err != nil {
		cleanup()
		return fmt.Errorf("set Git hook permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write Git hook: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync Git hook: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("close Git hook: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("install Git hook: %w", err)
	}
	return nil
}
