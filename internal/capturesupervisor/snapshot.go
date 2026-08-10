package capturesupervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const EvaluationSnapshotSchemaVersion = "capture-evaluation-snapshot/v1alpha1"

// EvaluationSnapshot is an independently replayed inventory denominator for
// capture evaluation. It contains hashes and inventory metadata, never paths or
// captured content.
type EvaluationSnapshot struct {
	SchemaVersion    string                     `json:"schema_version"`
	RunID            string                     `json:"run_id"`
	ConfigSHA256     string                     `json:"config_sha256"`
	AuditEventSHA256 string                     `json:"audit_event_sha256"`
	CompletedAt      time.Time                  `json:"completed_at"`
	FullReconcile    bool                       `json:"full_reconcile"`
	Sources          []EvaluationSnapshotSource `json:"sources"`
	SnapshotSHA256   string                     `json:"snapshot_sha256"`
	Privacy          string                     `json:"privacy"`
}

type EvaluationSnapshotSource struct {
	SourceID           string          `json:"source_id"`
	Agent              ledger.Agent    `json:"agent"`
	Kind               SourceKind      `json:"kind"`
	SourceConfigSHA256 string          `json:"source_config_sha256"`
	InventorySHA256    string          `json:"inventory_sha256"`
	Items              []InventoryItem `json:"items"`
}

// LoadEvaluationSnapshot replays the append-only supervisor audit and returns
// the last completed successful run. Every required source must have succeeded
// with an inventory already validated by the supervisor replay.
func LoadEvaluationSnapshot(store *ledger.Store, requiredAgents []ledger.Agent) (EvaluationSnapshot, error) {
	if store == nil {
		return emptyEvaluationSnapshot(), errors.New("store is required")
	}
	state, err := replayAudit(store)
	if err != nil {
		return emptyEvaluationSnapshot(), fmt.Errorf("verify capture supervisor audit: %w", err)
	}
	return evaluationSnapshotFromState(store, state, requiredAgents)
}

// LoadEvaluationSnapshotForAudit reconstructs the snapshot committed by a
// historical successful run_finished audit event. Later supervisor runs are
// outside the verified audit prefix and cannot change the result.
func LoadEvaluationSnapshotForAudit(store *ledger.Store, auditEventSHA256 string, requiredAgents []ledger.Agent) (EvaluationSnapshot, error) {
	if store == nil {
		return emptyEvaluationSnapshot(), errors.New("store is required")
	}
	if !validSHA256(auditEventSHA256) {
		return emptyEvaluationSnapshot(), errors.New("capture supervisor audit target hash is invalid")
	}
	state, err := replayAuditThrough(store, auditEventSHA256)
	if err != nil {
		return emptyEvaluationSnapshot(), fmt.Errorf("verify capture supervisor audit: %w", err)
	}
	return evaluationSnapshotFromState(store, state, requiredAgents)
}

// VerifyEvaluationSnapshot verifies both a snapshot's self-hash and its exact
// reconstruction from the historical supervisor audit prefix it names.
func VerifyEvaluationSnapshot(store *ledger.Store, snapshot EvaluationSnapshot, requiredAgents []ledger.Agent) error {
	digest, err := evaluationSnapshotSHA256(snapshot)
	if err != nil {
		return err
	}
	if !validSHA256(snapshot.SnapshotSHA256) || digest != snapshot.SnapshotSHA256 {
		return errors.New("capture evaluation snapshot hash verification failed")
	}
	expected, err := LoadEvaluationSnapshotForAudit(store, snapshot.AuditEventSHA256, requiredAgents)
	if err != nil {
		return err
	}
	if expected.SnapshotSHA256 != snapshot.SnapshotSHA256 {
		return errors.New("capture evaluation snapshot does not match its audit event")
	}
	return nil
}

func emptyEvaluationSnapshot() EvaluationSnapshot {
	return EvaluationSnapshot{SchemaVersion: EvaluationSnapshotSchemaVersion,
		Sources: []EvaluationSnapshotSource{}, Privacy: "local_only"}
}

