package portable

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
)

// ErrStorageRootsOverlap marks equal, nested, or link-aliased storage roots.
var ErrStorageRootsOverlap = errors.New("portable repository and local evidence root must be physically separate")

type RepositoryLock struct {
	path string
}

func Export(store *ledger.Store, repositoryRoot string, options ExportOptions) (ExportResult, error) {
	result := ExportResult{
		SchemaVersion: ExportResultSchemaVersion, FilesWritten: []string{}, Privacy: PortablePrivacy,
	}
	if store == nil {
		return result, errors.New("store is required")
	}
	if err := ensureSeparateRoots(store.Root(), repositoryRoot); err != nil {
		return result, err
	}
	preflight, _ := loadRepository(repositoryRoot)
	if len(preflight.Issues) != 0 {
		return result, fmt.Errorf("portable repository verification failed: %s", preflight.Issues[0].Message)
	}
	lock, err := AcquireRepositoryLock(repositoryRoot)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	report, existing := loadRepository(repositoryRoot)
	if len(report.Issues) != 0 {
		return result, fmt.Errorf("portable repository verification failed: %s", report.Issues[0].Message)
	}
	histories, err := promotion.ListHistories(store)
	if err != nil {
		return result, err
	}
	selected, err := selectHistories(histories, options.MemoryIDs)
	if err != nil {
		return result, err
	}
	result.MemoriesSelected = len(selected)
	projected := []Revision{}
	for _, history := range selected {
		if len(history.Revisions) == 0 {
			return result, fmt.Errorf("promotion history %s is empty", history.MemoryID)
		}
		current := history.Revisions[len(history.Revisions)-1]
		if current.Status == promotion.StatusActive {
			status, err := promotion.GetStatus(store, history.MemoryID)
			if err != nil {
				return result, err
			}
			if status.CurrentRevisionID != current.RevisionID || !status.ExportEligible {
				return result, fmt.Errorf("active memory %s is not eligible for portable export", history.MemoryID)
			}
		}
		revisions, err := projectHistory(history)
		if err != nil {
			return result, err
		}
		projected = append(projected, revisions...)
	}
	result.RevisionsProjected = len(projected)
	combined := make(map[string]Revision, len(existing.revisions)+len(projected))
	for revisionID, revision := range existing.revisions {
		combined[revisionID] = revision
	}
	planned := []Revision{}
	for _, revision := range projected {
		if current, exists := combined[revision.RevisionID]; exists {
			if !reflect.DeepEqual(current, revision) {
				return result, fmt.Errorf("portable revision %s already exists with different content", revision.RevisionID)
			}
			result.RevisionsUnchanged++
			continue
		}
		combined[revision.RevisionID] = revision
		planned = append(planned, revision)
	}
	combinedReport := VerificationReport{
		SchemaVersion: VerificationSchemaVersion, Issues: []VerificationIssue{}, Privacy: PortablePrivacy,
		RevisionsChecked: len(combined),
	}
	combinedState := &repositoryState{
		revisions: combined,
		heads:     map[string]Revision{},
		loadouts:  existing.loadouts,
	}
	validateRepositoryState(&combinedReport, combinedState)
	validateLoadoutState(&combinedReport, combinedState)
	combinedReport = finalizeReport(combinedReport)
	if len(combinedReport.Issues) != 0 {
		return result, fmt.Errorf("portable export would create a conflict: %s", combinedReport.Issues[0].Message)
	}
	for _, revision := range planned {
		data, err := RenderRevision(revision)
		if err != nil {
			return result, err
		}
		relative := revisionRelativePath(revision)
		path := filepath.Join(repositoryRoot, relative)
		if err := writeFileAtomic(path, data); err != nil {
			return result, err
		}
		result.FilesWritten = append(result.FilesWritten, filepath.ToSlash(relative))
		result.RevisionsWritten++
	}
	final := VerifyRepository(repositoryRoot)
	if len(final.Issues) != 0 {
		return result, fmt.Errorf("portable repository failed verification after export: %s", final.Issues[0].Message)
	}
	if err := lock.Release(); err != nil {
		return result, fmt.Errorf("portable export completed but lock cleanup failed: %w", err)
	}
	return result, nil
}

