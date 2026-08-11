package capturesupervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

type discoveryIssue struct {
	path     string
	status   string
	itemType string
}

func buildFileInventory(
	ctx context.Context, store *ledger.Store, source Source, runID string, observedAt time.Time, prior *Inventory,
) (Inventory, bool, error) {
	manifest := newInventory(source, runID, observedAt, "pre_capture")
	files, issues, discoveryComplete, discoveryErr := discoverFiles(ctx, source)
	previous := previousKnown(prior)
	forceFull := false
	for _, path := range files {
		identity, err := adapterjsonl.HashSourcePath(path)
		if err != nil {
			return manifest, false, err
		}
		item, prefixMatches, err := observeFile(ctx, path, identity, previous[identity], runID)
		if err != nil {
			status := classifyInventoryError(err)
			if ctx.Err() != nil {
				status = "unverified"
			}
			item = unavailableItem(identity, "file", status, previous[identity])
			manifest.Items = append(manifest.Items, item)
			incrementInventoryStatus(&manifest, status)
			continue
		}
		manifest.Items = append(manifest.Items, item)
		manifest.Available++
		if old, exists := previous[identity]; exists {
			if old.Status != "available" || old.ContentSHA256 != item.ContentSHA256 && !prefixMatches {
				forceFull = true
			}
		}
	}
	for _, issue := range issues {
		identity, err := adapterjsonl.HashSourcePath(issue.path)
		if err != nil {
			identity = hashString(filepath.ToSlash(issue.path))
		}
		itemType := issue.itemType
		if itemType == "" {
			itemType = "file"
		}
		manifest.Items = append(manifest.Items,
			unavailableItem(identity, itemType, issue.status, previous[identity]))
		incrementInventoryStatus(&manifest, issue.status)
	}
	mergeUnavailable(&manifest, previous, discoveryComplete)
	finalizeInventory(&manifest)
	if manifest.Missing > 0 || manifest.Unreadable > 0 || manifest.Unverified > 0 || manifest.Available == 0 {
		forceFull = true
	}
	return manifest, forceFull, discoveryErr
}

func buildSessionInventory(
	source Source, runID string, observedAt time.Time, identities []string, prior *Inventory, complete bool,
) Inventory {
	manifest := newInventory(source, runID, observedAt, "pre_capture")
	previous := previousKnown(prior)
	seen := map[string]struct{}{}
	for _, identity := range identities {
		if !validSHA256(identity) {
			continue
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		manifest.Items = append(manifest.Items, InventoryItem{
			IdentitySHA256: identity, Type: "session", Status: "available", LastSeenRunID: runID,
		})
		manifest.Available++
	}
	if !complete {
		identity := hashString("opencode-native-source\x00" + source.ID)
		manifest.Items = append(manifest.Items, InventoryItem{
			IdentitySHA256: identity, Type: "source", Status: "unreadable",
		})
		manifest.Unreadable++
	}
	mergeUnavailable(&manifest, previous, complete)
	finalizeInventory(&manifest)
	return manifest
}

func newInventory(source Source, runID string, observedAt time.Time, phase string) Inventory {
	sourceConfigSHA256, _ := sourceSHA256(source)
	return Inventory{
		SchemaVersion: InventorySchemaVersion, RunID: runID, SourceID: source.ID,
		SourceConfigSHA256: sourceConfigSHA256,
		Agent:              source.Agent, Kind: source.Kind, Phase: phase, ObservedAt: observedAt.UTC(),
		Items: []InventoryItem{}, Coverage: "locally_observable_only", Privacy: LocalPrivacy,
	}
}

func discoverFiles(ctx context.Context, source Source) ([]string, []discoveryIssue, bool, error) {
	root := source.Path
	info, err := os.Lstat(root)
	if err != nil {
		status := classifyInventoryError(err)
		return nil, []discoveryIssue{{path: root, status: status, itemType: "source"}}, status == "missing", nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, []discoveryIssue{{path: root, status: "link_rejected", itemType: "source"}}, false, nil
	}
	if !info.IsDir() {
		if info.Mode().IsRegular() && matchesSource(source, root, fileEntry{info}) {
			return []string{root}, nil, true, nil
		}
		return nil, []discoveryIssue{{path: root, status: "not_regular_source", itemType: "source"}}, false, nil
	}
	files := []string{}
	issues := []discoveryIssue{}
	complete := true
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			issues = append(issues, discoveryIssue{path: path, status: classifyInventoryError(walkErr)})
			complete = false
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			issues = append(issues, discoveryIssue{path: path, status: "link_rejected"})
			complete = false
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				issues = append(issues, discoveryIssue{path: path, status: "git_boundary_rejected"})
				complete = false
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type() != 0 && !entry.Type().IsRegular() {
			issues = append(issues, discoveryIssue{path: path, status: "not_regular_file"})
			return nil
		}
		if matchesSource(source, path, entry) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return files, issues, false, ctx.Err()
		}
		issues = append(issues, discoveryIssue{path: root, status: classifyInventoryError(err), itemType: "source"})
		complete = false
	}
	sort.Strings(files)
	return files, issues, complete, nil
}

