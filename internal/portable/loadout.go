package portable

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/secretscan"
)

const (
	DefaultLoadoutTokenBudget = 1200
	MaximumLoadoutTokenBudget = 8192
	DefaultLoadoutByteBudget  = 6144
	MaximumLoadoutByteBudget  = 128 * 1024
	MaximumLoadoutMemories    = 64
)

// CreateLoadout writes an immutable, content-addressed loadout to the portable
// Git repository. It contains exact portable revision references, not local
// evidence, review identities, or raw task content.
func CreateLoadout(root string, options LoadoutCreateOptions) (LoadoutCreateResult, error) {
	result := LoadoutCreateResult{
		SchemaVersion: LoadoutCreateSchemaVersion,
		Privacy:       PortablePrivacy,
	}
	if strings.TrimSpace(root) == "" {
		return result, errors.New("portable repository root is required")
	}
	lock, err := AcquireRepositoryLock(root)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	report, state := loadRepository(root)
	if len(report.Issues) != 0 {
		return result, fmt.Errorf("portable repository verification failed: %s", report.Issues[0].Message)
	}
	loadout, err := buildLoadout(options, state.heads)
	if err != nil {
		return result, err
	}
	data, err := renderLoadout(loadout)
	if err != nil {
		return result, err
	}
	relative := loadoutRelativePath(loadout.LoadoutID)
	result.Loadout = loadout
	result.RelativePath = filepath.ToSlash(relative)
	destination := filepath.Join(root, relative)
	existing, readErr := os.ReadFile(destination)
	if readErr == nil {
		if !bytes.Equal(existing, data) {
			return result, errors.New("portable loadout identity already exists with different content")
		}
		return result, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		return result, fmt.Errorf("read portable loadout: %w", readErr)
	}
	if err := writeFileAtomic(destination, data); err != nil {
		return result, err
	}
	result.Written = true
	final := VerifyRepository(root)
	if len(final.Issues) != 0 {
		return result, fmt.Errorf("portable repository failed verification after loadout creation: %s", final.Issues[0].Message)
	}
	if err := lock.Release(); err != nil {
		return result, fmt.Errorf("loadout creation completed but lock cleanup failed: %w", err)
	}
	return result, nil
}

// ListLoadouts returns every structurally verified historical loadout. A
// listed loadout may be stale; call LoadCurrentLoadout before using it.
func ListLoadouts(root string) ([]Loadout, VerificationReport) {
	report, state := loadRepository(root)
	if len(report.Issues) != 0 {
		return nil, report
	}
	ids := make([]string, 0, len(state.loadouts))
	for id := range state.loadouts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Loadout, 0, len(ids))
	for _, id := range ids {
		result = append(result, state.loadouts[id])
	}
	return result, report
}

// LoadCurrentLoadout verifies the complete portable repository and refuses a
// loadout if any referenced revision is missing, revoked, or superseded.
func LoadCurrentLoadout(root, loadoutID string) (Loadout, VerificationReport, error) {
	report, state := loadRepository(root)
	if len(report.Issues) != 0 {
		return Loadout{}, report, errors.New("portable repository is not eligible for loadout use")
	}
	loadout, exists := state.loadouts[loadoutID]
	if !exists {
		return Loadout{}, report, errors.New("portable loadout is unavailable")
	}
	if err := validateLoadoutCurrent(loadout, state.heads); err != nil {
		return Loadout{}, report, err
	}
	return loadout, report, nil
}

func buildLoadout(options LoadoutCreateOptions, heads map[string]Revision) (Loadout, error) {
	if options.TokenBudget == 0 {
		options.TokenBudget = DefaultLoadoutTokenBudget
	}
	if options.ByteBudget == 0 {
		options.ByteBudget = DefaultLoadoutByteBudget
	}
	loadout := Loadout{
		SchemaVersion: LoadoutSchemaVersion,
		Name:          strings.TrimSpace(options.Name),
		Description:   strings.TrimSpace(options.Description),
		Agents:        canonicalAgents(options.Agents),
		ScopeKind:     options.Scope.Kind,
		ScopeValue:    options.Scope.Value,
		Memories:      []LoadoutMemoryReference{},
		TokenBudget:   options.TokenBudget,
		ByteBudget:    options.ByteBudget,
		Privacy:       PortablePrivacy,
	}
	if len(loadout.Agents) != len(options.Agents) {
		return Loadout{}, errors.New("loadout agents must be unique supported agents")
	}
	seen := map[string]struct{}{}
	for _, memoryID := range options.MemoryIDs {
		if _, duplicate := seen[memoryID]; duplicate {
			return Loadout{}, fmt.Errorf("memory %s appears more than once in the loadout", memoryID)
		}
		revision, exists := heads[memoryID]
		if !exists || revision.Status != StatusActive {
			return Loadout{}, fmt.Errorf("memory %s is not an active portable head", memoryID)
		}
		seen[memoryID] = struct{}{}
		loadout.Memories = append(loadout.Memories, LoadoutMemoryReference{
			MemoryID: revision.MemoryID, RevisionID: revision.RevisionID,
		})
	}
	id, err := deriveLoadoutID(loadout)
	if err != nil {
		return Loadout{}, err
	}
	loadout.LoadoutID = id
	if err := validateLoadoutEnvelope(loadout); err != nil {
		return Loadout{}, err
	}
	if err := validateLoadoutCurrent(loadout, heads); err != nil {
		return Loadout{}, err
	}
	return loadout, nil
}

