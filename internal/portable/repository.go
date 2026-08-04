package portable

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	repositoryManifestName   = "portable-memory-repository.yaml"
	repositoryManifestData   = "schema_version: \"portable-memory-repository/v1alpha1\"\nlayout: \"immutable-markdown-revisions\"\n"
	repositoryReadmeData     = "# Personal agent memory\n\nThis private repository contains reviewed, redacted memory revisions only. Raw task evidence, transcripts, tool output, local proof records, indexes, and credentials do not belong here.\n\nEach file under `memories/` is an immutable revision. Current state is derived from parent links; divergent children are reported as conflicts rather than resolved by last-write-wins.\n"
	repositoryAttributesData = "*.md text eol=lf\n*.yaml text eol=lf\n"
	repositoryIgnoreData     = ".agentmem/\n.DS_Store\nThumbs.db\n"
)

type repositoryState struct {
	revisions map[string]Revision
	heads     map[string]Revision
}

func InitRepository(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("portable repository root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create portable repository root: %w", err)
	}
	files := map[string][]byte{
		repositoryManifestName: []byte(repositoryManifestData),
		"README.md":            []byte(repositoryReadmeData),
		".gitattributes":       []byte(repositoryAttributesData),
		".gitignore":           []byte(repositoryIgnoreData),
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		data := files[relative]
		existing, err := os.ReadFile(path)
		if err == nil {
			if !bytes.Equal(existing, data) {
				return fmt.Errorf("portable repository file %s already exists with different content", relative)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read portable repository file %s: %w", relative, err)
		}
		if err := writeFileAtomic(path, data); err != nil {
			return err
		}
	}
	report := VerifyRepository(root)
	if len(report.Issues) != 0 {
		return fmt.Errorf("portable repository initialization failed verification: %s", report.Issues[0].Message)
	}
	return nil
}

func VerifyRepository(root string) VerificationReport {
	report, _ := loadRepository(root)
	return report
}

func loadRepository(root string) (VerificationReport, *repositoryState) {
	report := VerificationReport{
		SchemaVersion: VerificationSchemaVersion, Issues: []VerificationIssue{}, Privacy: PortablePrivacy,
	}
	state := &repositoryState{revisions: map[string]Revision{}, heads: map[string]Revision{}}
	if strings.TrimSpace(root) == "" {
		addIssue(&report, VerificationIssue{Code: "repository_root_invalid", Message: "portable repository root is required"})
		return finalizeReport(report), state
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		addIssue(&report, VerificationIssue{Code: "repository_root_invalid", Message: "portable repository root is unavailable"})
		return finalizeReport(report), state
	}
	expected := map[string][]byte{
		repositoryManifestName: []byte(repositoryManifestData),
		"README.md":            []byte(repositoryReadmeData),
		".gitattributes":       []byte(repositoryAttributesData),
		".gitignore":           []byte(repositoryIgnoreData),
	}
	for relative, wanted := range expected {
		path := filepath.Join(root, relative)
		entry, statErr := os.Lstat(path)
		if statErr != nil || !entry.Mode().IsRegular() {
			addIssue(&report, VerificationIssue{
				Code: "repository_file_missing", Message: fmt.Sprintf("required repository file %s is unavailable", relative),
			})
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(data, wanted) {
			addIssue(&report, VerificationIssue{
				Code: "repository_file_changed", Message: fmt.Sprintf("required repository file %s is not canonical", relative),
			})
		}
		report.FilesChecked++
	}
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			addIssue(&report, VerificationIssue{Code: "repository_read_error", Message: "portable repository contains an unreadable path"})
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			addIssue(&report, VerificationIssue{Code: "repository_path_invalid", Message: "portable repository contains an invalid path"})
			return nil
		}
		if relative == "." {
			return nil
		}
		parts := splitPath(relative)
		if entry.IsDir() {
			if len(parts) == 1 && (parts[0] == ".git" || parts[0] == ".agentmem") {
				return filepath.SkipDir
			}
			if parts[0] == "memories" {
				return nil
			}
			if len(parts) == 1 {
				addIssue(&report, VerificationIssue{
					Code: "unexpected_path", Message: "portable repository contains an unsupported directory",
				})
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			addIssue(&report, VerificationIssue{Code: "unexpected_path", Message: "portable repository contains a non-regular file"})
			return nil
		}
		if len(parts) == 1 {
			if parts[0] == ".git" {
				return nil
			}
			if _, known := expected[parts[0]]; !known {
				addIssue(&report, VerificationIssue{Code: "unexpected_path", Message: "portable repository contains an unsupported root file"})
			}
			return nil
		}
		memoryID, revisionID, validPath := identityFromRevisionPath(parts)
		if !validPath {
			addIssue(&report, VerificationIssue{Code: "unexpected_path", Message: "portable repository contains an invalid memory revision path"})
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			addIssue(&report, VerificationIssue{Code: "repository_read_error", MemoryID: memoryID, RevisionID: revisionID, Message: "portable memory revision is unreadable"})
			return nil
		}
		report.FilesChecked++
		revision, err := ParseRevision(data)
		if err != nil {
			addIssue(&report, VerificationIssue{Code: "invalid_revision", MemoryID: memoryID, RevisionID: revisionID, Message: "portable memory revision failed canonical validation"})
			return nil
		}
		if revision.MemoryID != memoryID || revision.RevisionID != revisionID {
			addIssue(&report, VerificationIssue{Code: "revision_path_mismatch", MemoryID: memoryID, RevisionID: revisionID, Message: "portable revision identity does not match its path"})
			return nil
		}
		if _, duplicate := state.revisions[revision.RevisionID]; duplicate {
			addIssue(&report, VerificationIssue{Code: "duplicate_revision", MemoryID: memoryID, RevisionID: revisionID, Message: "portable revision identity appears more than once"})
			return nil
		}
		state.revisions[revision.RevisionID] = revision
		report.RevisionsChecked++
		return nil
	})
	if walkErr != nil {
		addIssue(&report, VerificationIssue{Code: "repository_read_error", Message: "portable repository traversal failed"})
	}
	validateRepositoryState(&report, state)
	return finalizeReport(report), state
}

