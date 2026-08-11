package publictree

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const quickstartManifestPath = "examples/quickstart/manifest.json"

type Options struct {
	IncludeUntracked bool
	IncludeHistory   bool
}

type Issue struct {
	Code    string
	Path    string
	Message string
}

type Report struct {
	FilesChecked     int
	HistoryChecked   bool
	QuickstartSHA256 string
	Issues           []Issue
}

func (r Report) Clean() bool { return len(r.Issues) == 0 }

type fixtureManifest struct {
	SchemaVersion         string `json:"schema_version"`
	RolloutPath           string `json:"rollout_path"`
	RolloutSHA256         string `json:"rollout_sha256"`
	ExpectedReviewReady   int    `json:"expected_review_ready"`
	ExpectedCandidateID   string `json:"expected_candidate_id"`
	ExpectedCandidateText string `json:"expected_candidate_text"`
	ExpectedTextSHA256    string `json:"expected_text_sha256"`
	RetrievalQuery        string `json:"retrieval_query"`
	Privacy               string `json:"privacy"`
}

type treeEntry struct {
	Path string
	Mode string
}

func Check(ctx context.Context, root string, options Options) Report {
	report := Report{Issues: []Issue{}, HistoryChecked: options.IncludeHistory}
	root, err := filepath.Abs(root)
	if err != nil {
		return addIssue(report, "repository_unavailable", "", "resolve repository root: "+err.Error())
	}
	entries, err := listEntries(ctx, root, options.IncludeUntracked)
	if err != nil {
		return addIssue(report, "repository_unavailable", "", err.Error())
	}
	entryByPath := make(map[string]treeEntry, len(entries))
	dataByPath := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		entryByPath[entry.Path] = entry
		data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if readErr != nil {
			report = addIssue(report, "tracked_file_unreadable", entry.Path, readErr.Error())
			continue
		}
		dataByPath[entry.Path] = data
		report.FilesChecked++
		if entry.Mode == "120000" {
			report = addIssue(report, "tracked_symlink", entry.Path, "public release trees must not contain symbolic links")
		}
		if code, message := blockedPath(entry.Path); code != "" {
			report = addIssue(report, code, entry.Path, message)
		}
		if entry.Path != approvedFixturePath() {
			for _, pattern := range blockedContentPatterns(entry.Path) {
				if bytes.Contains(data, []byte(pattern.Value)) {
					report = addIssue(report, pattern.Code, entry.Path, pattern.Message)
				}
			}
		}
	}
	report = verifyFixture(report, entryByPath, dataByPath)
	if options.IncludeHistory {
		report = checkHistory(ctx, root, report)
	}
	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Path != report.Issues[j].Path {
			return report.Issues[i].Path < report.Issues[j].Path
		}
		return report.Issues[i].Code < report.Issues[j].Code
	})
	return report
}

