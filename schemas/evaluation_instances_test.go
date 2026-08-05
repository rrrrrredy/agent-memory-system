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
			AttemptIDs:                    []string{"task-attempt-" + strings.Repeat("b", 64)},
			EligibleFollowupOpportunities: 1,
		},
	}
	attestationResult, err := evaluation.RecordAttestation(store, attestation,
		func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	attemptRequest := evaluation.TaskAttemptRequest{
		SchemaVersion: evaluation.TaskAttemptRequestSchema, TaskID: "schema-task",
		AttemptID: "schema-attempt", Agent: ledger.AgentCodex,
		SemanticKeySHA256: strings.Repeat("b", 64), TaskSpecSHA256: strings.Repeat("c", 64),
		AcceptanceCriteriaSHA256: strings.Repeat("d", 64), ExecutionConfigSHA256: strings.Repeat("e", 64),
		Condition: evaluation.TaskConditionBaseline, WindowStartEventID: "schema-attempt-start",
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
	appendAttemptSchemaEvent(attemptRequest.WindowStartEventID, ledger.KindSystemEvent,
		string(contractData), now.Add(3*time.Second))
	appendAttemptSchemaEvent(attemptRequest.WindowEndEventID, ledger.KindToolResult,
		"task passed", now.Add(4*time.Second), attemptRequest.WindowStartEventID)
	attemptVerdict := evaluation.TaskAttemptVerdict{
		SchemaVersion: evaluation.TaskAttemptVerdictSchema, TaskID: attemptRequest.TaskID,
		AttemptID: attemptRequest.AttemptID, Verdict: evaluation.TaskVerdictPass, Score: 1,
		TotalTokens: 10,
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
		"legacy-corpus-manifest.schema.json":                 manifest,
		"legacy-corpus-freeze-result.schema.json":            corpusResult,
		"legacy-corpus-verification.schema.json":             corpusVerification,
	}
	for schemaName, instance := range instances {
		t.Run(schemaName, func(t *testing.T) {
			validatePublishedInstance(t, schemaName, instance)
		})
	}
}

func TestContinuousEvaluationReportSchemaRejectsReleaseReady(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "evals", "fixtures", "quality-pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	input, err := evaluation.DecodeInput(file)
	if err != nil {
		t.Fatal(err)
	}
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
	rejectPublishedInstance(t, "learning-evaluation-report.schema.json", report)
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
