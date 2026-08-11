package publictree

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

func TestBlockedPathAndContentPatterns(t *testing.T) {
	if code, _ := blockedPath("data/evidence/ledger.jsonl"); code != "local_data_path" {
		t.Fatalf("evidence path was not blocked: %q", code)
	}
	if code, _ := blockedPath("notes/private.jsonl"); code != "unapproved_jsonl" {
		t.Fatalf("unapproved JSONL was not blocked: %q", code)
	}
	patterns := blockedContentPatterns("notes.md")
	foundRaw, foundMachine := false, false
	for _, pattern := range patterns {
		foundRaw = foundRaw || pattern.Code == "raw_transcript"
		foundMachine = foundMachine || pattern.Code == "machine_path"
	}
	if !foundRaw || !foundMachine {
		t.Fatalf("expected raw-transcript and machine-path patterns: %+v", patterns)
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
