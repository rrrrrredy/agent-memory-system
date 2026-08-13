package schemas_test

import (
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/compatibility"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestUserWorkflowInstancesMatchPublishedSchemas(t *testing.T) {
	compatibilityReport := compatibility.Report{
		SchemaVersion: compatibility.SchemaVersion,
		GeneratedAt:   time.Unix(2000, 0).UTC(),
		Ready:         false,
		Agents: []compatibility.AgentReport{{
			Agent: ledger.AgentCodex, Command: "codex",
			RuntimeStatus: compatibility.RuntimeNotFound, RuntimeIssue: "command_not_found",
			CaptureModes: []string{"rollout_import"}, RetrievalModes: []string{"cli_injection", "mcp"},
			NativeExecutionVerified:       false,
			ExecutionEvidence:             "not_verified",
			ProviderIndependentlyAttested: false,
			Limitations:                   []string{"Provider-hidden reasoning cannot be recovered."},
		}},
		Privacy: "local_only",
	}
	candidateList := candidates.ListResult{
		SchemaVersion: candidates.ListSchemaVersion,
		Generation:    "candidates-v1alpha1-example",
		Candidates:    []candidates.ListItem{},
		Privacy:       "local_only",
	}
	validatePublishedInstance(t, "agent-compatibility-report.schema.json", compatibilityReport)
	validatePublishedInstance(t, "candidate-list.schema.json", candidateList)

	invalidCompatibility := map[string]any{
		"schema_version": compatibility.SchemaVersion,
		"generated_at":   time.Unix(2000, 0).UTC().Format(time.RFC3339),
		"ready":          true,
		"agents": []any{map[string]any{
			"agent": "codex", "command": "codex", "runtime_status": "available",
			"runtime_issue": "command_not_found", "version": "codex 1.0",
			"capture_modes": []string{"rollout_import"}, "retrieval_modes": []string{"mcp"},
			"native_execution_verified": false, "limitations": []string{"limited"},
			"execution_evidence":              "not_verified",
			"provider_independently_attested": false,
		}},
		"privacy": "local_only",
	}

	contradictoryCompatibility := map[string]any{
		"schema_version": compatibility.SchemaVersion,
		"generated_at":   time.Unix(2000, 0).UTC().Format(time.RFC3339),
		"ready":          true,
		"agents": []any{map[string]any{
			"agent": "codex", "command": "codex", "runtime_status": "available", "version": "codex 1.0",
			"history_import_available": true, "capture_modes": []string{"rollout_import"}, "retrieval_modes": []string{"mcp"},
			"native_execution_verified": false, "execution_evidence": "local_replayable_receipt",
			"provider_independently_attested": false, "limitations": []string{"limited"},
		}},
		"privacy": "local_only",
	}
	rejectPublishedInstance(t, "agent-compatibility-report.schema.json", contradictoryCompatibility)
	rejectPublishedInstance(t, "agent-compatibility-report.schema.json", invalidCompatibility)

	invalidCandidateList := map[string]any{
		"schema_version": candidates.ListSchemaVersion,
		"generation":     "candidates-v1alpha1-example",
		"total":          1,
		"matching":       1,
		"returned":       1,
		"truncated":      false,
		"candidates": []any{map[string]any{
			"candidate": map[string]any{},
		}},
		"privacy": "local_only",
	}
	rejectPublishedInstance(t, "candidate-list.schema.json", invalidCandidateList)
}
