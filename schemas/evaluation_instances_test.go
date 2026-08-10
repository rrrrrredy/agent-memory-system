package schemas_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func TestEvaluationInstancesMatchPublishedSchemas(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(700, 0).UTC()
	payload := ledger.InlinePayload("utf-8", "text/plain", "source bytes")
	source, err := store.Append(ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: "source-schema-test",
		Kind: ledger.KindSourceSnapshot, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "schema-test", AdapterVersion: "schema-test/v1",
			DeviceID: store.DeviceID(), ThreadID: "thread-schema-test",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	minimum := 1.0
	input := evaluation.EvaluationInput{
		SchemaVersion: evaluation.EvaluationInputSchemaVersion, SuiteID: "schema-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "schema-test/v1", Privacy: "local_only",
		QualityProfile: evaluation.QualityProfileComponent,
		Thresholds:     evaluation.EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []evaluation.EvaluationCase{{
			CaseID: "capture", Category: evaluation.CategoryCaptureCoverage, Agent: ledger.AgentCodex,
			Evidence: []evaluation.EvidenceReference{{
				Kind: "ledger_event", ID: source.Event.EventID, SHA256: source.RecordHash,
			}},
			Capture: &evaluation.CaptureMeasurement{
				Unit: evaluation.CaptureUnitEvidenceEvents, Expected: 1, Complete: 1,
			},
		}},
	}
	run, err := evaluation.Run(store, input,
		evaluation.RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	verification := evaluation.VerifyRun(store, input.SuiteID, input.RunID)

	attestation := evaluation.EvaluationAttestation{
		SchemaVersion: evaluation.EvaluationAttestationSchema,
		AttestationID: "schema-attestation", CaseID: "correction",
		Category: evaluation.CategoryRepeatedCorrection, Agent: ledger.AgentCodex,
		Attestor: evaluation.Attestor{Kind: "human", ID: "owner"}, AttestedAt: now,
		Reason: "The correction grouping was reviewed.",
		Correction: &evaluation.CorrectionMeasurement{
			SemanticKeySHA256:             strings.Repeat("a", 64),
			InitialCorrectionAttemptID:    "task-attempt-" + strings.Repeat("c", 64),
			AttemptIDs:                    []string{"task-attempt-" + strings.Repeat("b", 64)},
			EligibleFollowupOpportunities: 1,
		},
	}
	attestationResult, err := evaluation.RecordAttestation(store, attestation,
		func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	systemArtifactSHA256, err := evaluation.CurrentSystemArtifactSHA256()
	if err != nil {
		t.Fatal(err)
	}
	attemptRequest := evaluation.TaskAttemptRequest{
		SchemaVersion: evaluation.TaskAttemptRequestSchema, TaskID: "schema-task",
		AttemptID: "schema-attempt", Agent: ledger.AgentCodex,
		SemanticKeySHA256: strings.Repeat("b", 64), TaskSpecSHA256: strings.Repeat("c", 64),
		AcceptanceCriteriaSHA256: strings.Repeat("d", 64), ExecutionConfigSHA256: strings.Repeat("e", 64),
		SystemArtifactSHA256: systemArtifactSHA256,
		Condition:            evaluation.TaskConditionBaseline, WindowStartEventID: "schema-attempt-start",
		WindowEndEventID: "schema-attempt-result", MemoryReferences: []retrieval.MemoryReference{},
		Oracle: evaluation.TaskOracle{Kind: "harness", ID: "schema-attempt", Version: "schema-attempt/v1",
			VerdictEventID: "schema-attempt-verdict"}, Privacy: "local_only",
	}
	appendAttemptSchemaEvent := func(id string, kind ledger.EventKind, content string, at time.Time,
		parents ...string) {
		payload := ledger.InlinePayload("utf-8", "application/json", content)
		event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
			ObservedAt: at, RecordedAt: at,
			Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "schema-attempt",
				AdapterVersion: "schema-attempt/v1", DeviceID: store.DeviceID(),
				ThreadID: "schema-attempt", SessionID: "schema-attempt"},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Privacy: ledger.Privacy{Classification: "local_only"}}
		if len(parents) != 0 {
			event.Causality = &ledger.Causality{ParentEventIDs: parents}
		}
		if _, appendErr := store.Append(event); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	attemptContract := evaluation.NewTaskAttemptContract(attemptRequest)
	contractData, err := json.Marshal(attemptContract)
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptSchemaEvent(attemptRequest.WindowStartEventID, ledger.KindTaskAttemptContract,
		string(contractData), now.Add(3*time.Second))
	appendAttemptSchemaEvent(attemptRequest.WindowEndEventID, ledger.KindToolResult,
		"task passed", now.Add(4*time.Second), attemptRequest.WindowStartEventID)
	attemptVerdict := evaluation.TaskAttemptVerdict{
		SchemaVersion: evaluation.TaskAttemptVerdictSchema, TaskID: attemptRequest.TaskID,
		AttemptID: attemptRequest.AttemptID, Verdict: evaluation.TaskVerdictPass, Score: 1,
		TokenCountEvaluated: true, TotalTokens: 10,
		ResultEvents: []evaluation.LabeledResultEvent{{EventID: attemptRequest.WindowEndEventID,
			Label: evaluation.ResultLabelSuccess}}, UserMessages: []evaluation.LabeledUserMessage{},
		TaskSpecSHA256: attemptRequest.TaskSpecSHA256,
		CriteriaSHA256: attemptRequest.AcceptanceCriteriaSHA256,
		ConfigSHA256:   attemptRequest.ExecutionConfigSHA256, Privacy: "local_only",
	}
	verdictData, err := json.Marshal(attemptVerdict)
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptSchemaEvent(attemptRequest.Oracle.VerdictEventID, ledger.KindToolResult,
		string(verdictData), now.Add(5*time.Second), attemptRequest.WindowEndEventID)
	attemptResult, err := evaluation.RecordTaskAttempt(store, attemptRequest,
		func() time.Time { return now.Add(6 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	attemptVerification := evaluation.VerifyTaskAttempt(store, attemptResult.Receipt.ReceiptID)
	legacyRoot := t.TempDir()
	cardPath := filepath.Join(legacyRoot, "summaries", "card.md")
	if err := os.MkdirAll(filepath.Dir(cardPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cardPath, []byte("# synthetic card\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(legacyRoot, "data", "index.jsonl")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	indexRecord, err := json.Marshal(map[string]any{
		"card_path": cardPath, "session_id": "synthetic-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, append(indexRecord, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	corpusResult, err := evaluation.FreezeLegacyCorpus(store, legacyRoot,
		evaluation.FreezeOptions{Name: "schema-corpus", Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := evaluation.LoadCorpusManifest(store, corpusResult.CorpusID)
	if err != nil {
		t.Fatal(err)
	}
	corpusVerification := evaluation.VerifyCorpus(store, corpusResult.CorpusID)
	trialSelection, err := evaluation.SelectTrialCorpus(store, corpusResult.CorpusID)
	if err != nil {
		t.Fatal(err)
	}

	oracleRegistry := evaluation.OracleRegistry{
		SchemaVersion: evaluation.OracleRegistrySchema,
		Entries: []evaluation.OracleRegistryEntry{{
			Kind: "harness", ID: "schema-attempt", Version: "schema-attempt/v1",
			Executable:       "C:\\oracles\\schema-attempt.exe",
			ExecutableSHA256: strings.Repeat("f", 64), Arguments: []string{}, TimeoutSeconds: 30,
		}}, Privacy: "local_only",
	}
	oracleReplayInput := evaluation.OracleReplayInput{
		SchemaVersion: evaluation.OracleReplayInputSchema, TaskID: "schema-task",
		AttemptID: "schema-attempt", Condition: evaluation.TaskConditionBaseline,
		TaskSpec: []byte("task"), AcceptanceCriteria: []byte("criteria"),
		ExecutionConfig: []byte("config"),
		ResultEvents: []evaluation.OracleReplayEvent{{
			EventID: "schema-attempt-result", Kind: ledger.KindToolResult,
			Payload: []byte("task passed"), RecordHash: strings.Repeat("a", 64),
		}}, UserMessages: []evaluation.OracleReplayEvent{}, Privacy: "local_only",
	}
	oracleReplayInput.OrderedEvents = append([]evaluation.OracleReplayEvent{}, oracleReplayInput.ResultEvents...)
	manifestBlob := func(digest string) ledger.BlobRef {
		return ledger.BlobRef{SHA256: digest, Bytes: 1,
			RelativePath: path.Join("evidence", "blobs", "sha256", digest[:2], digest[2:])}
	}
	systemPromptSHA, toolRegistrySHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
	harnessSHA, adapterSHA := strings.Repeat("c", 64), strings.Repeat("d", 64)
	systemUnderTest := evaluation.SystemUnderTestManifest{
		SchemaVersion: evaluation.SystemUnderTestManifestSchema, Agent: ledger.AgentCodex,
		Provider: "schema-provider", Model: "schema-model",
		SystemPromptSHA256: systemPromptSHA, SystemPromptBlob: manifestBlob(systemPromptSHA),
		ToolRegistrySHA256: toolRegistrySHA, ToolRegistryBlob: manifestBlob(toolRegistrySHA),
		HarnessSHA256: harnessSHA, HarnessBlob: manifestBlob(harnessSHA),
		AdapterSHA256: adapterSHA, AdapterBlob: manifestBlob(adapterSHA),
		Privacy: "local_only",
	}
	preregisterRequest := attemptRequest
	preregisterRequest.TaskSpecBlob = blobPointer(manifestBlob(attemptRequest.TaskSpecSHA256))
	preregisterRequest.AcceptanceCriteriaBlob = blobPointer(manifestBlob(attemptRequest.AcceptanceCriteriaSHA256))
	preregisterRequest.ExecutionConfigBlob = blobPointer(manifestBlob(attemptRequest.ExecutionConfigSHA256))
	preregisterRequest.SystemUnderTestSHA256 = strings.Repeat("9", 64)
	preregisterRequest.SystemUnderTestBlob = blobPointer(manifestBlob(preregisterRequest.SystemUnderTestSHA256))
	preregisterRequest.Oracle = evaluation.TaskOracle{Kind: "builtin", ID: "evidence-score", Version: "v1",
		VerdictEventID: "schema-attempt-verdict", RegistryEntrySHA256: strings.Repeat("8", 64)}
	attemptDraft := evaluation.TaskAttemptDraft{SchemaVersion: evaluation.TaskAttemptDraftSchema,
		TaskID: attemptRequest.TaskID, AttemptID: attemptRequest.AttemptID, Agent: attemptRequest.Agent,
		SemanticKeySHA256: attemptRequest.SemanticKeySHA256, Condition: attemptRequest.Condition,
		Privacy: "local_only"}
	attemptPreregistration := evaluation.TaskAttemptPreregistration{
		SchemaVersion: evaluation.TaskAttemptPreregisterSchema, Request: preregisterRequest,
		EventID: preregisterRequest.WindowStartEventID, RecordHash: strings.Repeat("7", 64), Privacy: "local_only"}
	attemptObservation := evaluation.TaskAttemptObservationResult{
		SchemaVersion: evaluation.TaskAttemptObservationSchema, EventID: preregisterRequest.WindowEndEventID,
		RecordHash: strings.Repeat("6", 64), PayloadSHA256: strings.Repeat("5", 64),
		Reused: false, Privacy: "local_only"}
	builtinCriteria := map[string]any{
		"schema_version": "builtin-evidence-score-criteria/v1",
		"allowed_event_orders": []any{[]any{map[string]any{
			"kind": "tool_result", "payload_sha256": strings.Repeat("4", 64),
		}}},
		"results": []any{map[string]any{
			"payload_sha256": strings.Repeat("4", 64), "label": "success",
		}},
		"users": []any{}, "pass_threshold": 1.0,
	}
	portablePopulationSnapshot := map[string]any{
		"schema_version":      "portable-population-snapshot/v1alpha1",
		"active_revision_ids": []any{}, "revisions": []any{}, "privacy": "local_only",
	}
	captureEvaluationSnapshot := map[string]any{
		"schema_version": "capture-evaluation-snapshot/v1alpha1", "run_id": "capture-run",
		"config_sha256": strings.Repeat("3", 64), "audit_event_sha256": strings.Repeat("2", 64),
		"completed_at": now, "full_reconcile": true,
		"sources": []any{map[string]any{
			"source_id": "codex", "agent": "codex", "kind": "codex_rollouts",
			"source_config_sha256": strings.Repeat("1", 64),
			"inventory_sha256":     strings.Repeat("0", 64),
			"items": []any{map[string]any{
				"identity_sha256": strings.Repeat("f", 64), "type": "file", "status": "missing",
			}},
		}},
		"snapshot_sha256": strings.Repeat("e", 64), "privacy": "local_only",
	}

	planID, pairID := "trial-plan-schema", "trial-pair-schema"
	baselineRequest := preregisterRequest
	baselineRequest.AttemptID = "planned-baseline"
	baselineRequest.TrialPlanID, baselineRequest.TrialPairID = planID, pairID
	baselineRequest.Condition = evaluation.TaskConditionBaseline
	baselineRequest.WindowStartEventID = "planned-baseline-contract"
	baselineRequest.WindowEndEventID = "planned-baseline-result"
	baselineRequest.Oracle.VerdictEventID = "planned-baseline-verdict"
	baselineRequest.MemoryReferences = []retrieval.MemoryReference{}
	treatmentRequest := baselineRequest
	treatmentRequest.AttemptID = "planned-treatment"
	treatmentRequest.Condition = evaluation.TaskConditionMemory
	treatmentRequest.WindowStartEventID = "planned-treatment-contract"
	treatmentRequest.WindowEndEventID = "planned-treatment-result"
	treatmentRequest.Oracle.VerdictEventID = "planned-treatment-verdict"
	trialPlan := evaluation.EvaluationTrialPlan{
		SchemaVersion: evaluation.EvaluationTrialPlanSchema,
		PlanID:        planID, SuiteID: "schema-suite", CorpusID: corpusResult.CorpusID,
		CorpusContentSHA256: manifest.CorpusContentSHA256, SeedSHA256: strings.Repeat("1", 64),
		SystemArtifactSHA256: systemArtifactSHA256,
		OracleRegistrySHA256: strings.Repeat("2", 64), CreatedAt: now,
		Pairs: []evaluation.TrialPlanPair{{PairID: pairID,
			CorpusArtifactID: "schema-artifact", CorpusArtifactSHA256: strings.Repeat("0", 64),
			ExecutionOrder: []evaluation.TaskCondition{evaluation.TaskConditionBaseline, evaluation.TaskConditionMemory},
			Baseline: evaluation.TrialPlanArm{Condition: evaluation.TaskConditionBaseline,
				ThreadID: "baseline-thread", SessionID: "baseline-session",
				Contract: evaluation.NewTaskAttemptContract(baselineRequest)},
			Treatment: evaluation.TrialPlanArm{Condition: evaluation.TaskConditionMemory,
				ThreadID: "treatment-thread", SessionID: "treatment-session",
				Contract: evaluation.NewTaskAttemptContract(treatmentRequest)}}},
		Privacy: "local_only",
	}
	trialPreregistration := evaluation.TrialPlanPreregistration{
		SchemaVersion: evaluation.TrialPlanPreregistrationSchema, Plan: trialPlan,
		EventID: "trial-plan-event", RecordHash: strings.Repeat("3", 64),
		Attempts: []evaluation.TaskAttemptPreregistration{
			{SchemaVersion: evaluation.TaskAttemptPreregisterSchema, Request: baselineRequest,
				EventID: baselineRequest.WindowStartEventID, RecordHash: strings.Repeat("4", 64), Privacy: "local_only"},
			{SchemaVersion: evaluation.TaskAttemptPreregisterSchema, Request: treatmentRequest,
				EventID: treatmentRequest.WindowStartEventID, RecordHash: strings.Repeat("5", 64), Privacy: "local_only"},
		}, Privacy: "local_only",
	}
	harnessManifest := evaluation.ExecutionHarnessManifest{
		SchemaVersion: evaluation.ExecutionHarnessManifestSchema, Arguments: []string{"--json"},
		TimeoutSeconds: 30, InheritEnvironment: false, RetrievalLimit: 5,
		RetrievalTokenBudget: 800, RetrievalByteBudget: 4096,
		ResultMediaType: "application/json", Privacy: "local_only",
	}
	executionInput := evaluation.AgentExecutionInput{
		SchemaVersion: evaluation.AgentExecutionInputSchema, Challenge: strings.Repeat("6", 64),
		Agent:    ledger.AgentCodex,
		Provider: "schema-provider", Model: "schema-model", SystemPrompt: []byte("system"),
		ToolRegistry: []byte("tools"), ExecutionConfig: []byte("config"),
		TaskSpec:         []byte("task"),
		MemoryReferences: []retrieval.MemoryReference{}, Privacy: "local_only",
	}
	contractReference := evaluation.BoundEventReference{EventID: baselineRequest.WindowStartEventID,
		RecordHash: strings.Repeat("7", 64), PayloadSHA256: strings.Repeat("8", 64)}
	startedReference := evaluation.BoundEventReference{EventID: "execution-started",
		RecordHash: strings.Repeat("9", 64), PayloadSHA256: strings.Repeat("a", 64)}
	resultReference := evaluation.BoundEventReference{EventID: baselineRequest.WindowEndEventID,
		RecordHash: strings.Repeat("b", 64), PayloadSHA256: strings.Repeat("c", 64)}
	executionInputBlob := manifestBlob(strings.Repeat("d", 64))
	executionResultBlob := manifestBlob(strings.Repeat("e", 64))
	executionStarted := evaluation.TaskExecutionStarted{
		SchemaVersion: evaluation.TaskExecutionStartedSchema, ExecutionID: "execution-schema",
		TrialPlanID: planID, TrialPairID: pairID, AttemptID: baselineRequest.AttemptID,
		Condition: evaluation.TaskConditionBaseline, ChallengeSHA256: "352302489bc2fcf025cf00cda8308033f97ac87712ce90b4d7cd72c58e4c3af9",
		Contract: contractReference, SystemUnderTestSHA256: baselineRequest.SystemUnderTestSHA256,
		SupervisorArtifactSHA256: strings.Repeat("1", 64), ExecutableSHA256: strings.Repeat("2", 64),
		ArgumentsSHA256: strings.Repeat("3", 64), EnvironmentSHA256: strings.Repeat("4", 64),
		InputBlob: executionInputBlob, StartedAt: now, Privacy: "local_only",
	}
	executionReceipt := evaluation.TaskExecutionReceipt{
		SchemaVersion: evaluation.TaskExecutionReceiptSchema, ReceiptID: "execution-receipt-schema",
		ExecutionID: executionStarted.ExecutionID, TrialPlanID: planID, TrialPairID: pairID,
		AttemptID: baselineRequest.AttemptID, Condition: evaluation.TaskConditionBaseline,
		Agent: ledger.AgentCodex, Provider: "schema-provider", Model: "schema-model",
		Contract: contractReference, Started: startedReference, Result: resultReference,
		SystemUnderTestSHA256:    baselineRequest.SystemUnderTestSHA256,
		SupervisorArtifactSHA256: executionStarted.SupervisorArtifactSHA256,
		ExecutableSHA256:         executionStarted.ExecutableSHA256,
		ArgumentsSHA256:          executionStarted.ArgumentsSHA256,
		EnvironmentSHA256:        executionStarted.EnvironmentSHA256,
		InputBlob:                executionInputBlob, ResultBlob: executionResultBlob,
		Outcome: evaluation.TaskExecutionCompleted, ResultMediaType: "text/plain", ExitCode: 0,
		StartedAt: now, FinishedAt: now.Add(time.Second),
		MemoryReferences: []retrieval.MemoryReference{}, Privacy: "local_only",
	}
	executionResult := evaluation.TaskExecutionResult{SchemaVersion: evaluation.TaskExecutionResultSchema,
		Request: baselineRequest, Execution: executionReceipt, Privacy: "local_only"}
	executionFailure := evaluation.TaskExecutionFailure{SchemaVersion: evaluation.TaskExecutionFailureSchema,
		Kind: "timeout", Message: "synthetic timeout", StdoutBlob: &executionResultBlob}
	compactionRequest := evaluation.CompactionGroundTruthSealRequest{
		SchemaVersion: evaluation.CompactionGroundTruthSealRequestSchema,
		CorpusID:      corpusResult.CorpusID, ReviewerID: "schema-reviewer",
		Reason: "Reviewed frozen evidence without detector output.",
		Labels: []evaluation.CompactionGroundTruthLabel{{EventID: source.Event.EventID,
			Expected: evaluation.DriftPreserved}}, Privacy: "local_only",
	}
	compactionPack := evaluation.CompactionGroundTruthPack{
		SchemaVersion: evaluation.CompactionGroundTruthPackSchema,
		PackID:        "compaction-pack-schema", PackSHA256: strings.Repeat("5", 64),
		CorpusID: corpusResult.CorpusID, CorpusContentSHA256: manifest.CorpusContentSHA256,
		ReviewerID: compactionRequest.ReviewerID, Reason: compactionRequest.Reason, SealedAt: now,
		Subjects: []evaluation.CompactionGroundTruthSubject{{EventID: source.Event.EventID,
			RecordHash: source.RecordHash, Agent: ledger.AgentCodex, Expected: evaluation.DriftPreserved}},
		Privacy: "local_only",
	}
	compactionSeal := evaluation.CompactionGroundTruthSealResult{
		SchemaVersion: evaluation.CompactionGroundTruthSealSchema, Pack: compactionPack,
		EventID: "compaction-ground-truth-event", RecordHash: strings.Repeat("6", 64), Privacy: "local_only",
	}

	if _, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1}); err != nil {
		t.Fatal(err)
	}
	generationAudits, err := episodes.ListVerifiedGenerationAudits(store)
	if err != nil {
		t.Fatal(err)
	}
	generationAttempts, err := episodes.ListVerifiedGenerationAttempts(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(generationAttempts) != 1 {
		t.Fatalf("expected one producer-generated episode attempt, got %d", len(generationAttempts))
	}
	if len(generationAudits) != 1 {
		t.Fatalf("expected one producer-generated episode audit, got %d", len(generationAudits))
	}

	instances := map[string]any{
		"learning-evaluation-input.schema.json":              input,
		"learning-evaluation-report.schema.json":             run.Report,
		"learning-evaluation-verification.schema.json":       verification,
		"learning-evaluation-attestation.schema.json":        attestation,
		"learning-evaluation-attestation-result.schema.json": attestationResult,
		"task-attempt-request.schema.json":                   attemptRequest,
		"task-attempt-contract.schema.json":                  attemptContract,
		"task-attempt-verdict.schema.json":                   attemptVerdict,
		"task-attempt-receipt.schema.json":                   attemptResult.Receipt,
		"task-attempt-record-result.schema.json":             attemptResult,
		"task-attempt-verification.schema.json":              attemptVerification,
		"task-attempt-draft.schema.json":                     attemptDraft,
		"task-attempt-preregistration.schema.json":           attemptPreregistration,
		"task-attempt-observation-result.schema.json":        attemptObservation,
		"builtin-evidence-score-criteria.schema.json":        builtinCriteria,
		"portable-population-snapshot.schema.json":           portablePopulationSnapshot,
		"capture-evaluation-snapshot.schema.json":            captureEvaluationSnapshot,
		"task-oracle-registry.schema.json":                   oracleRegistry,
		"task-oracle-replay-input.schema.json":               oracleReplayInput,
		"system-under-test-manifest.schema.json":             systemUnderTest,
		"legacy-corpus-manifest.schema.json":                 manifest,
		"legacy-corpus-freeze-result.schema.json":            corpusResult,
		"legacy-corpus-verification.schema.json":             corpusVerification,
		"evaluation-trial-corpus-selection.schema.json":      trialSelection,
		"evaluation-trial-plan.schema.json":                  trialPlan,
		"evaluation-trial-plan-preregistration.schema.json":  trialPreregistration,
		"execution-harness-manifest.schema.json":             harnessManifest,
		"agent-execution-input.schema.json":                  executionInput,
		"task-execution-started.schema.json":                 executionStarted,
		"task-execution-receipt.schema.json":                 executionReceipt,
		"task-execution-result.schema.json":                  executionResult,
		"task-execution-failure.schema.json":                 executionFailure,
		"episode-generation-attempt.schema.json":             generationAttempts[0].Attempt,
		"episode-generation-audit.schema.json":               generationAudits[0].Audit,
		"compaction-ground-truth-seal-request.schema.json":   compactionRequest,
		"compaction-ground-truth-pack.schema.json":           compactionPack,
		"compaction-ground-truth-seal.schema.json":           compactionSeal,
	}
	for schemaName, instance := range instances {
		t.Run(schemaName, func(t *testing.T) {
			validatePublishedInstance(t, schemaName, instance)
		})
	}
	brokenTrialBinding := baselineRequest
	brokenTrialBinding.TrialPairID = ""
	rejectPublishedInstance(t, "task-attempt-request.schema.json", brokenTrialBinding)
	unknownTokens := attemptVerdict
	unknownTokens.TokenCountEvaluated = false
	unknownTokens.TotalTokens = 10
	rejectPublishedInstance(t, "task-attempt-verdict.schema.json", unknownTokens)
	unboundPlanAttempts := trialPreregistration
	unboundPlanAttempts.Attempts = append([]evaluation.TaskAttemptPreregistration{}, trialPreregistration.Attempts...)
	unboundPlanAttempts.Attempts[0].Request.TrialPlanID = ""
	unboundPlanAttempts.Attempts[0].Request.TrialPairID = ""
	rejectPublishedInstance(t, "evaluation-trial-plan-preregistration.schema.json", unboundPlanAttempts)
	wrongArmCondition := trialPlan
	wrongArmCondition.Pairs = append([]evaluation.TrialPlanPair{}, trialPlan.Pairs...)
	wrongArmCondition.Pairs[0].Baseline.Contract.Condition = evaluation.TaskConditionMemory
	rejectPublishedInstance(t, "evaluation-trial-plan.schema.json", wrongArmCondition)
	legacyEvent := source.Event
	legacyEvent.SchemaVersion = ledger.SchemaVersionV1Alpha1
	validatePublishedInstance(t, "evidence-event.schema.json", legacyEvent)
	newEvent := source.Event
	newEvent.Kind = ledger.KindEvaluationTrialPlan
	validatePublishedInstance(t, "evidence-event.schema.json", newEvent)
	newEvent.SchemaVersion = ledger.SchemaVersionV1Alpha1
	rejectPublishedInstance(t, "evidence-event.schema.json", newEvent)
	newEvent.SchemaVersion, newEvent.Kind = ledger.SchemaVersionV1Alpha2, ledger.EventKind("unknown_kind")
	rejectPublishedInstance(t, "evidence-event.schema.json", newEvent)
}

func TestEvaluationReportSchemaLeavesReleaseAuthorityToTheEngine(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "evals", "fixtures", "quality-pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input evaluation.EvaluationInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	input.QualityProfile = evaluation.QualityProfileComponent
	report, err := evaluation.Calculate(input)
	if err != nil {
		t.Fatal(err)
	}
	report.InputBlob = &ledger.BlobRef{
		SHA256: strings.Repeat("a", 64), Bytes: 1,
		RelativePath: "evidence/blobs/sha256/aa/" + strings.Repeat("a", 62),
	}
	report.ReportSHA256 = strings.Repeat("b", 64)
	report.ReleaseReady = true
	validatePublishedInstance(t, "learning-evaluation-report.schema.json", report)
}

func blobPointer(reference ledger.BlobRef) *ledger.BlobRef {
	return &reference
}

func validatePublishedInstance(t *testing.T, schemaName string, instance any) {
	t.Helper()
	schema, err := readSchema(schemaName)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{
		Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			return readSchema(path.Base(uri.Path))
		},
	})
	if err != nil {
		t.Fatalf("resolve %s: %v", schemaName, err)
	}
	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(value); err != nil {
		t.Fatalf("%s rejected its Go instance: %v\n%s", schemaName, err, data)
	}
}

func rejectPublishedInstance(t *testing.T, schemaName string, instance any) {
	t.Helper()
	schema, err := readSchema(schemaName)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{
		Loader: func(uri *url.URL) (*jsonschema.Schema, error) {
			return readSchema(path.Base(uri.Path))
		},
	})
	if err != nil {
		t.Fatalf("resolve %s: %v", schemaName, err)
	}
	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(value); err == nil {
		t.Fatalf("%s accepted an invalid instance: %s", schemaName, data)
	}
}

func readSchema(name string) (*jsonschema.Schema, error) {
	if path.Base(name) != name || !strings.HasSuffix(name, ".schema.json") {
		return nil, fmt.Errorf("unsupported schema reference %q", name)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}
