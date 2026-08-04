package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestManualSyncSupportsFastForwardAndVerifiedDivergence(t *testing.T) {
	remote := newBareRemote(t)
	first := newDataRepository(t, remote, true)
	initial, err := Run(context.Background(), SyncOptions{RepositoryRoot: first})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Outcome != "remote_initialized" || !initial.Pushed {
		t.Fatalf("initial synchronization did not publish the root: %+v", initial)
	}
	second := cloneDataRepository(t, remote)

	writeRevision(t, first, testRootRevision(t, "Keep reports concise."))
	firstUpdate, err := Run(context.Background(), SyncOptions{RepositoryRoot: first})
	if err != nil {
		t.Fatal(err)
	}
	if firstUpdate.Outcome != "pushed" || !firstUpdate.LocalCommitCreated {
		t.Fatalf("first device did not publish its memory: %+v", firstUpdate)
	}
	secondUpdate, err := Run(context.Background(), SyncOptions{RepositoryRoot: second})
	if err != nil {
		t.Fatal(err)
	}
	if secondUpdate.Outcome != "fast_forwarded" || !secondUpdate.FastForwarded {
		t.Fatalf("second device did not fast-forward: %+v", secondUpdate)
	}

	writeRevision(t, second, testRootRevision(t, "Keep raw evidence on the local device."))
	if update, err := Run(context.Background(), SyncOptions{RepositoryRoot: second}); err != nil || !update.Pushed {
		t.Fatalf("second device did not publish its independent memory: %+v, %v", update, err)
	}
	writeRevision(t, first, testRootRevision(t, "Use a bounded retrieval budget."))
	merged, err := Run(context.Background(), SyncOptions{RepositoryRoot: first})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Outcome != "merged_and_pushed" || !merged.MergeCreated || !merged.Pushed {
		t.Fatalf("verified divergence did not merge: %+v", merged)
	}
	final, err := Run(context.Background(), SyncOptions{RepositoryRoot: second})
	if err != nil {
		t.Fatal(err)
	}
	if final.Outcome != "fast_forwarded" {
		t.Fatalf("merged history did not reach the second device: %+v", final)
	}
	report := portable.VerifyRepository(second)
	if len(report.Issues) != 0 || report.ActiveMemories != 3 {
		t.Fatalf("final portable repository is invalid: %+v", report)
	}
}

func TestManualSyncQuarantinesRevisionFork(t *testing.T) {
	remote := newBareRemote(t)
	first := newDataRepository(t, remote, false)
	if _, err := Run(context.Background(), SyncOptions{RepositoryRoot: first}); err != nil {
		t.Fatal(err)
	}
	root := testRootRevision(t, "Keep reports concise.")
	writeRevision(t, first, root)
	if _, err := Run(context.Background(), SyncOptions{RepositoryRoot: first}); err != nil {
		t.Fatal(err)
	}
	second := cloneDataRepository(t, remote)

	writeRevision(t, first, testChildRevision(t, root, "Keep reports under five hundred words."))
	firstResult, err := Run(context.Background(), SyncOptions{RepositoryRoot: first})
	if err != nil {
		t.Fatal(err)
	}
	writeRevision(t, second, testChildRevision(t, root, "Keep reports under eight hundred words."))
	secondResult, err := Run(context.Background(), SyncOptions{RepositoryRoot: second})
	if err == nil || secondResult.Outcome != "conflict" {
		t.Fatalf("revision fork was not quarantined: %+v, %v", secondResult, err)
	}
	if !hasSyncIssue(secondResult.Issues, "portable_revision_fork") ||
		!hasSyncIssue(secondResult.Issues, "portable_head_conflict") {
		t.Fatalf("fork evidence is incomplete: %+v", secondResult.Issues)
	}
	remoteHead := remoteBranchHead(t, remote)
	if remoteHead != firstResult.LocalCommitAfter {
		t.Fatalf("conflicting device changed the remote branch: got %s want %s", remoteHead, firstResult.LocalCommitAfter)
	}
	if head := strings.TrimSpace(runGit(t, second, "rev-parse", "HEAD")); head != secondResult.LocalCommitAfter {
		t.Fatalf("conflict changed the local branch: got %s want %s", head, secondResult.LocalCommitAfter)
	}
}

