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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const (
	quickstartManifestPath = "examples/quickstart/manifest.json"
	publicAllowlistPath    = "examples/public-tree-allowlist.json"
)

var (
	rawTranscriptPattern   = regexp.MustCompile(`(?i)["']?type["']?\s*:\s*["'](?:session_meta|turn_context|response_item|event_msg)["']`)
	plainTranscriptPattern = regexp.MustCompile(`(?im)^(?:user|assistant|tool|reasoning)\s*:\s*\S`)
	taskIdentifierPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	processTracePattern    = regexp.MustCompile(`(?i)\b(?:(?:adversarial|maintainer|final)[ -]+review|application[ -]+control[ -]+blocked|(?:generated|written|created)[ -]+by[ -]+(?:an?[ -]+)?(?:ai|agent|language[ -]+model))\b`)
)

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

type allowlistManifest struct {
	SchemaVersion    string           `json:"schema_version"`
	PublicIdentities []string         `json:"public_identities"`
	Entries          []allowlistEntry `json:"entries"`
	Privacy          string           `json:"privacy"`
}

type allowlistEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Reason string `json:"reason"`
}

type exactAllowlist struct {
	Entries          map[string]allowlistEntry
	PublicIdentities map[string]struct{}
}

type treeEntry struct {
	Path string
	Mode string
}