// ResolveSemanticKey maps a portable revision back to its verified local
// promotion source without adding private provenance to the portable format.
func ResolveSemanticKey(store *ledger.Store, memoryID, portableRevisionID string) (string, error) {
	_, semanticKey, err := ResolveLocalRevision(store, memoryID, portableRevisionID)
	return semanticKey, err
}

// ResolveLocalRevision reconstructs the exact portable projection backed by a
// verified local promotion revision and returns its private semantic key.
func ResolveLocalRevision(store *ledger.Store, memoryID, portableRevisionID string) (Revision, string, error) {
	if store == nil {
		return Revision{}, "", errors.New("store is required")
	}
	histories, err := promotion.ListHistories(store)
	if err != nil {
		return Revision{}, "", err
	}
	for _, history := range histories {
		if history.MemoryID != memoryID {
			continue
		}
		projected, err := projectHistory(history)
		if err != nil {
			return Revision{}, "", err
		}
		for index, revision := range projected {
			if revision.RevisionID != portableRevisionID {
				continue
			}
			if index >= len(history.Revisions) || history.Revisions[index].Source == nil ||
				history.Revisions[index].Source.SemanticKeySHA256 == "" {
				return Revision{}, "", errors.New("portable revision has no local semantic source")
			}
			return revision, history.Revisions[index].Source.SemanticKeySHA256, nil
		}
		return Revision{}, "", errors.New("portable revision is absent from its local promotion history")
	}
	return Revision{}, "", errors.New("portable memory is absent from local promotion history")
}

// LoadLocalPopulation reconstructs the complete portable projection of every
// verified local promotion history. Active heads must remain export-eligible.
func LoadLocalPopulation(store *ledger.Store) (map[string]Revision, []Revision, error) {
	if store == nil {
		return nil, nil, errors.New("store is required")
	}
	histories, err := promotion.ListHistories(store)
	if err != nil {
		return nil, nil, err
	}
	revisions := map[string]Revision{}
	active := []Revision{}
	for _, history := range histories {
		if len(history.Revisions) == 0 {
			return nil, nil, fmt.Errorf("promotion history %s is empty", history.MemoryID)
		}
		projected, projectErr := projectHistory(history)
		if projectErr != nil {
			return nil, nil, projectErr
		}
		for _, revision := range projected {
			if _, duplicate := revisions[revision.RevisionID]; duplicate {
				return nil, nil, fmt.Errorf("portable revision %s is duplicated", revision.RevisionID)
			}
			revisions[revision.RevisionID] = revision
		}
		currentLocal := history.Revisions[len(history.Revisions)-1]
		currentPortable := projected[len(projected)-1]
		if currentLocal.Status != promotion.StatusActive {
			continue
		}
		status, statusErr := promotion.GetStatus(store, history.MemoryID)
		if statusErr != nil {
			return nil, nil, statusErr
		}
		if status.CurrentRevisionID != currentLocal.RevisionID || !status.ExportEligible {
			return nil, nil, fmt.Errorf("active memory %s is not eligible for portable export", history.MemoryID)
		}
		active = append(active, currentPortable)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].RevisionID < active[j].RevisionID })
	return revisions, active, nil
}