func validateRepositoryState(report *VerificationReport, state *repositoryState) {
	byMemory := map[string][]Revision{}
	children := map[string][]string{}
	for _, revision := range state.revisions {
		byMemory[revision.MemoryID] = append(byMemory[revision.MemoryID], revision)
		if revision.ParentRevisionID != "" {
			children[revision.ParentRevisionID] = append(children[revision.ParentRevisionID], revision.RevisionID)
		}
	}
	memoryIDs := make([]string, 0, len(byMemory))
	for memoryID := range byMemory {
		memoryIDs = append(memoryIDs, memoryID)
	}
	sort.Strings(memoryIDs)
	report.MemoriesChecked = len(memoryIDs)
	for _, memoryID := range memoryIDs {
		revisions := byMemory[memoryID]
		sort.Slice(revisions, func(left, right int) bool { return revisions[left].RevisionID < revisions[right].RevisionID })
		roots := []string{}
		heads := []string{}
		for _, revision := range revisions {
			if revision.ParentRevisionID == "" {
				roots = append(roots, revision.RevisionID)
			} else {
				parent, exists := state.revisions[revision.ParentRevisionID]
				if !exists {
					addIssue(report, VerificationIssue{Code: "missing_parent", MemoryID: memoryID, RevisionID: revision.RevisionID, RelatedRevisionIDs: []string{revision.ParentRevisionID}, Message: "portable revision parent is missing"})
				} else if parent.MemoryID != memoryID || parent.Status != StatusActive ||
					parent.ScopeKind != revision.ScopeKind || parent.ScopeValue != revision.ScopeValue {
					addIssue(report, VerificationIssue{Code: "invalid_parent", MemoryID: memoryID, RevisionID: revision.RevisionID, RelatedRevisionIDs: []string{revision.ParentRevisionID}, Message: "portable revision parent is incompatible"})
				}
			}
			if len(children[revision.RevisionID]) == 0 {
				heads = append(heads, revision.RevisionID)
			}
			if len(children[revision.RevisionID]) > 1 {
				related := append([]string(nil), children[revision.RevisionID]...)
				sort.Strings(related)
				addIssue(report, VerificationIssue{Code: "revision_fork", MemoryID: memoryID, RevisionID: revision.RevisionID, RelatedRevisionIDs: related, Message: "portable memory has divergent children and requires explicit resolution"})
			}
		}
		if len(roots) != 1 {
			addIssue(report, VerificationIssue{Code: "root_conflict", MemoryID: memoryID, RelatedRevisionIDs: roots, Message: "portable memory does not have exactly one root revision"})
		}
		if hasCycle(revisions, state.revisions) {
			addIssue(report, VerificationIssue{Code: "revision_cycle", MemoryID: memoryID, Message: "portable memory revision graph contains a cycle"})
		}
		if len(heads) != 1 {
			sort.Strings(heads)
			addIssue(report, VerificationIssue{Code: "head_conflict", MemoryID: memoryID, RelatedRevisionIDs: heads, Message: "portable memory does not have exactly one current head"})
			continue
		}
		head := state.revisions[heads[0]]
		state.heads[memoryID] = head
		if head.Status == StatusActive {
			report.ActiveMemories++
		} else {
			report.RevokedMemories++
		}
	}
	validateSemanticHeads(report, state.heads)
}