type historyObject struct {
	SHA256 string
	Path   string
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
	}
	allowlist, allowErr := loadAllowlist(dataByPath[publicAllowlistPath])
	if allowErr != nil {
		report = addIssue(report, "public_allowlist_invalid", publicAllowlistPath, allowErr.Error())
		allowlist = exactAllowlist{Entries: map[string]allowlistEntry{}, PublicIdentities: map[string]struct{}{}}
	}
	for _, entry := range entries {
		data, ok := dataByPath[entry.Path]
		if !ok {
			continue
		}
		if entry.Mode == "120000" {
			report = addIssue(report, "tracked_symlink", entry.Path, "public release trees must not contain symbolic links")
		}
		if code, message := blockedPath(entry.Path); code != "" {
			report = addIssue(report, code, entry.Path, message)
		}
		report = scanBlob(report, entry.Path, data, allowlist, false)
	}
	report = verifyFixture(report, entryByPath, dataByPath)
	if options.IncludeHistory {
		report = checkHistory(ctx, root, report, allowlist)
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
	components := strings.Split(strings.Trim(lower, "/"), "/")
	for _, component := range components[:max(0, len(components)-1)] {
		switch component {
		case ".codex", "sessions", "evidence", "backups", "private-memory":
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

func loadAllowlist(data []byte) (exactAllowlist, error) {
	if len(data) == 0 {
		return exactAllowlist{}, errors.New("the exact synthetic-content allowlist is required")
	}
	var manifest allowlistManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return exactAllowlist{}, err
	}
	if manifest.SchemaVersion != "public-tree-allowlist/v1alpha1" || manifest.Privacy != "synthetic_public" {
		return exactAllowlist{}, errors.New("allowlist metadata is invalid")
	}
	result := exactAllowlist{Entries: map[string]allowlistEntry{}, PublicIdentities: map[string]struct{}{}}
	previousIdentity := ""
	for _, identity := range manifest.PublicIdentities {
		identity = strings.ToLower(strings.TrimSpace(identity))
		if identity == "" || !strings.Contains(identity, "@") || identity <= previousIdentity {
			return exactAllowlist{}, errors.New("public identities must be unique, sorted email addresses")
		}
		result.PublicIdentities[identity] = struct{}{}
		previousIdentity = identity
	}
	previous := ""
	for _, entry := range manifest.Entries {
		entry.Path = filepath.ToSlash(entry.Path)
		key := entry.Path + "\x00" + entry.SHA256
		if entry.Path == "" || entry.Path == publicAllowlistPath || strings.TrimSpace(entry.Reason) == "" ||
			!validSHA256(entry.SHA256) || key <= previous {
			return exactAllowlist{}, errors.New("allowlist entries must be unique, sorted, exact path-and-hash records with a reason")
		}
		if code, _ := blockedPath(entry.Path); code != "" {
			return exactAllowlist{}, errors.New("allowlist cannot authorize a blocked data path")
		}
		result.Entries[key] = entry
		previous = key
	}
	return result, nil
}

func scanBlob(report Report, path string, data []byte, allowlist exactAllowlist, historical bool) Report {
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	if _, allowed := allowlist.Entries[path+"\x00"+sha]; allowed {
		return report
	}
	prefix := ""
	if historical {
		prefix = "historical_"
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return addIssue(report, prefix+"unapproved_binary", path, "data-bearing binary content requires an exact path-and-hash allowlist entry")
	}
	text := string(data)
	seen := map[string]struct{}{}
	add := func(code, message string) {
		if _, exists := seen[code]; exists {
			return
		}
		seen[code] = struct{}{}
		report = addIssue(report, prefix+code, path, message)
	}
	if rawTranscriptPattern.MatchString(text) || plainTranscriptPattern.MatchString(text) {
		add("raw_transcript", "repository contains a raw transcript signature")
	}
	if taskIdentifierPattern.MatchString(text) {
		add("task_trace", "repository contains a task-style identifier")
	}
	if processTracePattern.MatchString(text) {
		add("process_trace", "repository contains internal execution, review, or machine-authorship wording")
	}
	for _, finding := range secretscan.Scan(text).Findings {
		match := text[finding.StartByte:finding.EndByte]
		if finding.Category == secretscan.CategoryPersonal && publicEmail(match, allowlist) {
			continue
		}
		switch finding.Category {
		case secretscan.CategoryCredential, secretscan.CategoryPrivateKey:
			add("secret_material", "repository contains credential or private-key material")
		case secretscan.CategoryPath:
			add("machine_path", "repository contains a machine-specific path")
		case secretscan.CategoryPersonal:
			add("personal_identifier", "repository contains a personal identifier")
		}
	}
	return report
}

func publicEmail(value string, allowlist exactAllowlist) bool {
	lower := strings.ToLower(value)
	if _, ok := allowlist.PublicIdentities[lower]; ok {
		return true
	}
	return strings.HasSuffix(lower, "@example.com") || strings.HasSuffix(lower, "@example.org") ||
		strings.HasSuffix(lower, "@example.net") || strings.HasSuffix(lower, "@users.noreply.github.com")
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

func checkHistory(ctx context.Context, root string, report Report, allowlist exactAllowlist) Report {
	objectsRaw, err := gitOutput(ctx, root, "rev-list", "--objects", "--all")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "list reachable objects: "+err.Error())
	}
	objects := []historyObject{}
	seenObject := map[string]struct{}{}
	for _, line := range strings.Split(strings.ReplaceAll(string(objectsRaw), "\r\n", "\n"), "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(fields) == 0 || fields[0] == "" || len(fields[0]) != 40 {
			continue
		}
		path := ""
		if len(fields) == 2 {
			path = filepath.ToSlash(fields[1])
		}
		key := fields[0] + "\x00" + path
		if _, exists := seenObject[key]; exists {
			continue
		}
		seenObject[key] = struct{}{}
		objects = append(objects, historyObject{SHA256: fields[0], Path: path})
	}
	if len(objects) == 0 {
		return addIssue(report, "history_unavailable", "", "repository has no reachable objects")
	}
	types, err := objectTypes(ctx, root, objects)
	if err != nil {
		return addIssue(report, "history_unavailable", "", err.Error())
	}
	blobs, err := readBlobs(ctx, root, objects, types)
	if err != nil {
		return addIssue(report, "history_unavailable", "", err.Error())
	}
	seenFinding := map[string]struct{}{}
	for _, object := range objects {
		if types[object.SHA256] != "blob" || object.Path == "" {
			continue
		}
		if code, message := blockedPath(object.Path); code != "" {
			key := code + "\x00" + object.Path
			if _, exists := seenFinding[key]; !exists {
				report = addIssue(report, "historical_"+code, object.Path, message+" in reachable history")
				seenFinding[key] = struct{}{}
			}
		}
		before := len(report.Issues)
		report = scanBlob(report, object.Path, blobs[object.SHA256], allowlist, true)
		for index := before; index < len(report.Issues); index++ {
			report.Issues[index].Path = object.SHA256[:12] + ":" + object.Path
		}
		if len(report.Issues) >= 200 {
			return addIssue(report, "history_findings_truncated", "", "reachable history has too many blocked findings")
		}
	}
	metadata, err := gitOutput(ctx, root, "log", "--all", "--format=%H%x1f%an%x1f%ae%x1f%B%x1e")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "read commit metadata: "+err.Error())
	}
	for _, raw := range bytes.Split(metadata, []byte{0x1e}) {
		fields := bytes.SplitN(raw, []byte{0x1f}, 4)
		if len(fields) != 4 {
			continue
		}
		commit := strings.TrimSpace(string(fields[0]))
		before := len(report.Issues)
		report = scanBlob(report, "commit-metadata", bytes.Join(fields[1:], []byte("\n")), allowlist, true)
		if len(commit) >= 12 {
			for index := before; index < len(report.Issues); index++ {
				report.Issues[index].Path = commit[:12] + ":commit-metadata"
			}
		}
	}
	refs, err := gitOutput(ctx, root, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return addIssue(report, "history_unavailable", "", "read public refs: "+err.Error())
	}
	report = scanBlob(report, "public-refs", refs, exactAllowlist{}, true)
	return report
}

