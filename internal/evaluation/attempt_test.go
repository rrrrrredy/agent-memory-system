package evaluation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

func TestTaskAttemptDerivesMeasurementFromCompleteCausalWindow(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(800, 0).UTC()
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	appendAttemptContract(t, store, request, now)
	appendAttemptEvent(t, store, "attempt-user", ledger.KindUserMessage,
		"please fix the failed output", now.Add(time.Second), []string{"attempt-start"})
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult,
		"checker failed", now.Add(2*time.Second), []string{"attempt-start"})
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictFail, Score: 0.25, TotalTokens: 300,
		ResultEvents:   []LabeledResultEvent{{EventID: "attempt-result", Label: ResultLabelError}},
		UserMessages:   []LabeledUserMessage{{EventID: "attempt-user", Label: UserMessageCorrection}},
		TaskSpecSHA256: request.TaskSpecSHA256, CriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ConfigSHA256: request.ExecutionConfigSHA256, Privacy: "local_only",
	}
	appendAttemptVerdict(t, store, request, verdict, now.Add(3*time.Second), "attempt-result")

	result, err := RecordTaskAttempt(store, request, func() time.Time { return now.Add(4 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Measurement != (TrialMeasurement{Success: false, Score: 0.25,
		Errors: 1, UserCorrections: 1, TotalTokens: 300}) ||
		result.Receipt.BindingStatus != "causal_complete" || result.Receipt.Authority != "measurement_only" {
		t.Fatalf("task attempt measurement was not replay-derived: %+v", result.Receipt)
	}
	verification := VerifyTaskAttempt(store, result.Receipt.ReceiptID)
	if len(verification.Issues) != 0 || verification.EventsChecked < 4 {
		t.Fatalf("task attempt replay failed: %+v", verification)
	}
	reused, err := RecordTaskAttempt(store, request, func() time.Time { return now.Add(5 * time.Second) })
	if err != nil || !reused.Reused || reused.RecordHash != result.RecordHash {
		t.Fatalf("task attempt recording was not idempotent: %+v, %v", reused, err)
	}
}

func TestTaskAttemptRejectsCherryPickedOrNoncausalEvidence(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(900, 0).UTC()
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	appendAttemptContract(t, store, request, now)
	appendAttemptEvent(t, store, "attempt-user", ledger.KindUserMessage, "correction",
		now.Add(time.Second), []string{"attempt-start"})
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult, "failed",
		now.Add(2*time.Second), nil)
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictPass, Score: 1, TotalTokens: 1,
		ResultEvents:   []LabeledResultEvent{{EventID: "attempt-result", Label: ResultLabelSuccess}},
		UserMessages:   []LabeledUserMessage{},
		TaskSpecSHA256: request.TaskSpecSHA256, CriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ConfigSHA256: request.ExecutionConfigSHA256, Privacy: "local_only",
	}
	appendAttemptVerdict(t, store, request, verdict, now.Add(3*time.Second), "attempt-result")
	if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
		(!strings.Contains(err.Error(), "complete observed window") &&
			!strings.Contains(err.Error(), "causally bound")) {
		t.Fatalf("cherry-picked or noncausal attempt was accepted: %v", err)
	}
}

func TestTaskAttemptRejectsNoncausalUserCorrection(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(950, 0).UTC()
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	appendAttemptContract(t, store, request, now)
	appendAttemptEvent(t, store, "attempt-user", ledger.KindUserMessage, "correction",
		now.Add(time.Second), nil)
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult, "failed",
		now.Add(2*time.Second), []string{"attempt-start"})
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictFail, Score: 0, TotalTokens: 1,
		ResultEvents:   []LabeledResultEvent{{EventID: "attempt-result", Label: ResultLabelError}},
		UserMessages:   []LabeledUserMessage{{EventID: "attempt-user", Label: UserMessageCorrection}},
		TaskSpecSHA256: request.TaskSpecSHA256, CriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ConfigSHA256: request.ExecutionConfigSHA256, Privacy: "local_only",
	}
	appendAttemptVerdict(t, store, request, verdict, now.Add(3*time.Second), "attempt-result")
	if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
		!strings.Contains(err.Error(), "user message") || !strings.Contains(err.Error(), "causally bound") {
		t.Fatalf("noncausal user correction was accepted: %v", err)
	}
}