func matchesSource(source Source, path string, entry fs.DirEntry) bool {
	switch source.Kind {
	case SourceCodexRollouts:
		return codex.IsSourceFile(path, entry)
	case SourceClaudeHome:
		relative, err := filepath.Rel(source.Path, path)
		return err == nil && claudecode.IsHomeEvidenceFile(filepath.ToSlash(relative), entry)
	case SourceOpenCodeEvents:
		return opencode.IsEventSourceFile(path, entry)
	default:
		return false
	}
}

type fileEntry struct{ fs.FileInfo }

func (entry fileEntry) Type() fs.FileMode          { return entry.Mode().Type() }
func (entry fileEntry) Info() (fs.FileInfo, error) { return entry.FileInfo, nil }

func observeFile(
	ctx context.Context, path, identity string, previous InventoryItem, runID string,
) (InventoryItem, bool, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return InventoryItem{}, false, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return InventoryItem{}, false, errors.New("source is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return InventoryItem{}, false, err
	}
	fullHasher := sha256.New()
	prefixHasher := sha256.New()
	prefixBytes := previous.Bytes
	if prefixBytes < 0 || prefixBytes > before.Size() || !validSHA256(previous.ContentSHA256) {
		prefixBytes = 0
	}
	var read int64
	buffer := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return InventoryItem{}, false, err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			chunk := buffer[:count]
			_, _ = fullHasher.Write(chunk)
			if read < prefixBytes {
				remaining := prefixBytes - read
				if int64(count) > remaining {
					chunk = chunk[:remaining]
				}
				_, _ = prefixHasher.Write(chunk)
			}
			read += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return InventoryItem{}, false, readErr
		}
	}
	closeErr := file.Close()
	if closeErr != nil {
		return InventoryItem{}, false, closeErr
	}
	after, err := os.Lstat(path)
	if err != nil {
		return InventoryItem{}, false, err
	}
	if before.Size() != read || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return InventoryItem{}, false, errors.New("source changed during inventory")
	}
	modified := before.ModTime().UTC()
	prefixMatches := prefixBytes > 0 &&
		hex.EncodeToString(prefixHasher.Sum(nil)) == previous.ContentSHA256
	return InventoryItem{
		IdentitySHA256: identity, Type: "file", Status: "available",
		ContentSHA256: hex.EncodeToString(fullHasher.Sum(nil)), Bytes: read,
		ModifiedAt: &modified, LastSeenRunID: runID,
	}, prefixMatches, nil
}

func previousKnown(prior *Inventory) map[string]InventoryItem {
	result := map[string]InventoryItem{}
	if prior == nil {
		return result
	}
	for _, item := range prior.Items {
		result[item.IdentitySHA256] = item
	}
	return result
}

func unavailableItem(identity, itemType, status string, previous InventoryItem) InventoryItem {
	return InventoryItem{
		IdentitySHA256: identity, Type: itemType, Status: status,
		ContentSHA256: previous.ContentSHA256, Bytes: previous.Bytes,
		ModifiedAt: previous.ModifiedAt, LastSeenRunID: previous.LastSeenRunID,
	}
}

func incrementInventoryStatus(manifest *Inventory, status string) {
	switch status {
	case "missing":
		manifest.Missing++
	case "unverified":
		manifest.Unverified++
	default:
		manifest.Unreadable++
	}
}

