package evaluation

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestSealedTrialPlanRequiresSupervisedExecutionForEveryArm(t *testing.T) {
	t.Run("both arms supervised", func(t *testing.T) {
		store, registryPath, portableRoot, plan := executionTrialFixture(t)
		for _, condition := range plan.Plan.Pairs[0].ExecutionOrder {
			preregistration := plan.Attempts[0]
			if preregistration.Request.Condition != condition {
				preregistration = plan.Attempts[1]
			}
			executed, err := ExecutePlannedTaskAttempt(store, preregistration.Request,
				ExecutePlannedAttemptOptions{PortableRoot: portableRoot})
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateTaskExecutionResult(store, executed); err != nil {
				t.Fatalf("supervised execution result did not fully replay: %v", err)
			}
			finalized, err := FinalizeBuiltinTaskAttempt(store, executed.Request, registryPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			if verification := VerifyTaskAttempt(store, finalized.Receipt.ReceiptID); len(verification.Issues) != 0 {
				t.Fatalf("supervised task attempt failed verification: %+v", verification)
			}
		}
		assertExecutionPopulation(t, store, registryPath, plan.Plan.CorpusID, true)
	})

	t.Run("manual baseline result", func(t *testing.T) {
		store, registryPath, portableRoot, plan := executionTrialFixture(t)
		baseline, treatment := plan.Attempts[0].Request, plan.Attempts[1].Request
		if baseline.Condition != TaskConditionBaseline {
			baseline, treatment = treatment, baseline
		}
		if _, err := ObserveTaskAttemptResult(store, baseline, []byte("acceptance check failed"),
			"text/plain", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := FinalizeBuiltinTaskAttempt(store, baseline, registryPath, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := ExecutePlannedTaskAttempt(store, treatment,
			ExecutePlannedAttemptOptions{PortableRoot: portableRoot}); err == nil {
			t.Fatal("manual completion of the preceding arm satisfied the sealed execution order")
		}
		assertExecutionPopulation(t, store, registryPath, plan.Plan.CorpusID, false)
	})

	t.Run("second arm cannot execute first", func(t *testing.T) {
		store, _, portableRoot, plan := executionTrialFixture(t)
		secondCondition := plan.Plan.Pairs[0].ExecutionOrder[1]
		second := plan.Attempts[0].Request
		if second.Condition != secondCondition {
			second = plan.Attempts[1].Request
		}
		if _, err := ExecutePlannedTaskAttempt(store, second,
			ExecutePlannedAttemptOptions{PortableRoot: portableRoot}); err == nil {
			t.Fatal("the second sealed arm executed before the first")
		}
	})
}

func TestExecutionFailureCreatesOneTerminalResultAndCannotBeRetried(t *testing.T) {
	store, _, portableRoot, plan := executionTrialFixtureWithAdapterMode(t, "execution-supervisor-empty")
	firstCondition := plan.Plan.Pairs[0].ExecutionOrder[0]
	first := plan.Attempts[0].Request
	if first.Condition != firstCondition {
		first = plan.Attempts[1].Request
	}
	executed, err := ExecutePlannedTaskAttempt(store, first,
		ExecutePlannedAttemptOptions{PortableRoot: portableRoot})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Execution.ExitCode != 125 {
		t.Fatalf("empty output exit code = %d, want 125", executed.Execution.ExitCode)
	}
	data, err := readAttemptBlob(store, executed.Execution.ResultBlob)
	if err != nil {
		t.Fatal(err)
	}
	var failure TaskExecutionFailure
	if err := decodeStrictEvaluationJSON(data, &failure); err != nil ||
		failure.SchemaVersion != TaskExecutionFailureSchema || failure.Kind != "empty_output" {
		t.Fatalf("terminal failure result is invalid: failure=%+v err=%v", failure, err)
	}
	if _, err := ExecutePlannedTaskAttempt(store, first,
		ExecutePlannedAttemptOptions{PortableRoot: portableRoot}); err == nil {
		t.Fatal("a started failed arm was retried")
	}
}

func TestNonzeroExecutionWithStdoutIsAFailedTerminal(t *testing.T) {
	store, _, portableRoot, plan := executionTrialFixtureWithAdapterMode(t, "execution-supervisor-nonzero")
	firstCondition := plan.Plan.Pairs[0].ExecutionOrder[0]
	first := plan.Attempts[0].Request
	if first.Condition != firstCondition {
		first = plan.Attempts[1].Request
	}
	executed, err := ExecutePlannedTaskAttempt(store, first,
		ExecutePlannedAttemptOptions{PortableRoot: portableRoot})
	if err != nil {
		t.Fatal(err)
	}
	if executed.Execution.Outcome != TaskExecutionFailed || executed.Execution.ExitCode != 2 ||
		executed.Execution.FailureKind != "process_exit" || executed.Execution.StdoutBlob == nil ||
		executed.Execution.ResultMediaType != TaskExecutionFailureMediaType {
		t.Fatalf("nonzero execution was not a canonical failed terminal: %+v", executed.Execution)
	}
	if err := ValidateTaskExecutionResult(store, executed); err != nil {
		t.Fatalf("failed terminal envelope did not replay: %v", err)
	}
}

func executionTrialFixture(t *testing.T) (*ledger.Store, string, string, TrialPlanPreregistration) {
	return executionTrialFixtureWithAdapterMode(t, "execution-supervisor-adapter")
}

func executionTrialFixtureWithAdapterMode(t *testing.T, adapterMode string) (*ledger.Store, string, string, TrialPlanPreregistration) {
	t.Helper()
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	memoryText := "Please remember that generated reports stay local"
	seed, _ := json.Marshal(map[string]string{"message": memoryText})
	appendPopulationEvent(t, store, "execution-memory-seed", ledger.KindUserMessage, ledger.AgentCodex,
		"capture", "capture/v1", "execution-seed", string(seed), time.Now().UTC())
	portableRoot, _, semanticKey := makePopulationPortableRepository(t, store, memoryText)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var corpusID string
	var artifact CorpusArtifact
	var taskSpec []byte
	var pairID, taskID string
	var assignedAgent ledger.Agent
	for index := 0; index < 128; index++ {
		corpusID = freezeTrialCorpusWithContent(t, store,
			[]byte(fmt.Sprintf("generated reports local %d\n", index)))
		artifact, taskSpec, pairID, taskID, assignedAgent = frozenTrialArtifactInput(t, store, corpusID)
		manifest, loadErr := LoadCorpusManifest(store, corpusID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if assignmentDigest(trialCorpusAssignmentSHA256(manifest), pairID)[0]&1 == 0 {
			break
		}
	}
	if corpusID == "" || assignmentDigest(trialCorpusAssignmentSHA256(mustLoadTrialCorpus(t, store, corpusID)), pairID)[0]&1 != 0 {
		t.Fatal("test fixture could not construct a baseline-first fixed-policy corpus")
	}
	adapter, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	harness := ExecutionHarnessManifest{SchemaVersion: ExecutionHarnessManifestSchema,
		Arguments:      []string{"-test.run=^TestExecutionSupervisorAdapterHelper$", "--", adapterMode},
		TimeoutSeconds: 30, InheritEnvironment: true, RetrievalLimit: 3,
		RetrievalTokenBudget: 512, RetrievalByteBudget: 2048,
		ResultMediaType: "text/plain", Privacy: "local_only"}
	if runtime.GOOS == "windows" {
		harness.ExecutableSuffix = ".exe"
	}
	harnessData, _ := json.Marshal(harness)
	manifest, err := BindSystemUnderTest(store, ledger.AgentCodex, "test-provider", "test-model",
		SystemUnderTestArtifacts{SystemPrompt: []byte("Use the bound task protocol."),
			ToolRegistry: []byte(`{"tools":["report"]}`), Harness: harnessData, Adapter: adapter})
	if err != nil {
		t.Fatal(err)
	}
	manifestData, _ := json.Marshal(manifest)
	failPayload, passPayload := []byte("acceptance check failed"), []byte("acceptance check passed")
	criteria := builtinEvidenceScoreCriteria{SchemaVersion: builtinEvidenceScoreCriteriaSchema,
		AllowedEventOrders: [][]builtinEventExpectation{
			{{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(failPayload)}},
			{{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(passPayload)}},
		},
		Results: []builtinResultExpectation{
			{PayloadSHA256: sha256Hex(failPayload), Label: ResultLabelError},
			{PayloadSHA256: sha256Hex(passPayload), Label: ResultLabelSuccess},
		}, Users: []builtinUserExpectation{}, PassThreshold: 0.75}
	sort.Slice(criteria.AllowedEventOrders, func(i, j int) bool {
		return criteria.AllowedEventOrders[i][0].PayloadSHA256 < criteria.AllowedEventOrders[j][0].PayloadSHA256
	})
	sort.Slice(criteria.Results, func(i, j int) bool { return criteria.Results[i].PayloadSHA256 < criteria.Results[j].PayloadSHA256 })
	criteriaData, _ := json.Marshal(criteria)
	entry := OracleRegistryEntry{Kind: "builtin", ID: "evidence-score", Version: "v1", Arguments: []string{}}
	registryPath := writeOracleRegistry(t, entry)
	plan, err := PreregisterTrialPlan(store, TrialPlanPreregisterOptions{SuiteID: "supervised-suite",
		CorpusID: corpusID, OracleRegistryPath: registryPath,
		Pairs: []TrialPlanPairInput{{PairID: pairID, TaskID: taskID, Agent: assignedAgent,
			CorpusArtifactID: artifact.ArtifactID, SemanticKeySHA256: semanticKey, BaselineThreadID: "baseline-thread",
			BaselineSessionID: "baseline-session", TreatmentThreadID: "treatment-thread",
			TreatmentSessionID: "treatment-session", TaskSpec: taskSpec,
			AcceptanceCriteria: criteriaData, ExecutionConfig: []byte(`{"temperature":0}`),
			SystemUnderTest: manifestData}}})
	if err != nil {
		t.Fatal(err)
	}
	return store, registryPath, portableRoot, plan
}

func assertExecutionPopulation(t *testing.T, store *ledger.Store, registryPath, corpusID string,
	expected bool) {
	t.Helper()
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := loadOracleRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	systemSHA, err := CurrentSystemArtifactSHA256()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		t.Fatal(err)
	}
	population := EvaluationPopulation{SystemArtifactSHA256: systemSHA, CorpusID: corpusID,
		CorpusContentSHA256:   manifest.CorpusContentSHA256,
		SystemUnderTestSHA256: map[ledger.Agent]string{}, PopulationIssues: []string{}}
	attempts := loadPopulationAttempts(store, records, ordered, registry, &population)
	observed := validatePopulationExecutionReceipts(store, records, ordered, attempts, &population)
	if !population.Prerequisites.PairedTrialPlanSealed ||
		!population.Prerequisites.PreregisteredAttemptUniverse || observed != expected ||
		(expected && len(attempts) != 2) || (!expected && len(population.PopulationIssues) == 0) {
		t.Fatalf("unexpected supervised population: expected=%v observed=%v attempts=%d population=%+v",
			expected, observed, len(attempts), population)
	}
}

func TestExecutionSupervisorAdapterHelper(t *testing.T) {
	mode := ""
	for _, argument := range os.Args {
		if argument == "execution-supervisor-adapter" || argument == "execution-supervisor-empty" ||
			argument == "execution-supervisor-nonzero" {
			mode = argument
		}
	}
	if mode == "" {
		return
	}
	if mode == "execution-supervisor-empty" {
		os.Exit(0)
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var input AgentExecutionInput
	if json.Unmarshal(data, &input) != nil || input.SchemaVersion != AgentExecutionInputSchema ||
		len(input.SystemPrompt) == 0 || len(input.ToolRegistry) == 0 || len(input.TaskSpec) == 0 {
		os.Exit(2)
	}
	output := []byte("acceptance check failed")
	if input.MemoryContext != "" && len(input.MemoryReferences) != 0 {
		output = []byte("acceptance check passed")
	}
	_, _ = os.Stdout.Write(output)
	if mode == "execution-supervisor-nonzero" {
		os.Exit(2)
	}
	os.Exit(0)
}

func freezeMinimalTrialCorpus(t *testing.T, store *ledger.Store) string {
	t.Helper()
	return freezeTrialCorpusWithContent(t, store, []byte("generated reports local\n"))
}

func freezeTrialCorpusWithContent(t *testing.T, store *ledger.Store, content []byte) string {
	t.Helper()
	legacyRoot := t.TempDir()
	cardPath := filepath.Join(legacyRoot, "summaries", "trial-card.md")
	writeTestFile(t, cardPath, content)
	entry := legacyIndexEntry{CardPath: cardPath, SessionID: "trial-corpus-session"}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(legacyRoot, "data", "index.jsonl"), append(line, '\n'))
	frozen, err := FreezeLegacyCorpus(store, legacyRoot, FreezeOptions{
		Name: "trial-corpus", Now: func() time.Time { return time.Unix(8_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return frozen.CorpusID
}

func mustLoadTrialCorpus(t *testing.T, store *ledger.Store, corpusID string) CorpusManifest {
	t.Helper()
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func frozenTrialArtifactInput(t *testing.T, store *ledger.Store, corpusID string) (CorpusArtifact, []byte, string, string, ledger.Agent) {
	t.Helper()
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		t.Fatal(err)
	}
	seedSHA := trialCorpusAssignmentSHA256(manifest)
	selected := selectedTrialCorpusArtifacts(manifest, seedSHA)
	if len(selected) != 1 {
		t.Fatalf("minimal trial corpus selected %d artifacts, want 1", len(selected))
	}
	artifact := selected[0].Artifact
	data, err := readAttemptBlob(store, artifact.Blob)
	if err != nil {
		t.Fatal(err)
	}
	return artifact, data,
		corpusTrialPairID(corpusID, seedSHA, artifact),
		corpusTrialTaskID(corpusID, artifact), selected[0].Agent
}
