package gitsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

func Bootstrap(ctx context.Context, options BootstrapOptions) (BootstrapResult, error) {
	result := BootstrapResult{
		SchemaVersion: BootstrapSchemaVersion,
		Privacy:       portable.PortablePrivacy,
	}
	remote, err := normalizeRemote(options.RemoteName)
	if err != nil {
		return result, err
	}
	branch := DefaultBranch
	if err := rejectNestedGitBeforeBootstrap(ctx, options.RepositoryRoot); err != nil {
		return result, err
	}
	if err := portable.InitRepository(options.RepositoryRoot); err != nil {
		return result, err
	}
	root, err := canonicalDirectory(options.RepositoryRoot)
	if err != nil {
		return result, err
	}
	gitMarker := filepath.Join(root, ".git")
	if _, statErr := os.Lstat(gitMarker); errors.Is(statErr, os.ErrNotExist) {
		probe := gitRunner{repository: root}
		if _, initErr := probe.run(ctx, "initialize portable memory Git repository",
			"init", "--initial-branch", branch); initErr != nil {
			return result, initErr
		}
	} else if statErr != nil {
		return result, fmt.Errorf("inspect portable memory Git metadata: %w", statErr)
	}
	runner, root, err := ensureGitRoot(ctx, root)
	if err != nil {
		return result, err
	}
	if err := configureRepositoryGit(ctx, runner); err != nil {
		return result, err
	}
	lock, err := portable.AcquireRepositoryLock(root)
	if err != nil {
		return result, err
	}
	released := false
	defer func() {
		if !released {
			_ = lock.Release()
		}
	}()
	disabledHooks, err := os.MkdirTemp(filepath.Join(root, ".agentmem"), "disabled-hooks-")
	if err != nil {
		return result, fmt.Errorf("create isolated Git hooks directory: %w", err)
	}
	defer os.RemoveAll(disabledHooks)
	if current := currentBranch(ctx, runner); current != branch {
		return result, fmt.Errorf("portable memory repository must use branch %q", branch)
	}
	result.Branch = branch
	if strings.TrimSpace(options.RemoteURL) != "" {
		configured, remoteErr := configureRemote(ctx, runner, remote, options.RemoteURL)
		if remoteErr != nil {
			return result, remoteErr
		}
		result.RemoteConfigured = configured
	}
	head, headErr := resolveCommit(ctx, runner, "HEAD")
	if headErr != nil {
		if _, err := runner.run(ctx, "stage portable memory repository", "add", "-A", "--", "."); err != nil {
			return result, err
		}
		report := Verify(ctx, root)
		if err := verificationError(report); err != nil {
			return result, err
		}
		if _, err := runner.run(ctx, "create portable memory root commit",
			withDisabledHooks(disabledHooks, "commit", "--no-verify", "-m", "Initialize portable memory repository")...); err != nil {
			return result, err
		}
		head, err = resolveCommit(ctx, runner, "HEAD")
		if err != nil {
			return result, err
		}
	} else {
		report := Verify(ctx, root)
		if err := verificationError(report); err != nil {
			return result, err
		}
		if !report.WorkingTreeClean {
			return result, errors.New("bootstrap will not modify an existing portable memory Git history")
		}
	}
	result.Head = head
	report := Verify(ctx, root)
	if err := verificationError(report); err != nil {
		return result, err
	}
	if options.InstallHooks {
		if err := installHooks(ctx, runner); err != nil {
			return result, err
		}
		result.HooksInstalled = true
	}
	if err := lock.Release(); err != nil {
		return result, fmt.Errorf("bootstrap completed but repository lock cleanup failed: %w", err)
	}
	released = true
	return result, nil
}

func rejectNestedGitBeforeBootstrap(ctx context.Context, repositoryRoot string) error {
	if strings.TrimSpace(repositoryRoot) == "" {
		return errors.New("repository root is required")
	}
	absolute, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(absolute, ".git")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect portable memory Git metadata: %w", err)
	}
	probeDirectory := absolute
	for {
		info, statErr := os.Stat(probeDirectory)
		if statErr == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(probeDirectory)
		if parent == probeDirectory {
			return nil
		}
		probeDirectory = parent
	}
	probe := gitRunner{repository: probeDirectory}
	if _, probeErr := probe.run(ctx, "inspect parent Git repository", "rev-parse", "--show-toplevel"); probeErr == nil {
		return errors.New("portable memory repository cannot be nested inside another Git worktree")
	}
	return nil
}

func configureRemote(ctx context.Context, runner gitRunner, remote, remoteURL string) (bool, error) {
	if strings.HasPrefix(strings.TrimSpace(remoteURL), "-") || strings.ContainsAny(remoteURL, "\x00\r\n") {
		return false, errors.New("remote URL is invalid")
	}
	existing, err := runner.run(ctx, "inspect Git remote", "remote", "get-url", remote)
	if err == nil {
		if strings.TrimSpace(string(existing.stdout)) != strings.TrimSpace(remoteURL) {
			return false, errors.New("Git remote already exists with a different URL")
		}
		return true, nil
	}
	if existing.exitCode != 2 {
		return false, errors.New("Git remote configuration could not be inspected")
	}
	if _, err := runner.run(ctx, "configure Git remote", "remote", "add", remote, remoteURL); err != nil {
		return false, err
	}
	return true, nil
}

func configureRepositoryGit(ctx context.Context, runner gitRunner) error {
	if _, err := runner.run(ctx, "enable long portable memory paths",
		"config", "--local", "core.longpaths", "true"); err != nil {
		return err
	}
	if _, err := runner.run(ctx, "disable implicit line-ending conversion",
		"config", "--local", "core.autocrlf", "false"); err != nil {
		return err
	}
	return nil
}

func normalizeRemote(remote string) (string, error) {
	if remote == "" {
		remote = DefaultRemote
	}
	if !validSimpleName(remote) {
		return "", errors.New("Git remote name is invalid")
	}
	return remote, nil
}

func validSimpleName(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || len(value) > 255 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return true
}