func parseLoadout(data []byte) (Loadout, error) {
	var loadout Loadout
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&loadout); err != nil {
		return Loadout{}, fmt.Errorf("decode portable loadout: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Loadout{}, errors.New("portable loadout contains trailing JSON")
	}
	if err := validateLoadoutEnvelope(loadout); err != nil {
		return Loadout{}, err
	}
	canonical, err := renderLoadout(loadout)
	if err != nil {
		return Loadout{}, err
	}
	if !bytes.Equal(canonical, data) {
		return Loadout{}, errors.New("portable loadout is not canonically encoded")
	}
	return loadout, nil
}

func renderLoadout(loadout Loadout) ([]byte, error) {
	data, err := json.MarshalIndent(loadout, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode portable loadout: %w", err)
	}
	return append(data, '\n'), nil
}

func validateLoadoutEnvelope(loadout Loadout) error {
	if loadout.SchemaVersion != LoadoutSchemaVersion ||
		!validPrefixedHash(loadout.LoadoutID, "loadout-") ||
		loadout.Privacy != PortablePrivacy ||
		loadout.Name == "" || loadout.Name != strings.TrimSpace(loadout.Name) ||
		len([]byte(loadout.Name)) > 128 || strings.ContainsAny(loadout.Name, "\x00\r\n") ||
		len([]byte(loadout.Description)) > 1024 || !utf8.ValidString(loadout.Description) ||
		strings.ContainsAny(loadout.Description, "\x00\r") ||
		!validScope(loadout.ScopeKind, loadout.ScopeValue) {
		return errors.New("portable loadout envelope is invalid")
	}
	if loadout.TokenBudget < 1 || loadout.TokenBudget > MaximumLoadoutTokenBudget ||
		loadout.ByteBudget < 1 || loadout.ByteBudget > MaximumLoadoutByteBudget ||
		len(loadout.Memories) == 0 || len(loadout.Memories) > MaximumLoadoutMemories ||
		len(loadout.Agents) == 0 || len(loadout.Agents) > 3 {
		return errors.New("portable loadout limits are invalid")
	}
	if len(secretscan.Scan(loadout.Name+"\n"+loadout.Description+"\n"+loadout.ScopeValue).Findings) != 0 {
		return errors.New("portable loadout contains sensitive content")
	}
	if !equalAgents(loadout.Agents, canonicalAgents(loadout.Agents)) {
		return errors.New("portable loadout agents are invalid or non-canonical")
	}
	seen := map[string]struct{}{}
	for _, ref := range loadout.Memories {
		if !validPrefixedHash(ref.MemoryID, "memory-") ||
			!validPrefixedHash(ref.RevisionID, "portable-revision-") {
			return errors.New("portable loadout contains an invalid memory reference")
		}
		if _, duplicate := seen[ref.MemoryID]; duplicate {
			return errors.New("portable loadout contains a duplicate memory")
		}
		seen[ref.MemoryID] = struct{}{}
	}
	expected, err := deriveLoadoutID(loadout)
	if err != nil || expected != loadout.LoadoutID {
		return errors.New("portable loadout id does not match its canonical content")
	}
	return nil
}

func validateLoadoutHistory(loadout Loadout, revisions map[string]Revision) error {
	for _, ref := range loadout.Memories {
		revision, exists := revisions[ref.RevisionID]
		if !exists || revision.MemoryID != ref.MemoryID {
			return errors.New("portable loadout references an unavailable memory revision")
		}
	}
	return nil
}

func validateLoadoutCurrent(loadout Loadout, heads map[string]Revision) error {
	for _, ref := range loadout.Memories {
		head, exists := heads[ref.MemoryID]
		if !exists || head.Status != StatusActive || head.RevisionID != ref.RevisionID {
			return errors.New("portable loadout no longer references the exact active memory heads")
		}
	}
	return nil
}

func deriveLoadoutID(loadout Loadout) (string, error) {
	copy := loadout
	copy.LoadoutID = ""
	data, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("encode portable loadout identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return "loadout-" + hex.EncodeToString(digest[:]), nil
}

func loadoutRelativePath(loadoutID string) string {
	return filepath.Join("loadouts", strings.TrimPrefix(loadoutID, "loadout-")+".json")
}

func canonicalAgents(values []ledger.Agent) []ledger.Agent {
	result := append([]ledger.Agent(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	unique := result[:0]
	for _, value := range result {
		if value != ledger.AgentCodex && value != ledger.AgentClaudeCode &&
			value != ledger.AgentOpenCode && value != ledger.AgentDeepSeekHarness {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func equalAgents(left, right []ledger.Agent) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validLoadoutPath(parts []string) bool {
	if len(parts) != 2 || parts[0] != "loadouts" || filepath.Ext(parts[1]) != ".json" {
		return false
	}
	return validHash(strings.TrimSuffix(parts[1], ".json"))
}
