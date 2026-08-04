package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/portable"
)

type historyVerification struct {
	commits int
	issues  []Issue
}

func Verify(ctx context.Context, repositoryRoot string) VerificationReport {
	return verifyRepository(ctx, repositoryRoot, true)
}

func verifyWorking(ctx context.Context, repositoryRoot string) VerificationReport {
	return verifyRepository(ctx, repositoryRoot, false)
}

func verifyRepository(ctx context.Context, repositoryRoot string, includeHistory bool) VerificationReport {
	report := VerificationReport{
		SchemaVersion: VerificationSchemaVersion,
		Issues:        []Issue{},
		Privacy:       portable.PortablePrivacy,
		Portable:      portable.VerifyRepository(repositoryRoot),
	}
	report.Issues = append(report.Issues, portableIssues(report.Portable.Issues, "")...)
	runner, _, err := ensureGitRoot(ctx, repositoryRoot)
	if err != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "git_repository_invalid", Message: err.Error(),
		})
		sortIssues(report.Issues)
		return report
	}
	report.GitRepository = true
	report.Branch = currentBranch(ctx, runner)
	if report.Branch == "" {
		report.Issues = append(report.Issues, Issue{
			Code: "detached_head", Message: "portable memory repository must be on a named branch",
		})
	}
	report.Head, _ = resolveCommit(ctx, runner, "HEAD")

	tracked, err := trackedPaths(ctx, runner)
	if err != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "tracked_tree_unavailable", Message: "Git tracked paths could not be inspected",
		})
	} else {
		report.TrackedFiles = len(tracked)
		for _, relative := range tracked {
			if !portable.IsDataPath(relative) {
				report.Issues = append(report.Issues, unknownPathIssue("tracked_path_not_allowlisted", relative,
					"Git tracks a path outside the portable memory allowlist"))
			}
		}
	}
	report.Issues = append(report.Issues, verifyIndex(ctx, runner)...)

	untracked, untrackedErr := runner.run(ctx, "inspect untracked data", "ls-files", "--others", "--exclude-standard", "-z", "--")
	if untrackedErr != nil {
		report.Issues = append(report.Issues, Issue{
			Code: "untracked_paths_unavailable", Message: "Git untracked paths could not be inspected",
		})
	} else {
		for _, relative := range nulFields(untracked.stdout) {
			if portable.IsDataPath(relative) {
				report.Issues = append(report.Issues, Issue{
					Code: "portable_data_untracked", Path: relative,
					Message: "portable memory data is not present in the Git index",
				})
			}
		}
	}

	unstaged := hasDiff(ctx, runner, false)
	staged := hasDiff(ctx, runner, true)
	report.WorkingTreeClean = !unstaged && !staged && len(nulFields(untracked.stdout)) == 0
	if unstaged {
		report.Issues = append(report.Issues, Issue{
			Code: "unstaged_changes", Message: "portable memory repository has unstaged changes",
		})
	}
	if report.Head != "" {
		deleted, deleteErr := runner.run(ctx, "inspect staged deletions",
			"diff", "--cached", "--diff-filter=D", "--name-only", "-z", "HEAD", "--")
		if deleteErr != nil {
			report.Issues = append(report.Issues, Issue{
				Code: "staged_deletions_unavailable", Message: "staged deletions could not be inspected",
			})
		} else {
			for _, relative := range nulFields(deleted.stdout) {
				report.Issues = append(report.Issues, safePathIssue(
					"append_only_deletion", relative,
					"portable memory Git history cannot delete a tracked data path",
				))
			}
		}
		if includeHistory {
			history := verifyHistory(ctx, runner, report.Head)
			report.CommitsChecked = history.commits
			report.Issues = append(report.Issues, history.issues...)
		}
	}
	sortIssues(report.Issues)
	return report
}

