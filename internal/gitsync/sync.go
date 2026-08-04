package gitsync

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

func Run(ctx context.Context, options SyncOptions) (Result, error) {
	result := Result{
		SchemaVersion: ResultSchemaVersion,
		Outcome:       "failed",
		Issues:        []Issue{},
		Privacy:       portable.PortablePrivacy,
	}
	remote, err := normalizeRemote(options.RemoteName)
	if err != nil {
		return result, err
	}
	branch := DefaultBranch
	result.Branch = branch
	message := "Update promoted memory"
	runner, root, err := ensureGitRoot(ctx, options.RepositoryRoot)
	if err != nil {
		return result, err
	}
	runner.nonInteractive = options.NonInteractive
	if err := configureRepositoryGit(ctx, runner); err != nil {
		return result, err
	}
	if current := currentBranch(ctx, runner); current != branch {
		return result, fmt.Errorf("portable memory repository must be on branch %q", branch)
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

	before, after, created, issues, err := prepareLocalCommit(ctx, runner, message, disabledHooks)
	result.LocalCommitBefore = before
	result.LocalCommitAfter = after
	result.LocalCommitCreated = created
	result.Issues = append(result.Issues, issues...)
	if err != nil {
		sortIssues(result.Issues)
		return result, err
	}

	remoteReference := "refs/heads/" + branch
	_, remoteExists, lookupErr := lookupRemoteCommit(ctx, runner, remote, remoteReference)
	if lookupErr != nil {
		return result, lookupErr
	}
	if !remoteExists {
		if err := pushCurrent(ctx, runner, remote, branch, disabledHooks, true); err != nil {
			return result, err
		}
		result.Outcome = "remote_initialized"
		result.Pushed = true
		if err := finishRun(lock, &released); err != nil {
			return result, err
		}
		return result, nil
	}
	incomingRef, err := temporaryIncomingRef()
	if err != nil {
		return result, err
	}
	defer func() {
		_, _ = runner.run(context.Background(), "remove temporary incoming reference", "update-ref", "-d", incomingRef)
	}()
	refspec := remoteReference + ":" + incomingRef
	if _, err := runner.run(ctx, "fetch remote memory branch", "fetch", "--no-tags", remote, refspec); err != nil {
		return result, err
	}
	result.Fetched = true
	remoteCommit, err := resolveCommit(ctx, runner, incomingRef)
	if err != nil {
		return result, err
	}
	result.RemoteCommit = remoteCommit
	remoteHistory := verifyHistory(ctx, runner, remoteCommit)
	if len(remoteHistory.issues) != 0 {
		result.Outcome = "remote_rejected"
		result.Issues = append(result.Issues, remoteHistory.issues...)
		sortIssues(result.Issues)
		return result, errors.New("remote memory history failed verification")
	}

	localCommit := result.LocalCommitAfter
	if localCommit == remoteCommit {
		result.Outcome = "up_to_date"
	} else {
		localIsAncestor, err := isAncestor(ctx, runner, localCommit, remoteCommit)
		if err != nil {
			return result, err
		}
		remoteIsAncestor, err := isAncestor(ctx, runner, remoteCommit, localCommit)
		if err != nil {
			return result, err
		}
		switch {
		case localIsAncestor:
			if _, err := runner.run(ctx, "fast-forward portable memory branch",
				withDisabledHooks(disabledHooks, "merge", "--ff-only", remoteCommit)...); err != nil {
				return result, err
			}
			result.Outcome = "fast_forwarded"
			result.FastForwarded = true
			result.LocalCommitAfter = remoteCommit
		case remoteIsAncestor:
			if err := pushCurrent(ctx, runner, remote, branch, disabledHooks, false); err != nil {
				result.Outcome = "retry_required"
				return result, err
			}
			result.Outcome = "pushed"
			result.Pushed = true
		default:
			mergeCommit, mergeIssues, mergeErr := mergeDivergence(
				ctx, runner, localCommit, remoteCommit, disabledHooks,
			)
			result.Issues = append(result.Issues, mergeIssues...)
			if mergeErr != nil {
				result.Outcome = "conflict"
				sortIssues(result.Issues)
				return result, mergeErr
			}
			result.MergeCreated = true
			result.FastForwarded = true
			result.LocalCommitAfter = mergeCommit
			if err := pushCurrent(ctx, runner, remote, branch, disabledHooks, false); err != nil {
				result.Outcome = "retry_required"
				return result, err
			}
			result.Outcome = "merged_and_pushed"
			result.Pushed = true
		}
	}
	if !result.Pushed {
		currentRemote, exists, err := lookupRemoteCommit(ctx, runner, remote, remoteReference)
		if err != nil {
			result.Outcome = "retry_required"
			return result, err
		}
		if !exists || currentRemote != result.LocalCommitAfter {
			result.Outcome = "retry_required"
			return result, errors.New("remote memory branch changed during synchronization; retry is required")
		}
	}
	finalReport := verifyWorking(ctx, root)
	if err := verificationError(finalReport); err != nil {
		result.Issues = append(result.Issues, finalReport.Issues...)
		sortIssues(result.Issues)
		return result, err
	}
	if result.LocalCommitAfter == "" {
		result.LocalCommitAfter, _ = resolveCommit(ctx, runner, "HEAD")
	}
	if err := finishRun(lock, &released); err != nil {
		return result, err
	}
	return result, nil
}

func prepareLocalCommit(
	ctx context.Context, runner gitRunner, message, disabledHooks string,
) (string, string, bool, []Issue, error) {
	issues := []Issue{}
	portableReport := portable.VerifyRepository(runner.repository)
	if len(portableReport.Issues) != 0 {
		issues = append(issues, portableIssues(portableReport.Issues, "")...)
		return "", "", false, issues, errors.New("portable memory working tree failed verification")
	}
	head, err := resolveCommit(ctx, runner, "HEAD")
	if err != nil {
		return "", "", false, issues, errNoHead
	}
	history := verifyHistory(ctx, runner, head)
	if len(history.issues) != 0 {
		return head, head, false, history.issues, errors.New("local portable memory history failed verification")
	}
	tracked, err := trackedPaths(ctx, runner)
	if err != nil {
		return head, head, false, issues, err
	}
	for _, relative := range tracked {
		if !portable.IsDataPath(relative) {
			issues = append(issues, unknownPathIssue("tracked_path_not_allowlisted", relative,
				"Git tracks a path outside the portable memory allowlist"))
		}
	}
	deleted, err := runner.run(ctx, "inspect missing tracked data", "ls-files", "--deleted", "-z", "--")
	if err != nil {
		return head, head, false, issues, err
	}
	for _, relative := range nulFields(deleted.stdout) {
		issues = append(issues, safePathIssue(
			"append_only_deletion", relative,
			"portable memory Git history cannot delete a tracked data path",
		))
	}
	if len(issues) != 0 {
		sortIssues(issues)
		return head, head, false, issues, errors.New("local portable memory repository is not append-only")
	}
	if _, err := runner.run(ctx, "stage portable memory changes", "add", "-A", "--", "."); err != nil {
		return head, head, false, issues, err
	}
	report := verifyWorking(ctx, runner.repository)
	if err := verificationError(report); err != nil {
		return head, head, false, report.Issues, err
	}
	created := hasDiff(ctx, runner, true)
	if created {
		if _, err := runner.run(ctx, "commit portable memory changes",
			withDisabledHooks(disabledHooks, "commit", "--no-verify", "-m", message)...); err != nil {
			return head, head, false, issues, err
		}
	}
	after, err := resolveCommit(ctx, runner, "HEAD")
	if err != nil {
		return head, "", created, issues, err
	}
	if created {
		_, commitIssues := verifyCommitTree(ctx, runner, after)
		if len(commitIssues) != 0 {
			return head, after, created, commitIssues, errors.New("local portable memory commit failed verification")
		}
	}
	final := verifyWorking(ctx, runner.repository)
	if err := verificationError(final); err != nil {
		return head, after, created, final.Issues, err
	}
	return head, after, created, issues, nil
}

func mergeDivergence(
	ctx context.Context, runner gitRunner, localCommit, remoteCommit, disabledHooks string,
) (string, []Issue, error) {
	workspace, err := os.MkdirTemp(filepath.Dir(runner.repository), ".agentmem-merge-")
	if err != nil {
		return "", nil, fmt.Errorf("create isolated merge workspace: %w", err)
	}
	if err := os.Remove(workspace); err != nil {
		return "", nil, fmt.Errorf("prepare isolated merge workspace: %w", err)
	}
	added := false
	defer func() {
		if added {
			_, _ = runner.run(context.Background(), "remove isolated merge workspace",
				"worktree", "remove", "--force", workspace)
		}
		_ = os.RemoveAll(workspace)
	}()
	if _, err := runner.run(ctx, "create isolated merge workspace",
		withDisabledHooks(disabledHooks, "worktree", "add", "--detach", workspace, localCommit)...); err != nil {
		return "", nil, err
	}
	added = true
	mergeRunner := gitRunner{repository: workspace, nonInteractive: runner.nonInteractive}
	mergeResult, mergeErr := mergeRunner.run(ctx, "merge portable memory revisions",
		withDisabledHooks(disabledHooks, "merge", "--no-commit", "--no-ff", remoteCommit)...)
	if mergeErr != nil {
		conflicts, _ := mergeRunner.run(ctx, "inspect textual merge conflicts",
			"diff", "--name-only", "--diff-filter=U", "-z", "--")
		paths := []string{}
		for _, relative := range nulFields(conflicts.stdout) {
			if portable.IsDataPath(relative) {
				paths = append(paths, relative)
			}
		}
		issue := Issue{
			Code: "textual_merge_conflict", Paths: paths,
			Message:   "portable memory branches have a textual Git conflict",
			RecoverBy: "resolve the conflicting revisions explicitly, then retry synchronization",
		}
		if len(paths) == 0 && mergeResult.exitCode != 0 {
			issue.Message = "portable memory branches could not be merged automatically"
		}
		return "", []Issue{issue}, mergeErr
	}
	report := verifyWorking(ctx, workspace)
	report.Issues = withoutIssueCode(report.Issues, "detached_head")
	if err := verificationError(report); err != nil {
		return "", report.Issues, errors.New("merged portable memory state requires explicit conflict resolution")
	}
	if _, err := mergeRunner.run(ctx, "commit merged portable memory revisions",
		withDisabledHooks(disabledHooks, "commit", "--no-verify", "-m", "Merge promoted memory revisions")...); err != nil {
		return "", nil, err
	}
	mergeCommit, err := resolveCommit(ctx, mergeRunner, "HEAD")
	if err != nil {
		return "", nil, err
	}
	history := verifyHistory(ctx, runner, mergeCommit)
	if len(history.issues) != 0 {
		return "", history.issues, errors.New("merged portable memory history failed verification")
	}
	current, err := resolveCommit(ctx, runner, "HEAD")
	if err != nil || current != localCommit || hasDiff(ctx, runner, false) || hasDiff(ctx, runner, true) {
		return "", nil, errors.New("local portable memory branch changed during isolated merge")
	}
	if _, err := runner.run(ctx, "advance local portable memory branch",
		withDisabledHooks(disabledHooks, "merge", "--ff-only", mergeCommit)...); err != nil {
		return "", nil, err
	}
	return mergeCommit, nil, nil
}

func pushCurrent(
	ctx context.Context, runner gitRunner, remote, branch, disabledHooks string, setUpstream bool,
) error {
	args := []string{"push", "--no-verify"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, remote, "HEAD:refs/heads/"+branch)
	_, err := runner.run(ctx, "push portable memory branch", withDisabledHooks(disabledHooks, args...)...)
	return err
}

func lookupRemoteCommit(
	ctx context.Context, runner gitRunner, remote, reference string,
) (string, bool, error) {
	lookup, err := runner.run(ctx, "inspect remote memory branch",
		"ls-remote", "--exit-code", "--heads", remote, reference)
	if err != nil {
		if lookup.exitCode == 2 {
			return "", false, nil
		}
		return "", false, err
	}
	fields := strings.Fields(strings.TrimSpace(string(lookup.stdout)))
	if len(fields) != 2 || fields[1] != reference {
		return "", false, errors.New("remote memory branch returned an invalid reference")
	}
	return fields[0], true, nil
}

func isAncestor(ctx context.Context, runner gitRunner, ancestor, descendant string) (bool, error) {
	result, err := runner.run(ctx, "compare portable memory histories",
		"merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if result.exitCode == 1 {
		return false, nil
	}
	return false, err
}

func temporaryIncomingRef() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("create temporary Git reference: %w", err)
	}
	return "refs/agentmem/incoming/" + hex.EncodeToString(random), nil
}

func withDisabledHooks(directory string, args ...string) []string {
	return append([]string{
		"-c", "core.hooksPath=" + directory,
		"-c", "commit.gpgSign=false",
	}, args...)
}

func withoutIssueCode(issues []Issue, code string) []Issue {
	result := issues[:0]
	for _, issue := range issues {
		if issue.Code != code {
			result = append(result, issue)
		}
	}
	return result
}

func finishRun(lock *portable.RepositoryLock, released *bool) error {
	if err := lock.Release(); err != nil {
		return fmt.Errorf("Git synchronization completed but repository lock cleanup failed: %w", err)
	}
	*released = true
	return nil
}
