package evaluation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestRunBindsEvidencePersistsAndVerifies(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	record, err := store.Append(testEvidenceEvent("source-1", ledger.KindSourceSnapshot,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	minimum := 1.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "verified-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		QualityProfile: QualityProfileComponent,
		Thresholds:     EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{{
			CaseID: "capture", Category: CategoryCaptureCoverage, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{Kind: "ledger_event", ID: record.Event.EventID, SHA256: record.RecordHash}},
			Capture:  &CaptureMeasurement{Unit: CaptureUnitEvidenceEvents, Expected: 1, Complete: 1},
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.ReleaseReady || result.Report.EvidenceReferencesChecked != 1 || result.Report.InputBlob == nil {
		t.Fatalf("verified run was not release-ready: %+v", result.Report)
	}
	if _, err := os.Stat(result.ReportPath); err != nil {
		t.Fatalf("report was not persisted: %v", err)
	}
	verification := VerifyRun(store, input.SuiteID, input.RunID)
	if len(verification.Issues) != 0 {
		t.Fatalf("verification failed: %+v", verification)
	}
	reused, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(2 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.ReportEventID != result.ReportEventID {
		t.Fatalf("identical run was not reused: %+v", reused)
	}
}

func TestRunRejectsMeasurementThatDoesNotMatchEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EvaluationReport)
	}{
		{name: "issues", mutate: func(report *EvaluationReport) { report.Issues = []string{} }},
		{name: "release ready", mutate: func(report *EvaluationReport) { report.ReleaseReady = true }},
		{name: "authority", mutate: func(report *EvaluationReport) { report.Authority = "efficacy" }},
		{name: "numeric metric", mutate: func(report *EvaluationReport) { report.Capture.Complete++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, input, result, sourceID, now := mismatchedCaptureRun(t)
			forged := result.Report
			test.mutate(&forged)
			persistForgedEvaluationRun(t, store, input, forged, result.ReportPath,
				now.Add(2*time.Second), []string{sourceID})
			verification := VerifyRun(store, input.SuiteID, input.RunID)
			if !containsIssue(verification.Issues, "complete evidence replay") {
				t.Fatalf("independently forged %s passed verification: %+v", test.name, verification)
			}
		})
	}
}

func TestVerifyRunRejectsRemovedContinuousEfficacyIssue(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join("..", "..", "evals", "fixtures", "quality-pass.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	input, err := DecodeInput(file)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(225, 0).UTC()
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	forged := result.Report
	forged.Issues = removeIssue(forged.Issues, ContinuousLearningEfficacyIssue)
	if len(forged.Issues) != len(result.Report.Issues)-1 {
		t.Fatalf("continuous efficacy issue was unavailable: %+v", result.Report.Issues)
	}
	persistForgedEvaluationRun(t, store, input, forged, result.ReportPath,
		now.Add(time.Second), nil)
	verification := VerifyRun(store, input.SuiteID, input.RunID)
	if !containsIssue(verification.Issues, "complete evidence replay") {
		t.Fatalf("continuous report without efficacy blocker passed verification: %+v", verification)
	}
}

func mismatchedCaptureRun(t *testing.T) (*ledger.Store, EvaluationInput, RunResult, string, time.Time) {
	t.Helper()
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	record, err := store.Append(testEvidenceEvent("source-1", ledger.KindSourceSnapshot,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	minimum := 0.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "mismatch-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		QualityProfile: QualityProfileComponent,
		Thresholds:     EvaluationThresholds{MinimumCaptureCoverage: &minimum},
		Cases: []EvaluationCase{{
			CaseID: "capture", Category: CategoryCaptureCoverage, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{Kind: "ledger_event", ID: record.Event.EventID, SHA256: record.RecordHash}},
			Capture:  &CaptureMeasurement{Unit: CaptureUnitEvidenceEvents, Expected: 1, Partial: 1},
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.ReleaseReady || len(result.Report.Issues) == 0 ||
		!strings.Contains(result.Report.Issues[0], "capture counts") {
		t.Fatalf("fabricated measurement was accepted: %+v", result.Report)
	}
	return store, input, result, record.Event.EventID, now
}

func persistForgedEvaluationRun(t *testing.T, store *ledger.Store, input EvaluationInput,
	forged EvaluationReport, reportPath string, recordedAt time.Time, parents []string) {
	t.Helper()
	if err := setReportHash(&forged); err != nil {
		t.Fatal(err)
	}
	forgedData, err := marshalIndented(forged)
	if err != nil {
		t.Fatal(err)
	}
	forgedBlob, err := store.PutBlob(strings.NewReader(string(forgedData)))
	if err != nil {
		t.Fatal(err)
	}
	forgedEvent := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: evaluationRunEventID(input, forged),
		Kind: ledger.KindEvaluationRun, ObservedAt: input.CreatedAt, RecordedAt: recordedAt,
		Source: ledger.Source{Agent: ledger.AgentUnknown, Adapter: evaluationAdapterName,
			AdapterVersion: evaluationAdapterVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: input.SuiteID, SessionID: input.RunID, SourceEventID: input.RunID,
			SourcePathHash: forged.InputBlob.SHA256, SourceCursor: "run:" + input.SuiteID + "/" + input.RunID},
		Payload: &ledger.Payload{Encoding: "json", MediaType: "application/json", Blob: &forgedBlob,
			SHA256: forgedBlob.SHA256, Bytes: forgedBlob.Bytes},
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	if len(parents) != 0 {
		forgedEvent.Causality = &ledger.Causality{ParentEventIDs: parents}
	}
	if _, err := store.Append(forgedEvent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, forgedData, 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeIssue(issues []string, target string) []string {
	result := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue != target {
			result = append(result, issue)
		}
	}
	return result
}

func TestRunRequiresARecordedAttestationForInterpretedMeasurements(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(250, 0).UTC()
	compactionRecord, err := store.Append(testEvidenceEvent("compaction-checkpoint", ledger.KindCompaction,
		ledger.CompletenessComplete, now))
	if err != nil {
		t.Fatal(err)
	}
	measurement := CompactionMeasurement{
		CheckpointID: "checkpoint-1", Expected: DriftDetected, Observed: DriftDetected,
	}
	attestation := EvaluationAttestation{
		SchemaVersion: EvaluationAttestationSchema, AttestationID: "compaction-label-1",
		CaseID: "compaction", Category: CategoryCompactionDrift, Agent: ledger.AgentCodex,
		Attestor: Attestor{Kind: "human", ID: "owner"}, AttestedAt: now.Add(time.Second),
		Reason: "The expected and observed continuity labels were reviewed.", Compaction: &measurement,
	}
	attested, err := RecordAttestation(store, attestation,
		func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	minimum := 1.0
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: "attested-suite",
		RunID: "run-1", CreatedAt: now, SystemVersion: "test-v1", Privacy: "local_only",
		QualityProfile: QualityProfileComponent,
		Thresholds:     EvaluationThresholds{MinimumDriftPrecision: &minimum, MinimumDriftRecall: &minimum},
		Cases: []EvaluationCase{{
			CaseID: "compaction", Category: CategoryCompactionDrift, Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{
				{Kind: "ledger_event", ID: compactionRecord.Event.EventID, SHA256: compactionRecord.RecordHash},
				{Kind: "ledger_event", ID: attested.EventID, SHA256: attested.RecordHash},
			},
			Compaction: &measurement,
		}},
	}
	result, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(3 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.ReleaseReady || result.Report.AttestedReferences != 1 ||
		result.Report.EvidenceReferencesChecked != 2 {
		t.Fatalf("recorded attestation did not support the run: %+v", result.Report)
	}

	input.RunID = "run-2"
	input.Cases[0].Compaction = &CompactionMeasurement{
		CheckpointID: "checkpoint-1", Expected: DriftDetected, Observed: DriftPreserved,
	}
	mismatched, err := Run(store, input, RunOptions{Now: func() time.Time { return now.Add(4 * time.Second) }})
	if err != nil {
		t.Fatal(err)
	}
	if mismatched.Report.ReleaseReady ||
		!containsIssue(mismatched.Report.Issues, "attestation measurement does not match") {
		t.Fatalf("mismatched attestation was accepted: %+v", mismatched.Report)
	}
}

func containsIssue(issues []string, fragment string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, fragment) {
			return true
		}
	}
	return false
}

func TestSummarizedOutcomeCannotHideHarmAsUnknown(t *testing.T) {
	if got := summarizedOutcome(map[OutcomeLabel]struct{}{
		OutcomeHelpful: {}, OutcomeHarmful: {},
	}); got != OutcomeHarmful {
		t.Fatalf("summarized outcome = %q, want harmful", got)
	}
	if got := summarizedOutcome(map[OutcomeLabel]struct{}{}); got != OutcomeUnknown {
		t.Fatalf("empty outcome = %q, want unknown", got)
	}
}

func TestComparableTaskAttemptsRejectContractMismatch(t *testing.T) {
	baseline := TaskAttemptReceipt{
		Condition: TaskConditionBaseline, Agent: ledger.AgentCodex, TaskID: "task-1",
		TaskSpecSHA256: repeatedSHA("a"), AcceptanceCriteriaSHA256: repeatedSHA("b"),
		ExecutionConfigSHA256: repeatedSHA("c"), SemanticKeySHA256: repeatedSHA("d"),
		Oracle: TaskOracle{Kind: "harness", ID: "checker", Version: "v1"},
	}
	treatment := baseline
	treatment.Condition = TaskConditionMemory
	if !comparableTaskAttempts(baseline, treatment, ledger.AgentCodex) {
		t.Fatal("matching task contracts were rejected")
	}
	for name, mutate := range map[string]func(*TaskAttemptReceipt){
		"task spec": func(receipt *TaskAttemptReceipt) { receipt.TaskSpecSHA256 = repeatedSHA("e") },
		"criteria":  func(receipt *TaskAttemptReceipt) { receipt.AcceptanceCriteriaSHA256 = repeatedSHA("e") },
		"config":    func(receipt *TaskAttemptReceipt) { receipt.ExecutionConfigSHA256 = repeatedSHA("e") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := treatment
			mutate(&changed)
			if comparableTaskAttempts(baseline, changed, ledger.AgentCodex) {
				t.Fatalf("paired attempts with different %s were accepted", name)
			}
		})
	}
}

func testEvidenceEvent(id string, kind ledger.EventKind, status ledger.CompletenessStatus,
	now time.Time) ledger.Event {
	payload := ledger.InlinePayload("text", "text/plain", "evidence")
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
		ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentCodex, Adapter: "test-adapter",
			AdapterVersion: "test-adapter/v1", DeviceID: "device-test", ThreadID: "thread-test"},
		Payload: &payload, Completeness: ledger.Completeness{Status: status},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
}