func TestVerificationRejectsDeletedHistoricalEvidence(t *testing.T) {
	repository := newDataRepository(t, "", false)
	rawPath := filepath.Join(repository, "raw-evidence.jsonl")
	if err := os.WriteFile(rawPath, []byte("synthetic local evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "-f", "--", "raw-evidence.jsonl")
	runGit(t, repository, "commit", "--no-verify", "-m", "Add invalid evidence")
	if err := os.Remove(rawPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "-A", "--", ".")
	runGit(t, repository, "commit", "--no-verify", "-m", "Remove invalid evidence")

	if current := portable.VerifyRepository(repository); len(current.Issues) != 0 {
		t.Fatalf("current portable tree should be canonical: %+v", current)
	}
	report := Verify(context.Background(), repository)
	if !hasSyncIssue(report.Issues, "history_path_not_allowlisted") {
		t.Fatalf("deleted historical evidence was not detected: %+v", report)
	}
	for _, issue := range report.Issues {
		if issue.Code == "history_path_not_allowlisted" &&
			(issue.Path != "" || issue.PathSHA256 == "") {
			t.Fatalf("unknown historical path was echoed or not fingerprinted: %+v", issue)
		}
	}
}

func TestManualSyncRejectsRemoteWithDeletedHistoricalEvidence(t *testing.T) {
	remote := newBareRemote(t)
	first := newDataRepository(t, remote, false)
	if _, err := Run(context.Background(), SyncOptions{RepositoryRoot: first}); err != nil {
		t.Fatal(err)
	}
	second := cloneDataRepository(t, remote)
	secondHead := strings.TrimSpace(runGit(t, second, "rev-parse", "HEAD"))

	rawPath := filepath.Join(first, "raw-evidence.jsonl")
	if err := os.WriteFile(rawPath, []byte("synthetic local evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, first, "add", "-f", "--", "raw-evidence.jsonl")
	runGit(t, first, "commit", "--no-verify", "-m", "Add invalid evidence")
	if err := os.Remove(rawPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, first, "add", "-A", "--", ".")
	runGit(t, first, "commit", "--no-verify", "-m", "Remove invalid evidence")
	runGit(t, first, "push", "--no-verify", DefaultRemote, DefaultBranch)

	result, err := Run(context.Background(), SyncOptions{RepositoryRoot: second})
	if err == nil || result.Outcome != "remote_rejected" ||
		!hasSyncIssue(result.Issues, "history_path_not_allowlisted") {
		t.Fatalf("invalid remote history was not rejected: %+v, %v", result, err)
	}
	if head := strings.TrimSpace(runGit(t, second, "rev-parse", "HEAD")); head != secondHead {
		t.Fatalf("invalid remote history changed the local branch: got %s want %s", head, secondHead)
	}
}

func TestVerificationRejectsForceAddedLocalState(t *testing.T) {
	repository := newDataRepository(t, "", false)
	statePath := filepath.Join(repository, ".agentmem", "captured.jsonl")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "-f", "--", ".agentmem/captured.jsonl")
	report := Verify(context.Background(), repository)
	if !hasSyncIssue(report.Issues, "tracked_path_not_allowlisted") {
		t.Fatalf("force-added local state was accepted: %+v", report)
	}
}

func TestVerificationChecksExactIndexBytes(t *testing.T) {
	repository := newDataRepository(t, "", false)
	relative := writeRevision(t, repository, testRootRevision(t, "Keep reports concise."))
	runGit(t, repository, "add", "--", relative)
	invalidBlob := strings.TrimSpace(runGitInput(t, repository, []byte("not a portable revision\n"),
		"hash-object", "-w", "--stdin"))
	runGit(t, repository, "update-index", "--cacheinfo", "100644,"+invalidBlob+","+relative)

	if current := portable.VerifyRepository(repository); len(current.Issues) != 0 {
		t.Fatalf("working tree fixture is invalid: %+v", current)
	}
	report := Verify(context.Background(), repository)
	if !hasSyncIssue(report.Issues, "index_portable_invalid_revision") {
		t.Fatalf("invalid staged bytes were not detected: %+v", report)
	}
}

func TestVerificationRejectsNoncanonicalCommitMessage(t *testing.T) {
	repository := newDataRepository(t, "", false)
	runGit(t, repository, "commit", "--allow-empty", "--no-verify", "-m", "Store task transcript")
	report := Verify(context.Background(), repository)
	if !hasSyncIssue(report.Issues, "history_commit_message_invalid") {
		t.Fatalf("arbitrary commit message was accepted: %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Store task transcript") {
		t.Fatal("invalid commit message was echoed in verification output")
	}
}

func TestBootstrapInstallsIdempotentRepositoryGuards(t *testing.T) {
	repository := newDataRepository(t, "", true)
	for _, name := range []string{"pre-commit", "pre-push"} {
		data, err := os.ReadFile(filepath.Join(repository, ".git", "hooks", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != repositoryGuardHook {
			t.Fatalf("unexpected %s hook", name)
		}
	}
	if err := InstallHooks(context.Background(), repository); err != nil {
		t.Fatalf("idempotent hook installation failed: %v", err)
	}
}

func TestHookConflictDoesNotPartiallyInstallGuards(t *testing.T) {
	repository := newDataRepository(t, "", false)
	hooksDirectory := filepath.Join(repository, ".git", "hooks")
	custom := filepath.Join(hooksDirectory, "pre-push")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(context.Background(), repository); err == nil {
		t.Fatal("existing hook was overwritten")
	}
	if _, err := os.Stat(filepath.Join(hooksDirectory, "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("pre-commit was partially installed: %v", err)
	}
	data, err := os.ReadFile(custom)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\nexit 0\n" {
		t.Fatal("existing pre-push hook changed")
	}
}

func TestBootstrapRejectsNestedRepositoryBeforeWriting(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, parent, "init", "--initial-branch", DefaultBranch)
	child := filepath.Join(parent, "private-memory")
	if _, err := Bootstrap(context.Background(), BootstrapOptions{RepositoryRoot: child}); err == nil {
		t.Fatal("nested data repository was accepted")
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatalf("bootstrap wrote into the parent worktree before rejecting it: %v", err)
	}
}

func newBareRemote(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "memory.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch", DefaultBranch, root)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize bare remote: %v: %s", err, output)
	}
	return root
}

func newDataRepository(t *testing.T, remote string, hooks bool) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "private-memory")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "--initial-branch", DefaultBranch)
	configureTestIdentity(t, repository)
	result, err := Bootstrap(context.Background(), BootstrapOptions{
		RepositoryRoot: repository, RemoteURL: remote, InstallHooks: hooks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Head == "" || result.HooksInstalled != hooks {
		t.Fatalf("unexpected bootstrap result: %+v", result)
	}
	return repository
}

func cloneDataRepository(t *testing.T, remote string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "private-memory")
	command := exec.Command("git", "clone", "--branch", DefaultBranch, "--", remote, repository)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone data repository: %v: %s", err, output)
	}
	configureTestIdentity(t, repository)
	return repository
}