func TestTaskAttemptRejectsPriorMemoryExposureInBaseline(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(960, 0).UTC()
	appendAttemptEvent(t, store, "prior-retrieval", ledger.KindRetrieval, "retrieval",
		now, nil)
	appendAttemptEvent(t, store, "prior-injection", ledger.KindInjection, "injection",
		now.Add(time.Second), []string{"prior-retrieval"})
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	appendAttemptContract(t, store, request, now.Add(2*time.Second), "prior-injection")
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult, "passed",
		now.Add(3*time.Second), []string{"attempt-start"})
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictPass, Score: 1, TotalTokens: 1,
		ResultEvents: []LabeledResultEvent{{EventID: "attempt-result", Label: ResultLabelSuccess}},
		UserMessages: []LabeledUserMessage{}, TaskSpecSHA256: request.TaskSpecSHA256,
		CriteriaSHA256: request.AcceptanceCriteriaSHA256, ConfigSHA256: request.ExecutionConfigSHA256,
		Privacy: "local_only",
	}
	appendAttemptVerdict(t, store, request, verdict, now.Add(4*time.Second), "attempt-result")
	if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
		!strings.Contains(err.Error(), "prior memory exposure") {
		t.Fatalf("baseline inherited prior memory exposure: %v", err)
	}
}

func TestTaskAttemptRejectsDuplicateEvidenceEventIDs(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(970, 0).UTC()
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	appendAttemptContract(t, store, request, now)
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult, "first",
		now.Add(time.Second), []string{"attempt-start"})
	appendAttemptEvent(t, store, "attempt-result", ledger.KindToolResult, "second",
		now.Add(2*time.Second), []string{"attempt-start"})
	if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
		!strings.Contains(err.Error(), "duplicate evidence event id") {
		t.Fatalf("duplicate evidence event id was accepted: %v", err)
	}
}

func TestTaskAttemptDecodersRejectMissingCanonicalArrays(t *testing.T) {
	request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
	request.MemoryReferences = nil
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskAttemptRequest(strings.NewReader(string(data))); err == nil {
		t.Fatal("null memory_references was accepted")
	}
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictPass, Score: 1, TotalTokens: 1,
		ResultEvents:   []LabeledResultEvent{{EventID: "attempt-result", Label: ResultLabelSuccess}},
		TaskSpecSHA256: request.TaskSpecSHA256, CriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ConfigSHA256: request.ExecutionConfigSHA256, Privacy: "local_only",
	}
	if err := validateTaskAttemptVerdict(verdict, request); err == nil {
		t.Fatal("null user_messages was accepted")
	}
}