func listEntries(ctx context.Context, root string, includeUntracked bool) ([]treeEntry, error) {
	output, err := gitOutput(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked public files: %w", err)
	}
	entries := map[string]treeEntry{}
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		parts := bytes.SplitN(raw, []byte{'\t'}, 2)
		fields := bytes.Fields(parts[0])
		if len(parts) != 2 || len(fields) != 3 {
			return nil, errors.New("git returned an invalid tracked-file record")
		}
		path := filepath.ToSlash(string(parts[1]))
		entries[path] = treeEntry{Path: path, Mode: string(fields[0])}
	}
	if includeUntracked {
		output, err = gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, fmt.Errorf("list untracked public files: %w", err)
		}
		for _, raw := range bytes.Split(output, []byte{0}) {
			if len(raw) == 0 {
				continue
			}
			path := filepath.ToSlash(string(raw))
			info, statErr := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
			if statErr != nil {
				return nil, fmt.Errorf("inspect untracked file %q: %w", path, statErr)
			}
			mode := "100644"
			if info.Mode()&os.ModeSymlink != 0 {
				mode = "120000"
			}
			entries[path] = treeEntry{Path: path, Mode: mode}
		}
	}
	result := make([]treeEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func blockedPath(path string) (string, string) {
	lower := strings.ToLower(filepath.ToSlash(path))
	for _, prefix := range []string{".codex/", "sessions/", "evidence/", "backups/", "private-memory/", "data/evidence/", "data/backups/"} {
		if strings.HasPrefix(lower, prefix) {
			return "local_data_path", "local evidence, session, backup, or private-memory data cannot be tracked"
		}
	}
	base := strings.ToLower(filepath.Base(lower))
	if base == ".env" || strings.HasPrefix(base, ".env.") && base != ".env.example" {
		return "secret_file", "environment files cannot be tracked"
	}
	for _, suffix := range []string{".db", ".sqlite", ".sqlite3", ".age", ".pem", ".key", ".p12", ".pfx"} {
		if strings.HasSuffix(lower, suffix) {
			return "sensitive_file", "database, encrypted backup, or private-key files cannot be tracked"
		}
	}
	if strings.HasSuffix(lower, ".jsonl") && !strings.EqualFold(lower, approvedFixturePath()) {
		return "unapproved_jsonl", "only the hash-pinned synthetic quickstart rollout may be tracked as JSONL"
	}
	return "", ""
}

type contentPattern struct {
	Code    string
	Value   string
	Message string
}

func blockedContentPatterns(path string) []contentPattern {
	patterns := []contentPattern{
		{Code: "machine_path", Value: "D:" + `\Codex\agent-memory-system`, Message: "repository contains a machine-specific project path"},
		{Code: "machine_path", Value: "D:" + "/Codex/agent-memory-system", Message: "repository contains a machine-specific project path"},
		{Code: "user_path", Value: "C:" + `\Users\` + "luo" + "song03", Message: "repository contains a user-specific local path"},
		{Code: "user_path", Value: "C:" + "/Users/" + "luo" + "song03", Message: "repository contains a user-specific local path"},
		{Code: "task_trace", Value: "019f" + "cba1", Message: "repository contains a local task identifier"},
		{Code: "process_trace", Value: "application-control-" + "blocked test binaries", Message: "repository contains internal execution-process wording"},
		{Code: "process_trace", Value: "final " + "adversarial review", Message: "repository contains internal review-process wording"},
		{Code: "authorship_trace", Value: "generated by " + "Codex", Message: "repository contains machine-authorship wording"},
	}
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return patterns
	}
	return append(patterns,
		contentPattern{Code: "raw_transcript", Value: `"type":"session_` + `meta"`, Message: "repository contains a raw transcript signature"},
		contentPattern{Code: "raw_transcript", Value: `"type": "session_` + `meta"`, Message: "repository contains a raw transcript signature"},
		contentPattern{Code: "raw_transcript", Value: `"type":"turn_` + `context"`, Message: "repository contains a raw transcript signature"},
		contentPattern{Code: "raw_transcript", Value: `"type":"response_` + `item"`, Message: "repository contains a raw transcript signature"},
	)
}

func approvedFixturePath() string {
	return "examples/quickstart/rollout-2026-08-11T00-00-00-11111111-1111-1111-1111-111111111111.jsonl"
}

func verifyFixture(report Report, entries map[string]treeEntry, data map[string][]byte) Report {
	manifestData, ok := data[quickstartManifestPath]
	if !ok {
		return addIssue(report, "fixture_manifest_missing", quickstartManifestPath, "the synthetic quickstart manifest is required")
	}
	var manifest fixtureManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return addIssue(report, "fixture_manifest_invalid", quickstartManifestPath, err.Error())
	}
	if manifest.SchemaVersion != "quickstart-fixture/v1alpha1" || manifest.Privacy != "synthetic_public" ||
		manifest.ExpectedReviewReady != 1 || manifest.ExpectedCandidateID == "" ||
		manifest.ExpectedCandidateText == "" || manifest.RetrievalQuery == "" ||
		!validSHA256(manifest.RolloutSHA256) || !validSHA256(manifest.ExpectedTextSHA256) {
		return addIssue(report, "fixture_manifest_invalid", quickstartManifestPath, "manifest fields do not satisfy the frozen public fixture contract")
	}
	fixturePath := filepath.ToSlash(filepath.Join("examples/quickstart", manifest.RolloutPath))
	if fixturePath != approvedFixturePath() || strings.Contains(manifest.RolloutPath, "..") || filepath.IsAbs(manifest.RolloutPath) {
		return addIssue(report, "fixture_path_invalid", quickstartManifestPath, "manifest rollout_path is not the approved synthetic fixture")
	}
	if _, ok := entries[fixturePath]; !ok {
		return addIssue(report, "fixture_missing", fixturePath, "the manifest-bound synthetic rollout is missing")
	}
	fixtureData, ok := data[fixturePath]
	if !ok {
		return report
	}
	digest := sha256.Sum256(fixtureData)
	report.QuickstartSHA256 = hex.EncodeToString(digest[:])
	if report.QuickstartSHA256 != manifest.RolloutSHA256 {
		report = addIssue(report, "fixture_hash_mismatch", fixturePath, "synthetic rollout bytes do not match the public manifest")
	}
	return report
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func checkHistory(ctx context.Context, root string, report Report) Report {
	commitsRaw, err := gitOutput(ctx, root, "rev-list", "--all")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "list reachable history: "+err.Error())
	}
	commits := strings.Fields(string(commitsRaw))
	if len(commits) == 0 {
		return addIssue(report, "history_unavailable", "", "repository has no reachable commits")
	}
	pathsRaw, err := gitOutput(ctx, root, "log", "--all", "--name-only", "--format=")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "list historical paths: "+err.Error())
	}
	seenPaths := map[string]struct{}{}
	for _, line := range strings.Split(strings.ReplaceAll(string(pathsRaw), "\r\n", "\n"), "\n") {
		path := filepath.ToSlash(strings.TrimSpace(line))
		if path == "" {
			continue
		}
		if _, seen := seenPaths[path]; seen {
			continue
		}
		seenPaths[path] = struct{}{}
		if code, message := blockedPath(path); code != "" {
			report = addIssue(report, "historical_"+code, path, message+" in reachable history")
		}
	}
	patternFile, err := os.CreateTemp("", "agentmem-public-patterns-*.txt")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "create history pattern file: "+err.Error())
	}
	patternPath := patternFile.Name()
	defer os.Remove(patternPath)
	writer := bufio.NewWriter(patternFile)
	for _, pattern := range blockedContentPatterns("history.md") {
		if _, err := writer.WriteString(pattern.Value + "\n"); err != nil {
			_ = patternFile.Close()
			return addIssue(report, "history_unavailable", "", "write history pattern file: "+err.Error())
		}
	}
	if err := writer.Flush(); err != nil {
		_ = patternFile.Close()
		return addIssue(report, "history_unavailable", "", "flush history pattern file: "+err.Error())
	}
	if err := patternFile.Close(); err != nil {
		return addIssue(report, "history_unavailable", "", "close history pattern file: "+err.Error())
	}
	seenMatches := map[string]struct{}{}
	for start := 0; start < len(commits); start += 40 {
		end := start + 40
		if end > len(commits) {
			end = len(commits)
		}
		args := []string{"grep", "-I", "-n", "-F", "-f", patternPath}
		args = append(args, commits[start:end]...)
		output, grepErr := gitOutputAllowNoMatch(ctx, root, args...)
		if grepErr != nil {
			return addIssue(report, "history_unavailable", "", "scan reachable history: "+grepErr.Error())
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			first := strings.IndexByte(line, ':')
			second := -1
			if first >= 0 {
				second = strings.IndexByte(line[first+1:], ':')
			}
			if first < 0 || second < 0 {
				continue
			}
			second += first + 1
			commit, path := line[:first], filepath.ToSlash(line[first+1:second])
			if path == approvedFixturePath() || strings.EqualFold(filepath.Ext(path), ".go") {
				continue
			}
			key := commit + ":" + path
			if _, seen := seenMatches[key]; seen {
				continue
			}
			seenMatches[key] = struct{}{}
			short := commit
			if len(short) > 12 {
				short = short[:12]
			}
			report = addIssue(report, "historical_sensitive_content", short+":"+path, "reachable history contains a blocked machine, task, authorship, process, or raw-transcript signature")
			if len(seenMatches) >= 100 {
				return addIssue(report, "history_findings_truncated", "", "reachable history has more than 100 blocked content matches")
			}
		}
	}
	return report
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
	}
	return nil, err
}

func gitOutputAllowNoMatch(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
	}
	return nil, err
}

func addIssue(report Report, code, path, message string) Report {
	report.Issues = append(report.Issues, Issue{Code: code, Path: path, Message: message})
	return report
}