func mergeUnavailable(manifest *Inventory, previous map[string]InventoryItem, discoveryComplete bool) {
	seen := map[string]struct{}{}
	for _, item := range manifest.Items {
		seen[item.IdentitySHA256] = struct{}{}
	}
	for identity, item := range previous {
		if _, exists := seen[identity]; exists {
			continue
		}
		if item.Type == "source" && discoveryComplete {
			continue
		}
		status := "unverified"
		if discoveryComplete {
			status = "missing"
		}
		manifest.Items = append(manifest.Items,
			unavailableItem(identity, item.Type, status, item))
		incrementInventoryStatus(manifest, status)
	}
}

func finalizeInventory(manifest *Inventory) {
	sort.Slice(manifest.Items, func(left, right int) bool {
		if manifest.Items[left].IdentitySHA256 != manifest.Items[right].IdentitySHA256 {
			return manifest.Items[left].IdentitySHA256 < manifest.Items[right].IdentitySHA256
		}
		if manifest.Items[left].Type != manifest.Items[right].Type {
			return manifest.Items[left].Type < manifest.Items[right].Type
		}
		return manifest.Items[left].Status < manifest.Items[right].Status
	})
	switch {
	case manifest.Available == 0 && manifest.Unverified > 0:
		manifest.Status = "unverified"
	case manifest.Available == 0 && manifest.Missing > 0:
		manifest.Status = "missing"
	case manifest.Available == 0 && manifest.Unreadable > 0:
		manifest.Status = "unreadable"
	case manifest.Available == 0:
		manifest.Status = "empty"
	case manifest.Missing > 0 || manifest.Unreadable > 0 || manifest.Unverified > 0:
		manifest.Status = "partial"
	default:
		manifest.Status = "available"
	}
}

func inventoriesEquivalent(left, right Inventory) bool {
	return left.SourceID == right.SourceID && left.Agent == right.Agent && left.Kind == right.Kind &&
		left.Status == right.Status && left.Available == right.Available &&
		left.Missing == right.Missing && left.Unreadable == right.Unreadable &&
		left.Unverified == right.Unverified && reflect.DeepEqual(left.Items, right.Items)
}

func writeInventory(store *ledger.Store, inventory Inventory) (Inventory, error) {
	inventory.InventorySHA256 = ""
	hash, err := inventorySHA256(inventory)
	if err != nil {
		return inventory, err
	}
	inventory.InventorySHA256 = hash
	if err := validateInventory(inventory); err != nil {
		return inventory, err
	}
	data, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return inventory, err
	}
	data = append(data, '\n')
	path := filepath.Join(inventoryRoot(store), hash+".json")
	if err := writeImmutable(store, path, data); err != nil {
		return inventory, err
	}
	return inventory, nil
}

func loadInventory(store *ledger.Store, hash string) (Inventory, error) {
	if !validSHA256(hash) {
		return Inventory{}, errors.New("capture inventory hash is invalid")
	}
	data, err := readImmutable(store, filepath.Join(inventoryRoot(store), hash+".json"))
	if err != nil {
		return Inventory{}, err
	}
	var inventory Inventory
	if err := decodeStrict(data, &inventory); err != nil {
		return Inventory{}, errors.New("capture inventory is invalid")
	}
	if inventory.InventorySHA256 != hash {
		return Inventory{}, errors.New("capture inventory filename does not match its content")
	}
	if err := validateInventory(inventory); err != nil {
		return Inventory{}, err
	}
	return inventory, nil
}