func verifyIndex(ctx context.Context, runner gitRunner) []Issue {
	listing, err := runner.run(ctx, "inspect portable memory Git index", "ls-files", "--stage", "-z", "--")
	if err != nil {
		return []Issue{{Code: "index_unavailable", Message: "portable memory Git index could not be inspected"}}
	}
	temporary, err := os.MkdirTemp("", "agentmem-git-index-")
	if err != nil {
		return []Issue{{Code: "index_unavailable", Message: "portable memory Git index could not be materialized"}}
	}
	defer os.RemoveAll(temporary)
	issues := []Issue{}
	blobs := map[string][]byte{}
	for _, record := range nulFields(listing.stdout) {
		header, relative, found := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !found || len(fields) != 3 {
			issues = append(issues, Issue{Code: "index_entry_invalid", Message: "Git index contains an invalid entry"})
			continue
		}
		mode, objectID, stage := fields[0], fields[1], fields[2]
		if !portable.IsDataPath(relative) {
			issues = append(issues, unknownPathIssue("index_path_not_allowlisted", relative,
				"Git index contains a path outside the portable memory allowlist"))
			continue
		}
		if mode != "100644" || stage != "0" {
			issues = append(issues, Issue{
				Code: "index_entry_invalid", Path: relative,
				Message: "portable memory data must be a resolved, non-executable regular Git blob",
			})
			continue
		}
		data, cached := blobs[objectID]
		if !cached {
			blob, blobErr := runner.run(ctx, "read portable memory index blob", "cat-file", "blob", objectID)
			if blobErr != nil {
				issues = append(issues, Issue{
					Code: "index_blob_unavailable", Path: relative,
					Message: "portable memory Git index contains an unreadable blob",
				})
				continue
			}
			data = append([]byte(nil), blob.stdout...)
			blobs[objectID] = data
		}
		destination := filepath.Join(temporary, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkdirErr != nil {
			issues = append(issues, Issue{Code: "index_unavailable", Message: "portable memory Git index could not be materialized"})
			continue
		}
		if writeErr := os.WriteFile(destination, data, 0o600); writeErr != nil {
			issues = append(issues, Issue{Code: "index_unavailable", Message: "portable memory Git index could not be materialized"})
		}
	}
	indexReport := portable.VerifyRepository(temporary)
	indexIssues := portableIssues(indexReport.Issues, "")
	for index := range indexIssues {
		indexIssues[index].Code = "index_" + indexIssues[index].Code
	}
	issues = append(issues, indexIssues...)
	sortIssues(issues)
	return issues
}

func verifyHistory(ctx context.Context, runner gitRunner, head string) historyVerification {
	verification := historyVerification{issues: []Issue{}}
	commitsResult, err := runner.run(ctx, "enumerate portable memory history",
		"rev-list", "--topo-order", "--reverse", head)
	if err != nil {
		verification.issues = append(verification.issues, Issue{
			Code: "history_unavailable", Message: "portable memory Git history could not be enumerated",
		})
		return verification
	}
	commits := lineFields(commitsResult.stdout)
	verification.commits = len(commits)
	trees := make(map[string]map[string]string, len(commits))
	blobs := map[string][]byte{}
	rootCount := 0
	for _, commit := range commits {
		tree, issues := verifyCommitTreeCached(ctx, runner, commit, blobs)
		verification.issues = append(verification.issues, issues...)
		trees[commit] = tree
		parentsResult, parentErr := runner.run(ctx, "inspect portable memory commit parents",
			"rev-list", "--parents", "-n", "1", commit)
		if parentErr != nil {
			verification.issues = append(verification.issues, Issue{
				Code: "history_parent_unavailable", Commit: commit,
				Message: "portable memory commit parents could not be inspected",
			})
			continue
		}
		fields := strings.Fields(strings.TrimSpace(string(parentsResult.stdout)))
		parentCount := len(fields) - 1
		if parentCount < 0 || parentCount > 2 {
			verification.issues = append(verification.issues, Issue{
				Code: "history_parent_count_invalid", Commit: commit,
				Message: "portable memory commits may have at most two parents",
			})
		}
		if issue := verifyCommitMessage(ctx, runner, commit, parentCount); issue != nil {
			verification.issues = append(verification.issues, *issue)
		}
		if len(fields) == 1 {
			rootCount++
		}
		for _, parent := range fields[1:] {
			parentTree, exists := trees[parent]
			if !exists {
				verification.issues = append(verification.issues, Issue{
					Code: "history_parent_order_invalid", Commit: commit,
					Message: "portable memory history contains an unavailable parent",
				})
				continue
			}
			for relative, objectID := range parentTree {
				currentObject, exists := tree[relative]
				switch {
				case !exists:
					verification.issues = append(verification.issues, Issue{
						Code: "history_deleted_path", Commit: commit, Path: relative,
						Message: "portable memory history deleted an immutable data path",
					})
				case currentObject != objectID:
					verification.issues = append(verification.issues, Issue{
						Code: "history_changed_path", Commit: commit, Path: relative,
						Message: "portable memory history changed an immutable data path",
					})
				}
			}
		}
	}
	if rootCount != 1 {
		verification.issues = append(verification.issues, Issue{
			Code: "history_root_conflict", Message: "portable memory Git history must have exactly one root commit",
		})
	}
	sortIssues(verification.issues)
	return verification
}

func verifyCommitMessage(
	ctx context.Context, runner gitRunner, commit string, parentCount int,
) *Issue {
	messageResult, err := runner.run(ctx, "inspect portable memory commit message",
		"show", "-s", "--format=%B", commit)
	if err != nil {
		return &Issue{
			Code: "history_commit_message_unavailable", Commit: commit,
			Message: "portable memory commit message could not be inspected",
		}
	}
	message := strings.TrimRight(string(messageResult.stdout), "\n")
	expected := ""
	switch parentCount {
	case 0:
		expected = "Initialize portable memory repository"
	case 1:
		expected = "Update promoted memory"
	case 2:
		expected = "Merge promoted memory revisions"
	}
	if strings.Contains(message, "\r") || message != expected {
		return &Issue{
			Code: "history_commit_message_invalid", Commit: commit,
			Message: "portable memory history contains a noncanonical commit message",
		}
	}
	return nil
}

func verifyCommitTree(ctx context.Context, runner gitRunner, commit string) (map[string]string, []Issue) {
	return verifyCommitTreeCached(ctx, runner, commit, map[string][]byte{})
}

func verifyCommitTreeCached(
	ctx context.Context, runner gitRunner, commit string, blobs map[string][]byte,
) (map[string]string, []Issue) {
	objects := map[string]string{}
	issues := []Issue{}
	listing, err := runner.run(ctx, "inspect portable memory commit tree",
		"ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return objects, []Issue{{
			Code: "history_tree_unavailable", Commit: commit,
			Message: "portable memory commit tree could not be inspected",
		}}
	}
	temporary, err := os.MkdirTemp("", "agentmem-git-tree-")
	if err != nil {
		return objects, []Issue{{
			Code: "history_tree_unavailable", Commit: commit,
			Message: "portable memory commit tree could not be materialized",
		}}
	}
	defer os.RemoveAll(temporary)
	for _, record := range nulFields(listing.stdout) {
		header, relative, found := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !found || len(fields) != 3 {
			issues = append(issues, Issue{
				Code: "history_tree_entry_invalid", Commit: commit,
				Message: "portable memory commit contains an invalid tree entry",
			})
			continue
		}
		mode, objectType, objectID := fields[0], fields[1], fields[2]
		if !portable.IsDataPath(relative) {
			issue := unknownPathIssue("history_path_not_allowlisted", relative,
				"portable memory history contains a path outside the data allowlist")
			issue.Commit = commit
			issues = append(issues, issue)
			continue
		}
		if mode != "100644" || objectType != "blob" {
			issues = append(issues, Issue{
				Code: "history_tree_entry_invalid", Commit: commit, Path: relative,
				Message: "portable memory data must be a non-executable regular Git blob",
			})
			continue
		}
		objects[relative] = objectID
		data, cached := blobs[objectID]
		if !cached {
			blob, blobErr := runner.run(ctx, "read portable memory history blob", "cat-file", "blob", objectID)
			if blobErr != nil {
				issues = append(issues, Issue{
					Code: "history_blob_unavailable", Commit: commit, Path: relative,
					Message: "portable memory history contains an unreadable blob",
				})
				continue
			}
			data = append([]byte(nil), blob.stdout...)
			blobs[objectID] = data
		}
		destination := filepath.Join(temporary, filepath.FromSlash(relative))
		if mkdirErr := os.MkdirAll(filepath.Dir(destination), 0o700); mkdirErr != nil {
			issues = append(issues, Issue{
				Code: "history_tree_unavailable", Commit: commit,
				Message: "portable memory commit tree could not be materialized",
			})
			continue
		}
		if writeErr := os.WriteFile(destination, data, 0o600); writeErr != nil {
			issues = append(issues, Issue{
				Code: "history_tree_unavailable", Commit: commit,
				Message: "portable memory commit tree could not be materialized",
			})
		}
	}
	portableReport := portable.VerifyRepository(temporary)
	issues = append(issues, portableIssues(portableReport.Issues, commit)...)
	sortIssues(issues)
	return objects, issues
}