func projectHistory(history promotion.History) ([]Revision, error) {
	localToPortable := map[string]string{}
	result := make([]Revision, 0, len(history.Revisions))
	for _, local := range history.Revisions {
		parent := ""
		if local.ParentRevisionID != "" {
			var exists bool
			parent, exists = localToPortable[local.ParentRevisionID]
			if !exists {
				return nil, fmt.Errorf("promotion history %s has an unavailable parent", history.MemoryID)
			}
		}
		revision := Revision{
			MemoryID: history.MemoryID, ParentRevisionID: parent,
			Kind: local.Kind, ScopeKind: local.Scope.Kind, ScopeValue: local.Scope.Value,
			RequiresExplicitRuleChangeApproval: local.RequiresExplicitRuleChangeApproval,
		}
		switch local.Action {
		case promotion.ActionPromote:
			revision.Action, revision.Status = ActionPromote, StatusActive
		case promotion.ActionSupersede:
			revision.Action, revision.Status = ActionSupersede, StatusActive
		case promotion.ActionRevoke:
			revision.Action, revision.Status = ActionRevoke, StatusRevoked
		default:
			return nil, fmt.Errorf("promotion history %s contains an unsupported action", history.MemoryID)
		}
		if revision.Status == StatusActive {
			if local.Source == nil {
				return nil, fmt.Errorf("promotion history %s has an active revision without source proof", history.MemoryID)
			}
			revision.Text = local.Text
			revision.EvidenceBasis = append(revision.EvidenceBasis, local.Source.ReviewBasis...)
		}
		projected, err := finalizeRevision(revision)
		if err != nil {
			return nil, fmt.Errorf("project promotion revision %s: %w", local.RevisionID, err)
		}
		if projected.MemoryID != local.MemoryID {
			return nil, fmt.Errorf("promotion history %s changed memory identity during projection", history.MemoryID)
		}
		localToPortable[local.RevisionID] = projected.RevisionID
		result = append(result, projected)
	}
	return result, nil
}

func selectHistories(histories []promotion.History, requested []string) ([]promotion.History, error) {
	if len(requested) == 0 {
		return histories, nil
	}
	wanted := map[string]struct{}{}
	for _, memoryID := range requested {
		if !validPrefixedHash(memoryID, "memory-") {
			return nil, fmt.Errorf("invalid memory id %q", memoryID)
		}
		if _, duplicate := wanted[memoryID]; duplicate {
			return nil, fmt.Errorf("memory id %q was requested more than once", memoryID)
		}
		wanted[memoryID] = struct{}{}
	}
	result := make([]promotion.History, 0, len(wanted))
	for _, history := range histories {
		if _, exists := wanted[history.MemoryID]; exists {
			result = append(result, history)
			delete(wanted, history.MemoryID)
		}
	}
	if len(wanted) != 0 {
		missing := make([]string, 0, len(wanted))
		for memoryID := range wanted {
			missing = append(missing, memoryID)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("promoted memories were not found: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func ensureSeparateRoots(evidenceRoot, repositoryRoot string) error {
	if strings.TrimSpace(repositoryRoot) == "" {
		return errors.New("portable repository root is required")
	}
	evidence, err := filepath.EvalSymlinks(evidenceRoot)
	if err != nil {
		return fmt.Errorf("resolve evidence root: %w", err)
	}
	repository, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return fmt.Errorf("resolve portable repository root: %w", err)
	}
	if pathsOverlap(evidence, repository) {
		return ErrStorageRootsOverlap
	}
	return nil
}

// EnsureSeparateRoots rejects equal, nested, or link-aliased evidence and
// portable repository roots.
func EnsureSeparateRoots(evidenceRoot, repositoryRoot string) error {
	return ensureSeparateRoots(evidenceRoot, repositoryRoot)
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." || relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	}
	return contains(left, right) || contains(right, left)
}

func AcquireRepositoryLock(repositoryRoot string) (*RepositoryLock, error) {
	directory := filepath.Join(repositoryRoot, ".agentmem")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create portable repository state directory: %w", err)
	}
	path := filepath.Join(directory, "repository.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, errors.New("portable repository is locked; inspect the lock before explicit recovery")
	}
	if err != nil {
		return nil, fmt.Errorf("acquire portable repository lock: %w", err)
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write portable repository lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("sync portable repository lock: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close portable repository lock: %w", err)
	}
	return &RepositoryLock{path: path}, nil
}

// AcquireRepositoryUseLease takes the same exclusive lock used by every
// portable-repository mutation. A caller must hold the lease from current-head
// verification until the consumer has durably bound and finished using the
// selected bytes. This prevents a concurrent export, sync, loadout creation,
// supersession, or revocation from invalidating an in-flight delivery.
func AcquireRepositoryUseLease(repositoryRoot string) (*RepositoryLock, error) {
	return AcquireRepositoryLock(repositoryRoot)
}

func (lock *RepositoryLock) Release() error {
	if lock == nil || lock.path == "" {
		return nil
	}
	if err := os.Remove(lock.path); err != nil {
		return err
	}
	lock.path = ""
	return nil
}
