package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/capturesupervisor"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func TestRecoveryAndRetrievalProducerInstancesMatchPublishedSchemas(t *testing.T) {
	hash := strings.Repeat("a", 64)
	instances := []struct {
		schema string
		value  any
	}{
		{"evidence-writer-recovery-result.schema.json", ledger.WriterRecoveryResult{
			SchemaVersion: ledger.WriterRecoveryResultSchemaVersion, WriterLockCleared: true,
			Verification: ledger.VerificationReport{RecordsChecked: 1, BlobsChecked: 1, LastRecordHash: hash, Issues: []string{}},
			Privacy:      "local_only",
		}},
		{"task-oracle-registry-init.schema.json", evaluation.OracleRegistryInitResult{
			SchemaVersion: evaluation.OracleRegistryInitResultSchemaVersion,
			File:          "oracle-registry.json", EntrySHA256: hash, Privacy: "local_only",
		}},
		{"git-sync-hook-install-result.schema.json", gitsync.HookInstallResult{
			SchemaVersion: gitsync.HookInstallResultSchemaVersion, Installed: true, Privacy: "portable_only",
		}},
		{"capture-supervisor-lock-recovery-result.schema.json", capturesupervisor.LockRecoveryResult{
			SchemaVersion: capturesupervisor.LockRecoveryResultSchemaVersion, LockCleared: true, Privacy: "local_only",
		}},
		{"memory-retrieval-result.schema.json", retrieval.Result{
			SchemaVersion: retrieval.ResultSchemaVersion, ReceiptID: "retrieval-example", Status: "completed",
			SelectorSHA256: hash, RepositoryIssues: []string{}, Selected: []retrieval.Match{},
			TokenBudget: 128, ByteBudget: 4096, Exclusions: retrieval.ExclusionCounts{},
			ContextGaps: []string{}, Privacy: "local_only",
		}},
	}
	for _, instance := range instances {
		t.Run(instance.schema, func(t *testing.T) {
			validatePublishedInstance(t, instance.schema, instance.value)
			data, err := json.Marshal(instance.value)
			if err != nil {
				t.Fatal(err)
			}
			var wrong map[string]any
			if err := json.Unmarshal(data, &wrong); err != nil {
				t.Fatal(err)
			}
			wrong["schema_version"] = "unsupported/v1"
			rejectPublishedInstance(t, instance.schema, wrong)
		})
	}
}

func TestReleaseAndHostedReceiptInstancesMatchPublishedSchemas(t *testing.T) {
	hash64 := strings.Repeat("a", 64)
	hash40 := strings.Repeat("b", 40)
	quickstart := map[string]any{
		"schema_version": "quickstart-smoke/v1alpha1", "fixture_sha256": hash64,
		"simulated_attestations": true, "gaps_appended": 0, "doctor_ready": true,
		"review_ready": 1, "revisions_written": 1, "active_memories": 1, "selected_memories": 1,
	}
	version := map[string]any{
		"schema_version": "agent-memory-version/v1alpha1", "version": "v0.1.0-rc.1",
		"commit": hash40, "build_date": "2026-08-11T00:00:00Z", "go_version": "go1.26.5", "platform": "windows/amd64",
	}
	windows := map[string]any{
		"schema_version": "agentmem-candidate-acceptance/v1alpha1", "os": "windows", "runner_arch": "X64",
		"archive": "agentmem_v0.1.0-rc.1_windows_amd64.zip", "archive_sha256": hash64,
		"version": version, "quickstart": quickstart,
	}
	macVersion := cloneMap(t, version)
	macVersion["platform"] = "darwin/arm64"
	macos := map[string]any{
		"schema_version": "agentmem-candidate-acceptance/v1alpha1", "os": "macos", "runner_arch": "ARM64",
		"archive": "agentmem_v0.1.0-rc.1_darwin_arm64.tar.gz", "archive_sha256": hash64,
		"version": macVersion, "quickstart": quickstart,
	}
	job := func(id int, name string) map[string]any {
		return map[string]any{"id": id, "name": name, "status": "completed", "conclusion": "success",
			"html_url": "https://github.com/rrrrrredy/agent-memory-system/actions/runs/1/job/1", "head_sha": hash40, "run_id": 1}
	}
	requiredChecks := map[string]any{
		"schema_version": "agentmem-required-checks/v1alpha1",
		"workflow": map[string]any{"id": 1, "path": ".github/workflows/ci.yml", "run_id": 1, "run_attempt": 1,
			"event": "push", "head_branch": "main", "head_sha": hash40,
			"html_url": "https://github.com/rrrrrredy/agent-memory-system/actions/runs/1"},
		"jobs": []any{job(1, "test (ubuntu-latest)"), job(2, "test (windows-latest)"), job(3, "test (macos-latest)"),
			job(4, "race"), job(5, "fuzz-smoke"), job(6, "opencode-runtime"), job(7, "public-tree-privacy")},
	}
	provenance := map[string]any{
		"schema_version": "agentmem-release-provenance/v1alpha1", "version": "v0.1.0-rc.1",
		"commit": hash40, "observed_main": hash40, "ref": "refs/heads/main",
		"repository": "rrrrrredy/agent-memory-system", "workflow_run_id": "1",
		"sha256sums_sha256": hash64, "required_checks_sha256": hash64,
		"windows_acceptance_sha256": hash64, "macos_acceptance_sha256": hash64,
		"required_checks": requiredChecks, "hosted_acceptance": []any{windows, macos},
	}
	opencode := map[string]any{
		"schema_version": "opencode-runtime-smoke/v1alpha1", "runtime_version": "1.18.11",
		"native_events_captured": 1, "native_session_event_type": "session.created",
		"native_session_id_sha256": hash64, "native_session_event_sha256": hash64,
		"evidence_events_appended": 1, "evidence_ready": true, "provider_model_invoked": false,
	}
	instances := []struct {
		schema string
		value  any
	}{
		{"quickstart-smoke.schema.json", quickstart},
		{"opencode-runtime-smoke.schema.json", opencode},
		{"agentmem-candidate-acceptance.schema.json", windows},
		{"agentmem-candidate-acceptance.schema.json", macos},
		{"agentmem-required-checks.schema.json", requiredChecks},
		{"agentmem-release-provenance.schema.json", provenance},
	}
	for index, instance := range instances {
		t.Run(instance.schema+string(rune('a'+index)), func(t *testing.T) {
			validatePublishedInstance(t, instance.schema, instance.value)
		})
	}
	wrong := cloneMap(t, provenance)
	wrong["commit"] = strings.Repeat("c", 39)
	rejectPublishedInstance(t, "agentmem-release-provenance.schema.json", wrong)
}

func TestTrackedFixtureAndAllowlistMatchPublishedSchemas(t *testing.T) {
	for _, item := range []struct {
		path   string
		schema string
	}{
		{"../examples/quickstart/manifest.json", "quickstart-fixture.schema.json"},
		{"../examples/public-tree-allowlist.json", "public-tree-allowlist.schema.json"},
	} {
		data, err := os.ReadFile(filepath.Clean(item.path))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		validatePublishedInstance(t, item.schema, value)
	}
}

func cloneMap(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
