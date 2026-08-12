package schemas_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/codexbench"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENTMEM_SCHEMA_CODEXBENCH_HELPER") == "1" {
		runSchemaCodexBenchmarkHelper()
		return
	}
	os.Exit(m.Run())
}

func runSchemaCodexBenchmarkHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-schema-test 1.0")
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "oracle" {
		message, _ := os.ReadFile(".agentmem-eval/agent-message.txt")
		if strings.TrimSpace(string(message)) == "remembered" {
			fmt.Println("accepted")
			os.Exit(0)
		}
		fmt.Println("rejected")
		os.Exit(1)
	}
	prompt := os.Args[len(os.Args)-1]
	message := "unknown"
	if strings.Contains(prompt, "<project_memory>") {
		message = "remembered"
	}
	fmt.Println(`{"type":"thread.started","thread_id":"schema-benchmark-thread"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"id\":\"item_0\",\"type\":\"agent_message\",\"text\":%q}}\n", message)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
	os.Exit(0)
}

func TestCodexBenchmarkProducerInstancesMatchPublishedSchemas(t *testing.T) {
	t.Setenv("AGENTMEM_SCHEMA_CODEXBENCH_HELPER", "1")
	root := t.TempDir()
	workspace, overlay := filepath.Join(root, "workspace"), filepath.Join(root, "oracle-overlay")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("synthetic fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "criteria.txt"), []byte("exact agent message\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plan := codexbench.Plan{SchemaVersion: codexbench.PlanSchema, SuiteID: "schema-benchmark", Model: "default",
		TimeoutSeconds: 60, Privacy: "local_only", Tasks: []codexbench.Task{{TaskID: "schema-task", ClusterID: "schema-cluster",
			Prompt: "Return the stored answer.", ToolPolicy: "forbid", MemoryContext: "The stored answer is remembered.", Workspace: "workspace",
			OracleOverlay: "oracle-overlay", OracleCommand: []string{executable, "oracle"}}}}
	validatePublishedInstance(t, "codex-memory-benchmark-plan.schema.json", plan)
	planData, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(root, "plan.json")
	if err := os.WriteFile(planPath, planData, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "results")
	report, err := codexbench.Run(context.Background(), store, codexbench.Options{
		PlanPath: planPath, CodexPath: executable, OutputRoot: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealedData, err := os.ReadFile(filepath.Join(output, "sealed-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sealed codexbench.SealedPlan
	if err := json.Unmarshal(sealedData, &sealed); err != nil {
		t.Fatal(err)
	}
	validatePublishedInstance(t, "codex-memory-benchmark-sealed-plan.schema.json", sealed)
	validatePublishedInstance(t, "codex-memory-benchmark-report.schema.json", report)
	verification := codexbench.Verify(store, filepath.Join(output, "report.json"))
	if len(verification.Issues) != 0 {
		t.Fatalf("producer verification failed: %+v", verification)
	}
	validatePublishedInstance(t, "codex-memory-benchmark-verification.schema.json", verification)
	reportData, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var falseClaim map[string]any
	if err := json.Unmarshal(reportData, &falseClaim); err != nil {
		t.Fatal(err)
	}
	falseClaim["efficacy_claim"] = "observed_benefit_on_sealed_suite_not_longitudinal_certification"
	rejectPublishedInstance(t, "codex-memory-benchmark-report.schema.json", falseClaim)

	var mixedClaim map[string]any
	if err := json.Unmarshal(reportData, &mixedClaim); err != nil {
		t.Fatal(err)
	}
	mixedClaim["efficacy_claim"] = "observed_benefit_on_sealed_suite_not_longitudinal_certification"
	mixedClaim["issues"] = []any{}
	originalPair := mixedClaim["pairs"].([]any)[0].(map[string]any)
	pairs := make([]any, 20)
	for index := range pairs {
		copy := map[string]any{}
		for key, value := range originalPair {
			copy[key] = value
		}
		copy["memory_source"] = "verified_injection"
		pairs[index] = copy
	}
	pairs[0].(map[string]any)["memory_source"] = "caller_provided"
	mixedClaim["pairs"] = pairs

	invalid := map[string]any{
		"schema_version": codexbench.PlanSchema, "suite_id": "invalid", "model": "default",
		"timeout_seconds": 60, "privacy": "local_only", "tasks": []any{map[string]any{
			"task_id": "task", "cluster_id": "cluster", "prompt": "task", "tool_policy": "forbid", "memory_context": "memory",
			"injection_id": "injection-invalid", "workspace": "workspace", "oracle_overlay": "oracle",
			"oracle_command": []string{"oracle"},
		}},
	}
	rejectPublishedInstance(t, "codex-memory-benchmark-plan.schema.json", invalid)
}
