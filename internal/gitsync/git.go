package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type commandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type gitRunner struct {
	repository     string
	nonInteractive bool
}

func (runner gitRunner) run(ctx context.Context, operation string, args ...string) (commandResult, error) {
	commandArgs := append([]string{"-C", runner.repository}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if runner.nonInteractive {
		command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0")
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, safeGitError(operation, result)
	}
	return result, fmt.Errorf("%s could not start: %w", operation, err)
}

func safeGitError(operation string, result commandResult) error {
	message := string(result.stderr)
	switch {
	case strings.Contains(message, "Author identity unknown"),
		strings.Contains(message, "Please tell me who you are"):
		return errors.New("Git author identity is not configured")
	case strings.Contains(message, "Authentication failed"),
		strings.Contains(message, "could not read Username"),
		strings.Contains(message, "terminal prompts disabled"):
		return errors.New("Git authentication failed")
	case strings.Contains(message, "CONFLICT"):
		return errors.New("Git reported a textual merge conflict")
	default:
		return fmt.Errorf("%s failed with exit code %d", operation, result.exitCode)
	}
}

func ensureGitRoot(ctx context.Context, repository string) (gitRunner, string, error) {
	canonical, err := canonicalDirectory(repository)
	if err != nil {
		return gitRunner{}, "", err
	}
	runner := gitRunner{repository: canonical}
	result, err := runner.run(ctx, "inspect Git repository", "rev-parse", "--show-toplevel")
	if err != nil {
		return gitRunner{}, "", errors.New("portable memory directory is not a Git repository")
	}
	top := strings.TrimSpace(string(result.stdout))
	canonicalTop, err := canonicalDirectory(top)
	if err != nil {
		return gitRunner{}, "", errors.New("Git repository root is unavailable")
	}
	same, err := sameDirectory(canonical, canonicalTop)
	if err != nil || !same {
		return gitRunner{}, "", errors.New("portable memory directory must be the Git repository root")
	}
	runner.repository = canonicalTop
	return runner, canonicalTop, nil
}

func canonicalDirectory(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("repository root is required")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository root is unavailable")
	}
	return filepath.Clean(resolved), nil
}

func sameDirectory(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	if os.SameFile(leftInfo, rightInfo) {
		return true, nil
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right)), nil
	}
	return filepath.Clean(left) == filepath.Clean(right), nil
}

func nulFields(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func lineFields(data []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	result := lines[:0]
	for _, line := range lines {
		if value := strings.TrimSpace(line); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func sortIssues(issues []Issue) {
	for index := range issues {
		sort.Strings(issues[index].Paths)
	}
	sort.Slice(issues, func(left, right int) bool {
		a, b := issues[left], issues[right]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Commit != b.Commit {
			return a.Commit < b.Commit
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Message < b.Message
	})
}