func portableIssues(values []portable.VerificationIssue, commit string) []Issue {
	issues := make([]Issue, 0, len(values))
	for _, value := range values {
		issues = append(issues, Issue{
			Code: "portable_" + value.Code, Commit: commit,
			MemoryID: value.MemoryID, RevisionID: value.RevisionID,
			RelatedMemoryIDs:   append([]string(nil), value.RelatedMemoryIDs...),
			RelatedRevisionIDs: append([]string(nil), value.RelatedRevisionIDs...),
			Message:            value.Message,
		})
	}
	return issues
}

func trackedPaths(ctx context.Context, runner gitRunner) ([]string, error) {
	result, err := runner.run(ctx, "inspect tracked data", "ls-files", "-z", "--")
	if err != nil {
		return nil, err
	}
	paths := nulFields(result.stdout)
	sort.Strings(paths)
	return paths, nil
}

func currentBranch(ctx context.Context, runner gitRunner) string {
	result, err := runner.run(ctx, "inspect current Git branch", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(result.stdout))
}

func resolveCommit(ctx context.Context, runner gitRunner, reference string) (string, error) {
	result, err := runner.run(ctx, "resolve Git commit", "rev-parse", "--verify", reference+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.stdout)), nil
}

func hasDiff(ctx context.Context, runner gitRunner, staged bool) bool {
	args := []string{"diff", "--quiet"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	result, err := runner.run(ctx, "inspect Git changes", args...)
	return err != nil && result.exitCode == 1
}

func unknownPathIssue(code, relative, message string) Issue {
	digest := sha256.Sum256([]byte(relative))
	return Issue{Code: code, PathSHA256: hex.EncodeToString(digest[:]), Message: message}
}

func safePathIssue(code, relative, message string) Issue {
	if portable.IsDataPath(relative) {
		return Issue{Code: code, Path: relative, Message: message}
	}
	return unknownPathIssue(code, relative, message)
}

func verificationError(report VerificationReport) error {
	if len(report.Issues) == 0 {
		return nil
	}
	return fmt.Errorf("Git synchronization verification failed: %s", report.Issues[0].Message)
}

func hasIssueCode(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

var errNoHead = errors.New("Git repository has no commit")
