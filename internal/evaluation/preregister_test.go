package evaluation

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func TestPreregisteredBuiltinAttemptsMeasureRealBaselineAndMemoryTreatment(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(4_000, 0).UTC()
	memoryText := "Please remember that generated reports stay local"
	seedPayload, _ := json.Marshal(map[string]string{"message": memoryText})
	appendPopulationEvent(t, store, "e2e-memory-seed", ledger.KindUserMessage, ledger.AgentCodex,
		"capture", "capture/v1", "e2e-seed", string(seedPayload), now)
	repository, _, semanticKey := makePopulationPortableRepository(t, store, memoryText)

	errorPayload := []byte("acceptance check failed")
	successPayload := []byte("acceptance check passed")
	correctionPayload := []byte("please fix the failed output")
	criteria := builtinEvidenceScoreCriteria{
		SchemaVersion: builtinEvidenceScoreCriteriaSchema,
		AllowedEventOrders: [][]builtinEventExpectation{
			{{Kind: ledger.KindUserMessage, PayloadSHA256: sha256Hex(correctionPayload)},
				{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(errorPayload)}},
			{{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(successPayload)}},
		},
		Results: []builtinResultExpectation{
			{PayloadSHA256: sha256Hex(errorPayload), Label: ResultLabelError},
			{PayloadSHA256: sha256Hex(successPayload), Label: ResultLabelSuccess},
		},
		Users:         []builtinUserExpectation{{PayloadSHA256: sha256Hex(correctionPayload), Label: UserMessageCorrection}},
		PassThreshold: 0.75,
	}
	sort.Slice(criteria.Results, func(i, j int) bool { return criteria.Results[i].PayloadSHA256 < criteria.Results[j].PayloadSHA256 })
	criteriaData, err := json.Marshal(criteria)
	if err != nil {
		t.Fatal(err)
	}
	sutData := bindPopulationSUT(t, store, ledger.AgentCodex)
	entry := OracleRegistryEntry{Kind: "builtin", ID: "evidence-score", Version: "v1", Arguments: []string{}}
	registryPath := writeOracleRegistry(t, entry)
	options := TaskAttemptPreregisterOptions{
		TaskSpec: []byte("produce the requested report"), AcceptanceCriteria: criteriaData,
		ExecutionConfig: []byte(`{"temperature":0}`), SystemUnderTest: sutData,
		OracleRegistryPath: registryPath,
	}

	baselineDraft := TaskAttemptDraft{SchemaVersion: TaskAttemptDraftSchema, TaskID: "e2e-task",
		AttemptID: "e2e-baseline", Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
		Condition: TaskConditionBaseline, Privacy: "local_only"}
	options.ThreadID, options.SessionID = "e2e-baseline", "e2e-baseline"
	options.Now = func() time.Time { return now.Add(time.Second) }
	baseline, err := PreregisterTaskAttempt(store, baselineDraft, options)
	if err != nil {
		t.Fatal(err)
	}
	appendPopulationEventWithParents(t, store, "e2e-baseline-correction", ledger.KindUserMessage,
		ledger.AgentCodex, "agent", "agent/v1", options.ThreadID, string(correctionPayload),
		now.Add(2*time.Second), baseline.EventID)
	observedBaseline, err := ObserveTaskAttemptResult(store, baseline.Request, errorPayload,
		"text/plain", func() time.Time { return now.Add(3 * time.Second) })
	if err != nil || observedBaseline.EventID != baseline.Request.WindowEndEventID {
		t.Fatalf("baseline result observation failed: %+v, %v", observedBaseline, err)
	}
	baselineResult, err := FinalizeBuiltinTaskAttempt(store, baseline.Request, registryPath,
		func() time.Time { return now.Add(4 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	memoryDraft := TaskAttemptDraft{SchemaVersion: TaskAttemptDraftSchema, TaskID: "e2e-task",
		AttemptID: "e2e-memory", Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
		Condition: TaskConditionMemory, Privacy: "local_only"}
	options.ThreadID, options.SessionID = "e2e-memory", "e2e-memory"
	options.Now = func() time.Time { return now.Add(5 * time.Second) }
	memory, err := PreregisterTaskAttempt(store, memoryDraft, options)
	if err != nil {
		t.Fatal(err)
	}
	contextValue := retrieval.Context{Agent: ledger.AgentCodex, ThreadID: options.ThreadID,
		SessionID: options.SessionID, Channel: retrieval.ChannelHarness}
	delivered, err := retrieval.BuildContext(store, repository, retrieval.Request{
		SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local", Context: contextValue,
		Limit: 1, TokenBudget: 512, ByteBudget: 2048,
	})
	if err != nil || len(delivered.Memories) != 1 {
		t.Fatalf("memory treatment delivery failed: %+v, %v", delivered, err)
	}
	observedMemory, err := ObserveTaskAttemptResult(store, memory.Request, successPayload,
		"text/plain", func() time.Time { return now.Add(6 * time.Second) })
	if err != nil || observedMemory.EventID != memory.Request.WindowEndEventID {
		t.Fatalf("memory result observation failed: %+v, %v", observedMemory, err)
	}
	if _, err := retrieval.RecordAdoption(store, contextValue, retrieval.AdoptionRequest{
		SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
		Reporter:           retrieval.Reporter{Kind: "harness", ID: "evidence-score"},
		RetrievalReceiptID: delivered.Retrieval.ReceiptID, InjectionID: delivered.InjectionID,
		Items: []retrieval.AdoptionItem{{MemoryReference: delivered.Memories[0],
			Adoption: retrieval.AdoptionAdopted, Outcome: retrieval.OutcomeHelpful,
			Reason: "The task result followed the delivered memory."}},
		OutcomeEvidenceEventIDs: []string{memory.Request.WindowEndEventID},
	}); err != nil {
		t.Fatal(err)
	}
	memoryResult, err := FinalizeBuiltinTaskAttempt(store, memory.Request, registryPath,
		func() time.Time { return now.Add(7 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	if baselineResult.Receipt.Measurement.Success || baselineResult.Receipt.Measurement.Score != 0 ||
		baselineResult.Receipt.Measurement.UserCorrections != 1 ||
		!memoryResult.Receipt.Measurement.Success || memoryResult.Receipt.Measurement.Score != 1 ||
		memoryResult.Receipt.Measurement.UserCorrections != 0 ||
		baselineResult.Receipt.Measurement.TokenCountEvaluated ||
		memoryResult.Receipt.Measurement.TokenCountEvaluated ||
		baselineResult.Receipt.Measurement.TotalTokens != 0 ||
		memoryResult.Receipt.Measurement.TotalTokens != 0 {
		t.Fatalf("blind preregistered comparison was not evidence-derived: baseline=%+v memory=%+v",
			baselineResult.Receipt.Measurement, memoryResult.Receipt.Measurement)
	}
	if len(VerifyTaskAttempt(store, baselineResult.Receipt.ReceiptID).Issues) != 0 ||
		len(VerifyTaskAttempt(store, memoryResult.Receipt.ReceiptID).Issues) != 0 {
		t.Fatal("preregistered task attempt receipts did not independently replay")
	}

	conflictDraft := TaskAttemptDraft{SchemaVersion: TaskAttemptDraftSchema, TaskID: "e2e-conflict-task",
		AttemptID: "e2e-conflict", Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
		Condition: TaskConditionBaseline, Privacy: "local_only"}
	options.ThreadID, options.SessionID = "e2e-conflict", "e2e-conflict"
	options.Now = func() time.Time { return now.Add(8 * time.Second) }
	conflict, err := PreregisterTaskAttempt(store, conflictDraft, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveTaskAttemptResult(store, conflict.Request, successPayload,
		"text/plain", func() time.Time { return now.Add(9 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	conflictingVerdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: conflict.Request.TaskID,
		AttemptID: conflict.Request.AttemptID, Verdict: TaskVerdictFail, Score: 0,
		TokenCountEvaluated: false, TotalTokens: 0,
		ResultEvents: []LabeledResultEvent{{EventID: conflict.Request.WindowEndEventID, Label: ResultLabelError}},
		UserMessages: []LabeledUserMessage{}, TaskSpecSHA256: conflict.Request.TaskSpecSHA256,
		CriteriaSHA256: conflict.Request.AcceptanceCriteriaSHA256,
		ConfigSHA256:   conflict.Request.ExecutionConfigSHA256, Privacy: "local_only",
	}
	conflictingData, err := json.Marshal(conflictingVerdict)
	if err != nil {
		t.Fatal(err)
	}
	appendPopulationEventWithParents(t, store, conflict.Request.Oracle.VerdictEventID,
		ledger.KindToolResult, ledger.AgentCodex, "evidence-score", "v1", options.ThreadID,
		string(conflictingData), now.Add(10*time.Second), conflict.Request.WindowEndEventID)
	if _, err := FinalizeBuiltinTaskAttempt(store, conflict.Request, registryPath,
		func() time.Time { return now.Add(11 * time.Second) }); err == nil {
		t.Fatal("an existing non-canonical verdict bypassed the builtin scorer")
	}

	earlyDraft := TaskAttemptDraft{SchemaVersion: TaskAttemptDraftSchema, TaskID: "e2e-early-task",
		AttemptID: "e2e-early-adoption", Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
		Condition: TaskConditionMemory, Privacy: "local_only"}
	options.ThreadID, options.SessionID = "e2e-early-adoption", "e2e-early-adoption"
	options.Now = func() time.Time { return now.Add(12 * time.Second) }
	early, err := PreregisterTaskAttempt(store, earlyDraft, options)
	if err != nil {
		t.Fatal(err)
	}
	earlyContext := retrieval.Context{Agent: ledger.AgentCodex, ThreadID: options.ThreadID,
		SessionID: options.SessionID, Channel: retrieval.ChannelHarness}
	earlyDelivery, err := retrieval.BuildContext(store, repository, retrieval.Request{
		SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local", Context: earlyContext,
		Limit: 1, TokenBudget: 512, ByteBudget: 2048,
	})
	if err != nil || len(earlyDelivery.Memories) != 1 {
		t.Fatalf("early-adoption delivery failed: %+v, %v", earlyDelivery, err)
	}
	if _, err := retrieval.RecordAdoption(store, earlyContext, retrieval.AdoptionRequest{
		SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
		Reporter:           retrieval.Reporter{Kind: "harness", ID: "evidence-score"},
		RetrievalReceiptID: earlyDelivery.Retrieval.ReceiptID, InjectionID: earlyDelivery.InjectionID,
		Items: []retrieval.AdoptionItem{{MemoryReference: earlyDelivery.Memories[0],
			Adoption: retrieval.AdoptionAdopted, Outcome: retrieval.OutcomeUnknown,
			Reason: "Recorded before the task result."}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveTaskAttemptResult(store, early.Request, successPayload,
		"text/plain", func() time.Time { return now.Add(13 * time.Second) }); err != nil {
		t.Fatal(err)
	}
	if _, err := FinalizeBuiltinTaskAttempt(store, early.Request, registryPath,
		func() time.Time { return now.Add(14 * time.Second) }); err == nil {
		t.Fatal("an adoption recorded before the task result was accepted")
	}
}