func objectTypes(ctx context.Context, root string, objects []historyObject) (map[string]string, error) {
	unique := []string{}
	seen := map[string]struct{}{}
	for _, object := range objects {
		if _, exists := seen[object.SHA256]; exists {
			continue
		}
		seen[object.SHA256] = struct{}{}
		unique = append(unique, object.SHA256)
	}
	input := []byte(strings.Join(unique, "\n") + "\n")
	output, err := gitInputOutput(ctx, root, input, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, fmt.Errorf("inspect reachable object types: %w", err)
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("git returned an invalid object-type record")
		}
		result[fields[0]] = fields[1]
	}
	return result, nil
}

func readBlobs(ctx context.Context, root string, objects []historyObject, types map[string]string) (map[string][]byte, error) {
	unique := []string{}
	seen := map[string]struct{}{}
	for _, object := range objects {
		if types[object.SHA256] != "blob" {
			continue
		}
		if _, exists := seen[object.SHA256]; exists {
			continue
		}
		seen[object.SHA256] = struct{}{}
		unique = append(unique, object.SHA256)
	}
	input := []byte(strings.Join(unique, "\n") + "\n")
	output, err := gitInputOutput(ctx, root, input, "cat-file", "--batch")
	if err != nil {
		return nil, fmt.Errorf("read reachable blobs: %w", err)
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	result := map[string][]byte{}
	for _, expected := range unique {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, errors.New("git returned a truncated blob header")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != expected || fields[1] != "blob" {
			return nil, errors.New("git returned an invalid blob header")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, errors.New("git returned an invalid blob size")
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, errors.New("git returned truncated blob content")
		}
		separator, err := reader.ReadByte()
		if err != nil || separator != '\n' {
			return nil, errors.New("git returned an invalid blob separator")
		}
		result[expected] = data
	}
	return result, nil
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	return gitInputOutput(ctx, root, nil, args...)
}

func gitInputOutput(ctx context.Context, root string, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
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

func addIssue(report Report, code, path, message string) Report {
	report.Issues = append(report.Issues, Issue{Code: code, Path: path, Message: message})
	return report
}
