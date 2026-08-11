package sourcerecovery

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type sourceCandidate struct {
	relative string
	sha256   string
	bytes    int64
	threadID string
}

func PlanLegacyCodex(
	store *ledger.Store, corpusID, sourceRoot, outputPath string, now time.Time,
) (PlanResult, error) {
	result := PlanResult{
		SchemaVersion: PlanResultSchema, CorpusID: corpusID,
		Issues: []PlanIssue{}, Privacy: "local_only",
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if strings.TrimSpace(outputPath) == "" {
		return result, errors.New("output path is required")
	}
	verification := evaluation.VerifyCorpus(store, corpusID)
	if len(verification.Issues) != 0 {
		return result, errors.New("legacy corpus verification failed")
	}
	corpus, err := evaluation.LoadCorpusManifest(store, corpusID)
	if err != nil {
		return result, err
	}
	root, err := resolveSourceRoot(sourceRoot)
	if err != nil {
		return result, err
	}
	candidates, filesExamined, scanIssues, err := collectCodexCandidates(root)
	if err != nil {
		return result, err
	}
	result.FilesExamined = filesExamined
	if now.IsZero() {
		now = time.Now().UTC()
	}

	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CreatedAt: now.UTC(), Agent: ledger.AgentCodex,
		Entries: []Entry{}, Privacy: "local_only",
	}
	issueCounts := scanIssues
	for _, rollout := range corpus.Rollouts {
		if rollout.Status != "missing" {
			continue
		}
		result.Expected++
		threadID := fallbackThreadID(rollout.SessionIDs, rollout.SourcePathSHA256)
		matches := candidatesForSessions(candidates, rollout.SessionIDs)
		chosen, available, ambiguous := chooseCandidate(matches)
		entry := Entry{
			LogicalSourcePathSHA256: rollout.SourcePathSHA256, ThreadID: threadID,
		}
		switch {
		case available:
			entry.Status = StatusAvailable
			entry.ThreadID = chosen.threadID
			entry.RelativePath = chosen.relative
			entry.ExpectedContentSHA256 = chosen.sha256
			entry.ExpectedBytes = &chosen.bytes
			result.Available++
		case ambiguous:
			entry.Status = StatusMissing
			entry.Reason = "source_ambiguous_after_local_search"
			result.Missing++
			result.Ambiguous++
			issueCounts[entry.Reason]++
		default:
			entry.Status = StatusMissing
			entry.Reason = "source_not_found_after_local_search"
			if scanIssues["candidate_unreadable"] > 0 || scanIssues["candidate_identity_missing"] > 0 {
				entry.Reason = "source_not_found_search_incomplete"
			}
			result.Missing++
			issueCounts[entry.Reason]++
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	if len(manifest.Entries) == 0 {
		return result, errors.New("legacy corpus has no missing rollout references")
	}
	sort.Slice(manifest.Entries, func(left, right int) bool {
		return manifest.Entries[left].LogicalSourcePathSHA256 < manifest.Entries[right].LogicalSourcePathSHA256
	})
	manifest.RecoveryID, err = RecoveryID(manifest)
	if err != nil {
		return result, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, fmt.Errorf("encode source recovery manifest: %w", err)
	}
	data = append(data, '\n')
	digest := sha256.Sum256(data)
	result.RecoveryID = manifest.RecoveryID
	result.ManifestSHA256 = hex.EncodeToString(digest[:])
	for code, count := range issueCounts {
		result.Issues = append(result.Issues, PlanIssue{Code: code, Count: count})
	}
	sort.Slice(result.Issues, func(left, right int) bool { return result.Issues[left].Code < result.Issues[right].Code })
	if err := writeNewManifest(outputPath, data); err != nil {
		return result, err
	}
	return result, nil
}

func collectCodexCandidates(root string) (map[string][]sourceCandidate, int, map[string]int, error) {
	result := map[string][]sourceCandidate{}
	issues := map[string]int{}
	filesExamined := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		filesExamined++
		threadID, err := readCodexThreadID(path)
		if err != nil {
			issues["candidate_unreadable"]++
			return nil
		}
		if threadID == "" {
			issues["candidate_identity_missing"]++
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, size, err := hashRegularFile(path)
		if err != nil {
			issues["candidate_unreadable"]++
			return nil
		}
		result[threadID] = append(result[threadID], sourceCandidate{
			relative: filepath.ToSlash(relative), sha256: digest, bytes: size, threadID: threadID,
		})
		return nil
	})
	if err != nil {
		return nil, filesExamined, issues, fmt.Errorf("search recovered Codex sources: %w", err)
	}
	for threadID := range result {
		sort.Slice(result[threadID], func(left, right int) bool {
			return result[threadID][left].relative < result[threadID][right].relative
		})
	}
	return result, filesExamined, issues, nil
}

func readCodexThreadID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineNumber := 0; lineNumber < 128; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var envelope struct {
				Type    string `json:"type"`
				Payload struct {
					ID        string `json:"id"`
					SessionID string `json:"session_id"`
				} `json:"payload"`
			}
			if json.Unmarshal(bytes.TrimSpace(line), &envelope) == nil && envelope.Type == "session_meta" {
				if envelope.Payload.ID != "" {
					return envelope.Payload.ID, nil
				}
				if envelope.Payload.SessionID != "" {
					return envelope.Payload.SessionID, nil
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return "", nil
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return "", nil
}

func hashRegularFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("candidate is not a regular file")
		}
		return "", 0, err
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func candidatesForSessions(index map[string][]sourceCandidate, sessionIDs []string) []sourceCandidate {
	seen := map[string]struct{}{}
	var result []sourceCandidate
	for _, sessionID := range sessionIDs {
		for _, candidate := range index[sessionID] {
			if _, duplicate := seen[candidate.relative]; duplicate {
				continue
			}
			seen[candidate.relative] = struct{}{}
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].relative < result[right].relative })
	return result
}

func chooseCandidate(candidates []sourceCandidate) (sourceCandidate, bool, bool) {
	if len(candidates) == 0 {
		return sourceCandidate{}, false, false
	}
	first := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.sha256 != first.sha256 || candidate.bytes != first.bytes {
			return sourceCandidate{}, false, true
		}
	}
	return first, true, false
}

func fallbackThreadID(sessionIDs []string, sourcePathSHA256 string) string {
	if len(sessionIDs) > 0 && strings.TrimSpace(sessionIDs[0]) != "" {
		return sessionIDs[0]
	}
	return "unknown-" + sourcePathSHA256[:16]
}

func writeNewManifest(path string, data []byte) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve recovery manifest output: %w", err)
	}
	if err := ensureOutputOutsideGit(absolute); err != nil {
		return err
	}
	file, err := os.OpenFile(absolute, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create recovery manifest: %w", err)
	}
	failed := true
	defer func() {
		_ = file.Close()
		if failed {
			_ = os.Remove(absolute)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write recovery manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync recovery manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery manifest: %w", err)
	}
	failed = false
	return nil
}

func ensureOutputOutsideGit(path string) error {
	for current := filepath.Dir(filepath.Clean(path)); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil {
			return errors.New("source recovery manifest must be outside every Git worktree")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect recovery manifest ancestors: %w", err)
		}
		bare, err := looksLikeBareGit(current)
		if err != nil {
			return err
		}
		if bare {
			return errors.New("source recovery manifest must be outside every bare Git repository")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