func evaluationSnapshotFromState(store *ledger.Store, state replayState, requiredAgents []ledger.Agent) (EvaluationSnapshot, error) {
	snapshot := emptyEvaluationSnapshot()
	if state.LastCompletedRunID == "" || state.LastCompletedOutcome != "success" {
		return snapshot, errors.New("a completed successful capture run is required")
	}
	if !state.LastCompletedFullReconcile {
		return snapshot, errors.New("the last successful capture run was not a full reconcile")
	}
	snapshot.RunID = state.LastCompletedRunID
	snapshot.ConfigSHA256 = state.LastCompletedConfigSHA256
	snapshot.AuditEventSHA256 = state.LastCompletedAuditSHA256
	snapshot.CompletedAt = state.LastCompletedAt
	snapshot.FullReconcile = state.LastCompletedFullReconcile

	required := map[ledger.Agent]bool{}
	for _, agent := range requiredAgents {
		required[agent] = false
	}
	sourceIDs := make([]string, 0, len(state.LastCompletedSources))
	for sourceID := range state.LastCompletedSources {
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	for _, sourceID := range sourceIDs {
		source := state.LastCompletedSources[sourceID]
		if !source.Required {
			continue
		}
		result, ok := state.LastCompletedResults[sourceID]
		if !ok || result.Outcome != "success" || result.SourceConfigSHA256 == "" {
			return snapshot, fmt.Errorf("required capture source %s did not succeed", sourceID)
		}
		inventories := state.LastCompletedInventories[sourceID]
		if err := validateSuccessfulInventories(source, inventories); err != nil {
			return snapshot, fmt.Errorf("required capture source %s inventory: %w", sourceID, err)
		}
		inventory := inventories[0]
		items := append([]InventoryItem(nil), inventory.Items...)
		sort.Slice(items, func(left, right int) bool {
			if items[left].IdentitySHA256 != items[right].IdentitySHA256 {
				return items[left].IdentitySHA256 < items[right].IdentitySHA256
			}
			return items[left].Status < items[right].Status
		})
		for _, item := range items {
			if item.Type == "file" && item.Status == "available" {
				if err := verifyCapturedInventoryBlob(store, item); err != nil {
					return snapshot, fmt.Errorf("required capture source %s item: %w", sourceID, err)
				}
			}
		}
		snapshot.Sources = append(snapshot.Sources, EvaluationSnapshotSource{
			SourceID: sourceID, Agent: source.Agent, Kind: source.Kind,
			SourceConfigSHA256: result.SourceConfigSHA256,
			InventorySHA256:    inventory.InventorySHA256, Items: items,
		})
		if _, requested := required[source.Agent]; requested {
			required[source.Agent] = true
		}
	}
	for agent, found := range required {
		if !found {
			return snapshot, fmt.Errorf("successful required capture source is missing for %s", agent)
		}
	}
	if len(snapshot.Sources) == 0 {
		return snapshot, errors.New("successful capture run has no required sources")
	}
	digest, err := evaluationSnapshotSHA256(snapshot)
	if err != nil {
		return snapshot, err
	}
	snapshot.SnapshotSHA256 = digest
	return snapshot, nil
}

func evaluationSnapshotSHA256(snapshot EvaluationSnapshot) (string, error) {
	snapshot.SnapshotSHA256 = ""
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("encode capture evaluation snapshot: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func verifyCapturedInventoryBlob(store *ledger.Store, item InventoryItem) error {
	reference := ledger.BlobRef{SHA256: item.ContentSHA256, Bytes: item.Bytes,
		RelativePath: path.Join("evidence", "blobs", "sha256", item.ContentSHA256[:2], item.ContentSHA256[2:])}
	file, err := store.OpenBlob(reference)
	if err != nil {
		return errors.New("captured full-source blob is unavailable")
	}
	defer file.Close()
	hasher := sha256.New()
	bytesRead, err := io.Copy(hasher, file)
	if err != nil {
		return errors.New("captured full-source blob is unreadable")
	}
	if bytesRead != item.Bytes || hex.EncodeToString(hasher.Sum(nil)) != item.ContentSHA256 {
		return errors.New("captured full-source blob does not match the inventory")
	}
	return nil
}