func hasCycle(revisions []Revision, all map[string]Revision) bool {
	for _, revision := range revisions {
		seen := map[string]struct{}{}
		current := revision
		for current.ParentRevisionID != "" {
			if _, exists := seen[current.RevisionID]; exists {
				return true
			}
			seen[current.RevisionID] = struct{}{}
			parent, exists := all[current.ParentRevisionID]
			if !exists || parent.MemoryID != revision.MemoryID {
				break
			}
			current = parent
		}
	}
	return false
}

func identityFromRevisionPath(parts []string) (string, string, bool) {
	if len(parts) != 4 || parts[0] != "memories" || len(parts[1]) != 2 ||
		!validPrefixedHash(parts[2], "memory-") || filepath.Ext(parts[3]) != ".md" {
		return "", "", false
	}
	revisionID := strings.TrimSuffix(parts[3], ".md")
	if !validPrefixedHash(revisionID, "portable-revision-") ||
		parts[1] != strings.TrimPrefix(parts[2], "memory-")[:2] {
		return "", "", false
	}
	return parts[2], revisionID, true
}

func revisionRelativePath(revision Revision) string {
	hash := strings.TrimPrefix(revision.MemoryID, "memory-")
	return filepath.Join("memories", hash[:2], revision.MemoryID, revision.RevisionID+".md")
}

func splitPath(path string) []string {
	return strings.Split(filepath.Clean(path), string(filepath.Separator))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create portable repository directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".agentmem-*.tmp")
	if err != nil {
		return fmt.Errorf("create portable repository temporary file: %w", err)
	}
	temporary := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(temporary)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set portable repository file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write portable repository file: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync portable repository file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("close portable repository file: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(temporary)
		return errors.New("portable repository destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(temporary)
		return fmt.Errorf("inspect portable repository destination: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish portable repository file: %w", err)
	}
	return nil
}

func addIssue(report *VerificationReport, issue VerificationIssue) {
	issue.RelatedMemoryIDs = sortedUnique(issue.RelatedMemoryIDs)
	issue.RelatedRevisionIDs = sortedUnique(issue.RelatedRevisionIDs)
	report.Issues = append(report.Issues, issue)
}

func finalizeReport(report VerificationReport) VerificationReport {
	sort.Slice(report.Issues, func(left, right int) bool {
		a, b := report.Issues[left], report.Issues[right]
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.MemoryID != b.MemoryID {
			return a.MemoryID < b.MemoryID
		}
		if a.RevisionID != b.RevisionID {
			return a.RevisionID < b.RevisionID
		}
		return a.Message < b.Message
	})
	return report
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	result := copy[:0]
	for _, value := range copy {
		if value != "" && (len(result) == 0 || result[len(result)-1] != value) {
			result = append(result, value)
		}
	}
	return result
}