func TestTaskAttemptRejectsContractAndOracleSourceMismatch(t *testing.T) {
	t.Run("contract hash", func(t *testing.T) {
		store, err := ledger.Init(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(980, 0).UTC()
		request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
		contract := NewTaskAttemptContract(request)
		contract.TaskSpecSHA256 = repeatedSHA("f")
		data, err := json.Marshal(contract)
		if err != nil {
			t.Fatal(err)
		}
		appendAttemptEvent(t, store, request.WindowStartEventID, ledger.KindSystemEvent,
			string(data), now, nil)
		appendAttemptEvent(t, store, request.WindowEndEventID, ledger.KindToolResult, "passed",
			now.Add(time.Second), []string{request.WindowStartEventID})
		verdict := passingAttemptVerdict(request)
		appendAttemptVerdict(t, store, request, verdict, now.Add(2*time.Second), request.WindowEndEventID)
		if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
			!strings.Contains(err.Error(), "declared task contract") {
			t.Fatalf("mismatched task contract was accepted: %v", err)
		}
	})

	t.Run("verdict source", func(t *testing.T) {
		store, err := ledger.Init(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(990, 0).UTC()
		request := baselineAttemptRequest("attempt-start", "attempt-result", "attempt-verdict")
		appendAttemptContract(t, store, request, now)
		appendAttemptEvent(t, store, request.WindowEndEventID, ledger.KindToolResult, "passed",
			now.Add(time.Second), []string{request.WindowStartEventID})
		data, err := json.Marshal(passingAttemptVerdict(request))
		if err != nil {
			t.Fatal(err)
		}
		appendAttemptEventFrom(t, store, request.Oracle.VerdictEventID, ledger.KindToolResult,
			string(data), now.Add(2*time.Second), []string{request.WindowEndEventID}, "wrong-oracle", "v0")
		if _, err := RecordTaskAttempt(store, request, nil); err == nil ||
			!strings.Contains(err.Error(), "oracle does not match") {
			t.Fatalf("mismatched oracle source was accepted: %v", err)
		}
	})
}

func passingAttemptVerdict(request TaskAttemptRequest) TaskAttemptVerdict {
	return TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID, AttemptID: request.AttemptID,
		Verdict: TaskVerdictPass, Score: 1, TotalTokens: 1,
		ResultEvents: []LabeledResultEvent{{EventID: request.WindowEndEventID, Label: ResultLabelSuccess}},
		UserMessages: []LabeledUserMessage{}, TaskSpecSHA256: request.TaskSpecSHA256,
		CriteriaSHA256: request.AcceptanceCriteriaSHA256, ConfigSHA256: request.ExecutionConfigSHA256,
		Privacy: "local_only",
	}
}

func baselineAttemptRequest(start, end, verdict string) TaskAttemptRequest {
	return TaskAttemptRequest{
		SchemaVersion: TaskAttemptRequestSchema, TaskID: "task-1", AttemptID: "attempt-1",
		Agent: ledger.AgentCodex, SemanticKeySHA256: repeatedSHA("a"),
		TaskSpecSHA256: repeatedSHA("b"), AcceptanceCriteriaSHA256: repeatedSHA("c"),
		ExecutionConfigSHA256: repeatedSHA("d"), Condition: TaskConditionBaseline,
		WindowStartEventID: start, WindowEndEventID: end,
		MemoryReferences: []retrieval.MemoryReference{},
		Oracle: TaskOracle{Kind: "harness", ID: "attempt-test", Version: "attempt-test/v1",
			VerdictEventID: verdict},
		Privacy: "local_only",
	}
}

func appendAttemptContract(t *testing.T, store *ledger.Store, request TaskAttemptRequest,
	now time.Time, parents ...string) {
	t.Helper()
	data, err := json.Marshal(NewTaskAttemptContract(request))
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEvent(t, store, request.WindowStartEventID, ledger.KindSystemEvent,
		string(data), now, parents)
}

func appendAttemptVerdict(t *testing.T, store *ledger.Store, request TaskAttemptRequest,
	verdict TaskAttemptVerdict, now time.Time, parent string) {
	t.Helper()
	data, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	appendAttemptEvent(t, store, request.Oracle.VerdictEventID, ledger.KindToolResult,
		string(data), now, []string{parent})
}

func appendAttemptEvent(t *testing.T, store *ledger.Store, id string, kind ledger.EventKind,
	content string, now time.Time, parents []string) {
	t.Helper()
	appendAttemptEventFrom(t, store, id, kind, content, now, parents,
		"attempt-test", "attempt-test/v1")
}

func appendAttemptEventFrom(t *testing.T, store *ledger.Store, id string, kind ledger.EventKind,
	content string, now time.Time, parents []string, adapter, adapterVersion string) {
	t.Helper()
	payload := ledger.InlinePayload("utf-8", "application/json", content)
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
		ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: adapter,
			AdapterVersion: adapterVersion, DeviceID: store.DeviceID(),
			ThreadID: "attempt-thread", SessionID: "attempt-session"},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
	if len(parents) != 0 {
		event.Causality = &ledger.Causality{ParentEventIDs: parents}
	}
	if _, err := store.Append(event); err != nil {
		t.Fatal(err)
	}
}
