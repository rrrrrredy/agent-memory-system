package codexbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestMain(m *testing.M) {
	if os.Getenv("CODEXBENCH_TEST_HELPER") == "1" {
		runTestHelper()
		return
	}
	os.Exit(m.Run())
}

func runTestHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-test 1.0")
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "oracle" {
		if _, err := os.Stat("solution.txt"); err != nil {
			fmt.Println("solution missing")
			os.Exit(1)
		}
		fmt.Println("solution verified")
		os.Exit(0)
	}
	workspace := ""
	for index, value := range os.Args {
		if value == "--cd" && index+1 < len(os.Args) {
			workspace = os.Args[index+1]
		}
	}
	prompt := os.Args[len(os.Args)-1]
	if strings.Contains(prompt, "<project_memory>") {
		_ = os.WriteFile(filepath.Join(workspace, "solution.txt"), []byte("remembered\n"), 0o600)
	}
	fmt.Println(`{"type":"thread.started","thread_id":"benchmark-thread"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"done"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":50,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":2}}`)
	os.Exit(0)
}

func TestRunSealsPlanBeforePairedExecutionAndKeepsRawEvidenceLocal(t *testing.T) {
	t.Setenv("CODEXBENCH_TEST_HELPER", "1")
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	oracle := filepath.Join(root, "oracle")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oracle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("starter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oracle, "oracle.txt"), []byte("bound oracle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{SchemaVersion: PlanSchema, SuiteID: "paired-smoke", Model: "default",
		TimeoutSeconds: 60, Privacy: "local_only", Tasks: []Task{{TaskID: "memory-required", ClusterID: "fixture-project",
			Prompt: "Complete the project task.", ToolPolicy: "allow", MemoryContext: "Create solution.txt.",
			Workspace: "workspace", OracleOverlay: "oracle", OracleCommand: []string{executable, "oracle"}}}}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(root, "plan.json")
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ledger.Init(filepath.Join(root, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "results")
	report, err := Run(context.Background(), store, Options{PlanPath: planPath,
		CodexPath: executable, OutputRoot: output})
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Pairs != 1 || report.Summary.Wins != 1 || report.Summary.Losses != 0 ||
		report.Pairs[0].Baseline.OraclePassed || !report.Pairs[0].Treatment.OraclePassed {
		t.Fatalf("unexpected paired result: %+v", report.Summary)
	}
	if report.EfficacyClaim != "measurement_only" || report.PlanBlob.Bytes == 0 ||
		report.InputPlanBlob.Bytes == 0 || report.Summary.OneSidedSignTestP != 0.5 ||
		report.Pairs[0].Baseline.RawEventsBlob.Bytes == 0 || report.Pairs[0].Treatment.RawEventsBlob.Bytes == 0 {
		t.Fatalf("benchmark evidence boundary is incomplete: %+v", report)
	}
	sealedData, err := os.ReadFile(filepath.Join(output, "sealed-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sealed SealedPlan
	if err := decodeStrict(sealedData, &sealed); err != nil {
		t.Fatal(err)
	}
	if sealed.InputPlanBlob.Bytes == 0 || sealed.CodexExecutable.Blob.Bytes == 0 || sealed.RunnerExecutable.Blob.Bytes == 0 ||
		len(sealed.Tasks) != 1 || len(sealed.Tasks[0].WorkspaceFiles) != 1 ||
		sealed.Tasks[0].WorkspaceFiles[0].Blob.Bytes == 0 || sealed.Tasks[0].OracleExecutable.Blob.Bytes == 0 ||
		report.Summary.VerifiedRetrievalPairs != 0 || report.Pairs[0].MemorySource != "caller_provided" {
		t.Fatalf("sealed benchmark artifacts or memory source are incomplete: sealed=%+v report=%+v", sealed, report.Summary)
	}
	verification := Verify(store, filepath.Join(output, "report.json"))
	suitePath := filepath.Join(root, "suite.json")
	if err := os.WriteFile(suitePath, []byte(`{"schema_version":"codex-memory-benchmark-suite/v1alpha1","suite_id":"paired-smoke","tool_policy":"forbid","privacy":"synthetic_public"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildPublicReceipt(store, filepath.Join(output, "report.json"), suitePath); err == nil {
		t.Fatal("measurement-only report produced a public observed-benefit receipt")
	}
	if len(verification.Issues) != 0 || verification.ReportSHA256 != report.ReportSHA256 ||
		verification.PlanSHA256 != report.PlanSHA256 || verification.RecordsChecked == 0 || verification.BlobsChecked == 0 {
		t.Fatalf("benchmark verification failed: %+v", verification)
	}
	tamperedPath := filepath.Join(root, "tampered-report.json")
	var tampered Report
	reportData, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil || decodeStrict(reportData, &tampered) != nil {
		t.Fatal("read generated report")
	}
	tampered.Pairs[0].Treatment.ToolCalls++
	tampered.ReportSHA256, err = reportSHA256(tampered)
	if err != nil {
		t.Fatal(err)
	}
	tamperedData, err := marshalIndented(tampered)
	if err != nil || os.WriteFile(tamperedPath, tamperedData, 0o600) != nil {
		t.Fatal("write tampered report")
	}
	if rejected := Verify(store, tamperedPath); len(rejected.Issues) == 0 {
		t.Fatal("benchmark verification accepted a tampered report with a recomputed self-hash")
	}
	rawReference := report.Pairs[0].Baseline.RawEventsBlob
	rawPath := filepath.Join(root, "evidence", filepath.FromSlash(rawReference.RelativePath))
	if err := os.WriteFile(rawPath, []byte("tampered nested evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	rejectedNested := Verify(store, filepath.Join(output, "report.json"))
	if !strings.Contains(strings.Join(rejectedNested.Issues, "\n"), "raw events blob is invalid") {
		t.Fatalf("benchmark verification accepted a tampered nested blob: %+v", rejectedNested)
	}

	ledgerVerification := store.Verify()
	if len(ledgerVerification.Issues) != 0 {
		t.Fatalf("benchmark evidence ledger failed verification: %+v", ledgerVerification.Issues)
	}
}

func TestPlanRejectsUnsealedOraclePathArguments(t *testing.T) {
	plan := Plan{SchemaVersion: PlanSchema, SuiteID: "sealed-arguments", Model: "default",
		TimeoutSeconds: 60, Privacy: "local_only", Tasks: []Task{{TaskID: "task", ClusterID: "cluster",
			Prompt: "Do the task.", ToolPolicy: "forbid", MemoryContext: "Use the reviewed memory.", Workspace: "workspace",
			OracleOverlay: "oracle", OracleCommand: []string{"oracle", "../mutable/criteria.json"}}}}
	if err := validatePlan(plan); err == nil {
		t.Fatal("plan accepted an oracle argument that references an unsealed file")
	}
}

func TestObservedBenefitRequiresIndependentClustersAndStatisticalEvidence(t *testing.T) {
	pairs := make([]PairResult, 20)
	for index := range pairs {
		pairs[index] = PairResult{TaskID: fmt.Sprintf("task-%d", index), ClusterID: fmt.Sprintf("cluster-%d", index),
			MemorySource: "verified_injection", ToolPolicy: "forbid", Outcome: "win",
			Treatment: ArmResult{OraclePassed: true}}
	}
	summary := summarize(pairs)
	if summary.DistinctClusters != 20 || summary.VerifiedRetrievalPairs != 20 ||
		summary.DiscordantPairs != 20 || summary.OneSidedSignTestP > 0.05 {
		t.Fatalf("unexpected sign-test summary: %+v", summary)
	}
}

func TestObservedBenefitRejectsAnyCallerProvidedMemory(t *testing.T) {
	pairs := make([]PairResult, 20)
	for index := range pairs {
		pairs[index] = PairResult{TaskID: fmt.Sprintf("task-%d", index), ClusterID: fmt.Sprintf("cluster-%d", index),
			ToolPolicy:   "forbid",
			MemorySource: "verified_injection", Baseline: ArmResult{OraclePassed: false},
			Treatment: ArmResult{OraclePassed: true}, Outcome: "win"}
	}
	pairs[0].MemorySource = "caller_provided"
	report := Report{Pairs: pairs, Issues: []string{}}
	classifyReport(&report)
	if report.EfficacyClaim != "measurement_only" || report.Summary.VerifiedRetrievalPairs != 19 {
		t.Fatalf("caller-provided memory escaped the efficacy gate: %+v", report)
	}
	if len(report.Issues) != 1 || !strings.Contains(report.Issues[0], "verified retrieval injections for every pair") {
		t.Fatalf("missing caller-provided memory issue: %#v", report.Issues)
	}
}

func TestParserCountsToolItemsOnceAcrossStartedAndCompletedEvents(t *testing.T) {
	data := []byte(strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread"}`,
		`{"type":"item.started","item":{"id":"command-1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"command-1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning"}}`,
		`{"type":"item.completed","item":{"id":"message-1","type":"agent_message","text":"answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`,
	}, "\n"))
	parsed, err := parseExecutionJSONL(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ToolCalls != 1 {
		t.Fatalf("unexpected tool-call count: %+v", parsed)
	}
}

func TestObservedBenefitRejectsToolUse(t *testing.T) {
	pairs := make([]PairResult, 20)
	for index := range pairs {
		pairs[index] = PairResult{TaskID: fmt.Sprintf("task-%d", index), ClusterID: fmt.Sprintf("cluster-%d", index),
			MemorySource: "verified_injection", ToolPolicy: "forbid", Outcome: "win",
			Treatment: ArmResult{OraclePassed: true}}
	}
	pairs[0].Baseline.ToolCalls = 1
	report := Report{Pairs: pairs, Issues: []string{}}
	classifyReport(&report)
	if report.EfficacyClaim != "measurement_only" || report.Summary.ToolFreePairs != 19 ||
		len(report.Issues) != 1 || !strings.Contains(report.Issues[0], "zero tool calls") {
		t.Fatalf("tool use escaped the claim gate: %+v", report)
	}
}