func configureTestIdentity(t *testing.T, repository string) {
	t.Helper()
	runGit(t, repository, "config", "user.name", "Memory Test")
	runGit(t, repository, "config", "user.email", "memory-test@example.invalid")
}

func runGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runGitInput(t *testing.T, repository string, input []byte, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func remoteBranchHead(t *testing.T, remote string) string {
	t.Helper()
	command := exec.Command("git", "ls-remote", "--heads", remote, "refs/heads/"+DefaultBranch)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		t.Fatalf("unexpected remote branch output: %q", output)
	}
	return fields[0]
}

func testRootRevision(t *testing.T, text string) portable.Revision {
	t.Helper()
	scopeKind, scopeValue := review.ScopeGlobal, "*"
	revision := portable.Revision{
		Action: portable.ActionPromote, Status: portable.StatusActive,
		Kind: candidates.KindDirective, ScopeKind: scopeKind, ScopeValue: scopeValue,
		EvidenceBasis: []review.Basis{review.BasisExplicitRemember}, Text: text,
	}
	revision.TextSHA256 = digestText(text)
	revision.MemoryID = testMemoryID(revision.TextSHA256, scopeKind, scopeValue)
	return finalizeTestRevision(t, revision)
}

func testChildRevision(t *testing.T, parent portable.Revision, text string) portable.Revision {
	t.Helper()
	return finalizeTestRevision(t, portable.Revision{
		MemoryID: parent.MemoryID, ParentRevisionID: parent.RevisionID,
		Action: portable.ActionSupersede, Status: portable.StatusActive,
		Kind: candidates.KindCorrection, ScopeKind: parent.ScopeKind, ScopeValue: parent.ScopeValue,
		EvidenceBasis: []review.Basis{review.BasisUserCorrection}, Text: text,
		TextSHA256: digestText(text),
	})
}

func finalizeTestRevision(t *testing.T, revision portable.Revision) portable.Revision {
	t.Helper()
	revision.SchemaVersion = portable.RevisionSchemaVersion
	revision.RuleChangeAuthorization = "not_granted"
	revision.Privacy = portable.PortablePrivacy
	identity := revision
	identity.RevisionID = ""
	identity.Text = ""
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	revision.RevisionID = "portable-revision-" + hex.EncodeToString(digest[:])
	if _, err := portable.RenderRevision(revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func testMemoryID(textSHA string, kind review.ScopeKind, value string) string {
	envelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA, review.Scope{Kind: kind, Value: value}}
	data, _ := json.Marshal(envelope)
	digest := sha256.Sum256(data)
	return "memory-" + hex.EncodeToString(digest[:])
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func writeRevision(t *testing.T, repository string, revision portable.Revision) string {
	t.Helper()
	data, err := portable.RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash := strings.TrimPrefix(revision.MemoryID, "memory-")
	revisionHash := strings.TrimPrefix(revision.RevisionID, "portable-revision-")
	relative := filepath.Join("memories", memoryHash[:2], memoryHash, revisionHash+".md")
	path := filepath.Join(repository, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(relative)
}

func hasSyncIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
