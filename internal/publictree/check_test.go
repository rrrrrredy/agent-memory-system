package publictree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublishedTreePassesCurrentPrivacyGate(t *testing.T) {
	report := Check(context.Background(), repositoryRoot(t), Options{IncludeUntracked: true})
	if !report.Clean() {
		t.Fatalf("public tree privacy gate failed: %+v", report.Issues)
	}
	if report.FilesChecked == 0 || report.QuickstartSHA256 == "" {
		t.Fatalf("public tree report is incomplete: %+v", report)
	}
}

func TestBlockedPathsUseComponents(t *testing.T) {
	for _, path := range []string{
		"data/evidence/ledger.jsonl",
		"docs/private-memory/memory.md",
		"tmp/backups/snapshot.txt",
		"nested/sessions/raw.txt",
	} {
		if code, _ := blockedPath(path); code != "local_data_path" {
			t.Fatalf("sensitive path %q was not blocked: %q", path, code)
		}
	}
	if code, _ := blockedPath("notes/private.jsonl"); code != "unapproved_jsonl" {
		t.Fatalf("unapproved JSONL was not blocked: %q", code)
	}
}

func TestBlobScanRejectsFormattingExtensionBinaryAndCredentials(t *testing.T) {
	cases := []struct {
		path string
		data []byte
		code string
	}{
		{"private.go", []byte("package fixture\nconst data = `{\"type\" : \"session_meta\"}`\n"), "raw_transcript"},
		{"notes.md", []byte("user: private instruction\n"), "raw_transcript"},
		{"payload.bin", []byte{'a', 0, 'b'}, "unapproved_binary"},
		{"config.txt", []byte("Authorization: Bearer abcdefghijklmnopqrstuvwxyz\n"), "secret_material"},
		{"notes.txt", []byte("01890f7e-1a2b-7c3d-8e9f-0123456789ab\n"), "task_trace"},
		{"notes.txt", []byte(strings.Join([]string{"maintainer", "review notes\n"}, " ")), "process_trace"},
	}
	for _, test := range cases {
		report := scanBlob(Report{Issues: []Issue{}}, test.path, test.data, exactAllowlist{}, false)
		if !hasIssue(report, test.code) {
			t.Fatalf("%s did not reject %q: %+v", test.code, test.path, report.Issues)
		}
	}
}

func TestExactAllowlistDoesNotAuthorizeChangedBytes(t *testing.T) {
	path := "internal/example_test.go"
	data := []byte("package fixture\nconst data = `{\"type\":\"session_meta\"}`\n")
	digest := sha256.Sum256(data)
	allowed := exactAllowlist{Entries: map[string]allowlistEntry{
		path + "\x00" + hex.EncodeToString(digest[:]): {
			Path: path, SHA256: hex.EncodeToString(digest[:]), Reason: "synthetic fixture",
		},
	}}
	if report := scanBlob(Report{Issues: []Issue{}}, path, data, allowed, false); !report.Clean() {
		t.Fatalf("exact synthetic fixture was rejected: %+v", report.Issues)
	}
	changed := append(append([]byte{}, data...), '\n')
	if report := scanBlob(Report{Issues: []Issue{}}, path, changed, allowed, false); report.Clean() {
		t.Fatal("changed synthetic fixture was accepted by an old hash")
	}
}

func TestDeclaredPublicIdentityIsNotReportedAsPrivate(t *testing.T) {
	allowed := exactAllowlist{PublicIdentities: map[string]struct{}{"owner@example.invalid": {}}}
	report := scanBlob(Report{Issues: []Issue{}}, "commit-metadata", []byte("owner@example.invalid"), allowed, true)
	if !report.Clean() {
		t.Fatalf("declared public identity was rejected: %+v", report.Issues)
	}
}

func TestHistoryScansDeletedGoTranscript(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Repository Test")
	runGit(t, root, "config", "user.email", "test@users.noreply.github.com")
	writePublicFixture(t, root)
	leak := filepath.Join(root, "private.go")
	if err := os.WriteFile(leak, []byte("package fixture\nconst data = `{\"type\" : \"session_meta\"}`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "Add fixture")
	if err := os.Remove(leak); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-u")
	runGit(t, root, "commit", "-m", "Remove fixture")
	report := Check(context.Background(), root, Options{IncludeHistory: true})
	if !hasIssue(report, "historical_raw_transcript") {
		t.Fatalf("deleted Go transcript was not found in history: %+v", report.Issues)
	}
}

func TestFixtureManifestRejectsTamperedRollout(t *testing.T) {
	root := repositoryRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(quickstartManifestPath)))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(approvedFixturePath())))
	if err != nil {
		t.Fatal(err)
	}
	report := verifyFixture(Report{Issues: []Issue{}}, map[string]treeEntry{
		quickstartManifestPath: {Path: quickstartManifestPath},
		approvedFixturePath():  {Path: approvedFixturePath()},
	}, map[string][]byte{
		quickstartManifestPath: manifest,
		approvedFixturePath():  append(append([]byte{}, fixture...), byte('\n')),
	})
	if report.Clean() {
		t.Fatal("tampered public fixture was accepted")
	}
}

func writePublicFixture(t *testing.T, root string) {
	t.Helper()
	fixturePath := filepath.Join(root, filepath.FromSlash(approvedFixturePath()))
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := []byte("synthetic fixture\n")
	if err := os.WriteFile(fixturePath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fixture)
	manifest := fixtureManifest{
		SchemaVersion: "quickstart-fixture/v1alpha1", RolloutPath: filepath.Base(fixturePath),
		RolloutSHA256: hex.EncodeToString(digest[:]), ExpectedReviewReady: 1,
		ExpectedCandidateID: "candidate-synthetic", ExpectedCandidateText: "synthetic",
		ExpectedTextSHA256: strings.Repeat("a", 64), RetrievalQuery: "synthetic", Privacy: "synthetic_public",
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(quickstartManifestPath)), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	allowlistData := []byte("{\"schema_version\":\"public-tree-allowlist/v1alpha1\",\"public_identities\":[],\"entries\":[],\"privacy\":\"synthetic_public\"}\n")
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(publicAllowlistPath)), allowlistData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func hasIssue(report Report, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}
