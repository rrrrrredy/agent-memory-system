package schemas_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/capturesupervisor"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestCaptureSupervisorInstancesMatchPublishedSchemas(t *testing.T) {
	base := t.TempDir()
	store, err := ledger.Init(filepath.Join(base, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "codex")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(source,
		"rollout-2027-03-01T00-00-00-00000000-0000-0000-0000-000000000222.jsonl")
	data := []byte(`{"timestamp":"2027-03-01T00:00:00Z","type":"session_meta","payload":{"id":"00000000-0000-0000-0000-000000000222"}}` + "\n")
	if err := os.WriteFile(rollout, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config := capturesupervisor.Config{
		SchemaVersion: capturesupervisor.ConfigSchemaVersion, IntervalSeconds: 30,
		FullReconcileEveryRuns: 12, SourceTimeoutSeconds: 60,
		Sources: []capturesupervisor.Source{{
			ID: "codex", Agent: ledger.AgentCodex, Kind: capturesupervisor.SourceCodexRollouts,
			Required: true, Path: source,
		}}, Privacy: capturesupervisor.LocalPrivacy,
	}
	configInput := filepath.Join(base, "capture-config.json")
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configInput, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1801000000, 0).UTC()
	configured, err := capturesupervisor.ConfigureFile(store, configInput, now)
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "capture-supervisor-status.schema.json", configured)
	run, err := capturesupervisor.Run(context.Background(), store,
		capturesupervisor.RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "capture-supervisor-run-result.schema.json", run)
	for _, result := range run.Sources {
		validatePublishedInstance(t, "capture-supervisor-source-result.schema.json", result)
	}
	status, err := capturesupervisor.GetStatus(store, capturesupervisor.StatusOptions{
		RequireConfigured: true, RequireHealthy: true,
		RequiredAgents: []ledger.Agent{ledger.AgentCodex}, MaximumAge: time.Hour,
		Now: func() time.Time { return now.Add(2 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "capture-supervisor-status.schema.json", status)

	stateRoot := filepath.Join(store.Root(), "state", "capture-supervisor")
	validateStateObjects(t, filepath.Join(stateRoot, "configs"),
		"capture-supervisor-config.schema.json", capturesupervisor.Config{})
	validateStateObjects(t, filepath.Join(stateRoot, "inventories"),
		"capture-supervisor-inventory.schema.json", capturesupervisor.Inventory{})
	validateStateObjects(t, filepath.Join(stateRoot, "audit"),
		"capture-supervisor-event.schema.json", capturesupervisor.Event{})

	doctor := diagnostics.Run(context.Background(), store, diagnostics.Options{
		RequireCapture: true, CaptureRequiredAgents: []ledger.Agent{ledger.AgentCodex},
		CaptureMaximumAge: time.Hour, Now: func() time.Time { return now.Add(2 * time.Second) },
	})
	if !doctor.Ready || doctor.CaptureSupervisor == nil {
		t.Fatalf("capture-aware doctor is not ready: %+v", doctor)
	}
	validatePublishedInstance(t, "agent-memory-doctor-report.schema.json", doctor)
}

func TestCaptureSupervisorSchemasRejectInconsistentContracts(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	baseConfig := map[string]any{
		"schema_version": "capture-supervisor-config/v1alpha1", "interval_seconds": 30,
		"full_reconcile_every_runs": 12, "source_timeout_seconds": 60,
		"privacy": "local_only",
		"sources": []any{map[string]any{
			"id": "codex", "agent": "codex", "kind": "codex_rollouts",
			"required": true, "path": "/private/codex",
		}},
	}
	wrongConfigAgent := copyMap(baseConfig)
	wrongConfigAgent["sources"] = []any{map[string]any{
		"id": "codex", "agent": "opencode", "kind": "codex_rollouts",
		"required": true, "path": "/private/codex",
	}}
	rejectPublishedInstance(t, "capture-supervisor-config.schema.json", wrongConfigAgent)
	noRequiredSource := copyMap(baseConfig)
	noRequiredSource["sources"] = []any{map[string]any{
		"id": "codex", "agent": "codex", "kind": "codex_rollouts",
		"required": false, "path": "/private/codex",
	}}
	rejectPublishedInstance(t, "capture-supervisor-config.schema.json", noRequiredSource)
	wrongConfigFields := copyMap(baseConfig)
	wrongConfigFields["sources"] = []any{map[string]any{
		"id": "native", "agent": "opencode", "kind": "opencode_native",
		"required": true, "path": "/private/opencode",
	}}
	rejectPublishedInstance(t, "capture-supervisor-config.schema.json", wrongConfigFields)

	baseSource := map[string]any{
		"source_id": "codex", "agent": "codex", "kind": "codex_rollouts",
		"required": true, "source_config_sha256": hash, "inventory_sha256": hash,
		"outcome": "success", "files_examined": 1, "files_changed": 0,
		"events_appended": 0, "gaps_appended": 0, "bytes_captured": 0,
	}
	withError := copyMap(baseSource)
	withError["error_code"] = "should_not_exist"
	rejectPublishedInstance(t, "capture-supervisor-source-result.schema.json", withError)

	wrongAgent := copyMap(baseSource)
	wrongAgent["agent"] = "opencode"
	rejectPublishedInstance(t, "capture-supervisor-source-result.schema.json", wrongAgent)

	badEvent := map[string]any{
		"schema_version": "capture-supervisor-event/v1alpha1", "sequence": 1,
		"event_id": "event-one", "observed_at": "2027-03-01T00:00:00Z",
		"action": "configure", "outcome": "success", "config_sha256": hash,
		"event_sha256": hash, "privacy": "local_only",
	}
	rejectPublishedInstance(t, "capture-supervisor-event.schema.json", badEvent)

	incompleteAvailableFile := map[string]any{
		"schema_version": "capture-supervisor-inventory/v1alpha1", "run_id": "run-one",
		"source_id": "codex", "source_config_sha256": hash, "agent": "codex",
		"kind": "codex_rollouts", "phase": "pre_capture", "observed_at": "2027-03-01T00:00:00Z",
		"status": "available", "items": []any{map[string]any{
			"identity_sha256": hash, "type": "file", "status": "available",
		}},
		"available": 1, "missing": 0, "unreadable": 0, "unverified": 0,
		"coverage": "locally_observable_only", "inventory_sha256": hash, "privacy": "local_only",
	}
	rejectPublishedInstance(t, "capture-supervisor-inventory.schema.json", incompleteAvailableFile)
	sourceMarkedAvailable := copyMap(incompleteAvailableFile)
	sourceMarkedAvailable["items"] = []any{map[string]any{
		"identity_sha256": hash, "type": "source", "status": "available",
	}}
	rejectPublishedInstance(t, "capture-supervisor-inventory.schema.json", sourceMarkedAvailable)
}

func copyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func validateStateObjects[T any](t *testing.T, root, schema string, zero T) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no state objects for %s: %v", schema, err)
	}
	for _, statePath := range paths {
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		instance := any(zero)
		switch any(zero).(type) {
		case capturesupervisor.Config:
			var value capturesupervisor.Config
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			instance = value
		case capturesupervisor.Inventory:
			var value capturesupervisor.Inventory
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			instance = value
		case capturesupervisor.Event:
			var value capturesupervisor.Event
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			instance = value
		}
		validatePublishedInstance(t, schema, instance)
	}
}