func inventorySHA256(inventory Inventory) (string, error) {
	inventory.InventorySHA256 = ""
	data, err := json.Marshal(inventory)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validateInventory(inventory Inventory) error {
	if inventory.SchemaVersion != InventorySchemaVersion || inventory.Privacy != LocalPrivacy ||
		inventory.RunID == "" || !sourceIDPattern.MatchString(inventory.SourceID) ||
		!validSHA256(inventory.SourceConfigSHA256) ||
		inventory.ObservedAt.IsZero() || inventory.Coverage != "locally_observable_only" ||
		!oneOf(inventory.Phase, "pre_capture", "post_capture") ||
		!oneOf(inventory.Status, "available", "partial", "missing", "unreadable", "unverified", "empty") {
		return errors.New("capture inventory has an unsupported contract")
	}
	expectedAgent := ledger.AgentUnknown
	switch inventory.Kind {
	case SourceCodexRollouts:
		expectedAgent = ledger.AgentCodex
	case SourceClaudeHome:
		expectedAgent = ledger.AgentClaudeCode
	case SourceOpenCodeNative, SourceOpenCodeEvents:
		expectedAgent = ledger.AgentOpenCode
	}
	if inventory.Agent != expectedAgent {
		return errors.New("capture inventory agent does not match its kind")
	}
	if len(inventory.Items) > 1_000_000 {
		return errors.New("capture inventory contains too many items")
	}
	expected, err := inventorySHA256(inventory)
	if err != nil || expected != inventory.InventorySHA256 {
		return errors.New("capture inventory hash verification failed")
	}
	available, missing, unreadable, unverified := 0, 0, 0, 0
	seen := map[string]struct{}{}
	for _, item := range inventory.Items {
		if !validSHA256(item.IdentitySHA256) || !oneOf(item.Type, "file", "session", "source") ||
			!oneOf(item.Status, "available", "missing", "permission_denied", "unreadable",
				"unverified", "link_rejected", "git_boundary_rejected", "not_regular_source", "not_regular_file") {
			return errors.New("capture inventory contains an invalid item")
		}
		key := item.IdentitySHA256 + "\x00" + item.Type
		if _, duplicate := seen[key]; duplicate {
			return errors.New("capture inventory repeats an item")
		}
		seen[key] = struct{}{}
		switch item.Status {
		case "available":
			available++
			if item.Type == "source" {
				return errors.New("capture inventory source marker cannot be available")
			}
			if item.Type == "file" && (!validSHA256(item.ContentSHA256) || item.Bytes < 0 || item.ModifiedAt == nil) {
				return errors.New("capture inventory available file is incomplete")
			}
		case "missing":
			missing++
		case "unverified":
			unverified++
		default:
			unreadable++
		}
	}
	if available != inventory.Available || missing != inventory.Missing ||
		unreadable != inventory.Unreadable || unverified != inventory.Unverified {
		return errors.New("capture inventory totals do not match its items")
	}
	return nil
}

func missingGapEvents(
	store *ledger.Store, source Source, inventory Inventory, now time.Time,
) []ledger.Event {
	events := []ledger.Event{}
	for _, item := range inventory.Items {
		if item.Status != "missing" && item.Status != "permission_denied" &&
			item.Status != "unreadable" && item.Status != "link_rejected" &&
			item.Status != "git_boundary_rejected" && item.Status != "not_regular_source" &&
			item.Status != "not_regular_file" && item.Status != "unverified" {
			continue
		}
		reason := "source_inventory_" + item.Status
		eventID := adapterjsonl.DeterministicID("capture-inventory-gap", source.ID,
			item.IdentitySHA256, item.LastSeenRunID, reason)
		events = append(events, ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindGap,
			ObservedAt: now.UTC(), RecordedAt: now.UTC(),
			Source: ledger.Source{
				Agent: source.Agent, Adapter: "capture-supervisor-inventory",
				AdapterVersion: "capture-supervisor-inventory/v1alpha1",
				DeviceID:       store.DeviceID(), ThreadID: "inventory-" + source.ID,
				SourcePathHash: item.IdentitySHA256,
			},
			Completeness: ledger.Completeness{Status: ledger.CompletenessMissing, Reason: reason},
			Privacy:      ledger.Privacy{Classification: LocalPrivacy},
		})
	}
	return events
}

func appendInventoryGaps(
	ctx context.Context, store *ledger.Store, events []ledger.Event,
) (map[string]int, error) {
	counts := map[string]int{}
	if len(events) == 0 {
		return counts, nil
	}
	known := map[string]struct{}{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		known[record.Event.EventID] = struct{}{}
		return nil
	})
	if err != nil {
		return counts, err
	}
	defer appender.Close()
	pending := make([]ledger.Event, 0, len(events))
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return map[string]int{}, err
		}
		if _, exists := known[event.EventID]; exists {
			continue
		}
		known[event.EventID] = struct{}{}
		pending = append(pending, event)
		counts[strings.TrimPrefix(event.Source.ThreadID, "inventory-")]++
	}
	records, err := appender.AppendBatch(pending)
	if err != nil {
		return map[string]int{}, err
	}
	if err := appender.Close(); err != nil {
		return map[string]int{}, err
	}
	if len(records) != len(pending) {
		return map[string]int{}, errors.New("capture inventory gap append count is inconsistent")
	}
	return counts, nil
}

func classifyInventoryError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "unreadable"
	}
}

func hashString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
