package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/claudecode"
	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/adapters/opencode"
	"github.com/rrrrrredy/agent-memory-system/internal/backup"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/diagnostics"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/evaluation"
	"github.com/rrrrrredy/agent-memory-system/internal/gitsync"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

const rememberedText = "Please remember that generated reports must stay local"

func TestPromotedMemorySurvivesEncryptedRecoveryAndTravelsToThreeAgents(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Memory Acceptance")
	t.Setenv("GIT_AUTHOR_EMAIL", "memory-acceptance@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Memory Acceptance")
	t.Setenv("GIT_COMMITTER_EMAIL", "memory-acceptance@example.invalid")

	base := shortTempDir(t)
	sourceStore, err := ledger.Init(filepath.Join(base, "device-a-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	importThreeAgentSources(t, sourceStore, filepath.Join(base, "agent-sources"))
	if report := sourceStore.Verify(); len(report.Issues) != 0 {
		t.Fatalf("raw evidence did not verify: %+v", report)
	}

	episodeResult, err := episodes.Build(sourceStore, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if episodeResult.Episodes != 3 {
		t.Fatalf("three Agent sources did not produce three episodes: %+v", episodeResult)
	}
	candidateResult, err := candidates.Build(sourceStore, candidates.BuildOptions{
		EpisodeGenerationPath: episodeResult.GenerationPath,
		ShardCount:            1,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := findCandidate(t, candidateResult.GenerationPath, rememberedText)
	if candidate.Validation.Status != candidates.StatusReviewReady ||
		!containsSupport(candidate.SupportTypes, candidates.SupportExplicitRemember) {
		t.Fatalf("explicit memory was not review-ready: %+v", candidate)
	}

	generation := filepath.Base(candidateResult.GenerationPath)
	reviewed, err := review.Apply(sourceStore, generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion,
		Reviewer:      review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedStatus: review.StatusPending, Action: review.ActionValidate,
			Scope:  &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis:  []review.Basis{review.BasisExplicitRemember},
			Reason: "The instruction is explicit and safe for cross-Agent use.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	textDigest := sha256.Sum256([]byte(rememberedText))
	promoted, err := promotion.Apply(sourceStore, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"},
		Action:        promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{
			Generation: generation, CandidateID: candidate.CandidateID,
			CandidateContentSHA256:  candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewed.RecordSHA256,
		},
		ExpectedTextSHA256: hex.EncodeToString(textDigest[:]),
		Reason:             "The reviewed memory is eligible for portable retrieval.",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := initBareRemote(t, filepath.Join(base, "remote-memory.git"))
	deviceARepository := filepath.Join(base, "device-a-memory")
	bootstrap, err := gitsync.Bootstrap(context.Background(), gitsync.BootstrapOptions{
		RepositoryRoot: deviceARepository, RemoteURL: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Head == "" || !bootstrap.RemoteConfigured {
		t.Fatalf("portable repository did not bootstrap: %+v", bootstrap)
	}
	if initial, err := gitsync.Run(context.Background(), gitsync.SyncOptions{
		RepositoryRoot: deviceARepository,
	}); err != nil || initial.Outcome != "remote_initialized" {
		t.Fatalf("portable root was not published: %+v, %v", initial, err)
	}
	exported, err := portable.Export(sourceStore, deviceARepository, portable.ExportOptions{
		MemoryIDs: []string{promoted.Revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exported.RevisionsWritten != 1 {
		t.Fatalf("promoted memory was not projected: %+v", exported)
	}
	published, err := gitsync.Run(context.Background(), gitsync.SyncOptions{
		RepositoryRoot: deviceARepository,
	})
	if err != nil || published.Outcome != "pushed" || !published.Pushed {
		t.Fatalf("portable memory was not synchronized: %+v, %v", published, err)
	}

	deviceBRepository := filepath.Join(base, "device-b-memory")
	cloneRepository(t, remote, deviceBRepository)
	if report := gitsync.Verify(context.Background(), deviceBRepository); len(report.Issues) != 0 ||
		report.Portable.ActiveMemories != 1 {
		t.Fatalf("second device did not verify the synchronized memory: %+v", report)
	}
	assertNoRawEvidenceInPortableRepository(t, deviceBRepository)
	if err := os.Rename(remote, remote+".offline"); err != nil {
		t.Fatalf("disconnect portable memory remote: %v", err)
	}

	key, err := backup.GenerateIdentity(filepath.Join(base, "recovery-key", "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "backup", "evidence.age")
	created, err := backup.Create(sourceStore, backup.CreateOptions{
		Output: archive, Recipients: []string{key.Recipient},
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := backup.Verify(backup.VerifyOptions{
		Archive: archive, IdentityPaths: []string{key.IdentityPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verified.LedgerVerified || len(verified.Issues) != 0 ||
		verified.ArchiveSHA256 != created.ArchiveSHA256 ||
		verified.SourceLastRecordHash != created.SourceLastRecordHash {
		t.Fatalf("encrypted evidence backup did not verify: %+v", verified)
	}
	restoredPath := filepath.Join(base, "device-b-evidence")
	restored, err := backup.Restore(backup.RestoreOptions{
		Archive: archive, IdentityPaths: []string{key.IdentityPath}, Target: restoredPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Issues) != 0 || restored.DeviceIDRotated ||
		restored.SourceDeviceID != restored.RestoredDeviceID {
		t.Fatalf("encrypted evidence did not restore as one logical store: %+v", restored)
	}
	deviceBStore, err := ledger.Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	recoveryReport := diagnostics.Run(context.Background(), deviceBStore, diagnostics.Options{
		Repository: deviceBRepository, RequireRepository: true,
	})
	if !recoveryReport.Ready || recoveryReport.Evidence.RecordsChecked != restored.RestoredRecords ||
		!recoveryReport.RepositoryChecked {
		t.Fatalf("replacement device was not ready after restore and clone: %+v", recoveryReport)
	}
	channels := map[ledger.Agent]retrieval.DeliveryChannel{
		ledger.AgentCodex:      retrieval.ChannelCodexHook,
		ledger.AgentClaudeCode: retrieval.ChannelClaudeHook,
		ledger.AgentOpenCode:   retrieval.ChannelOpenCodePlugin,
	}
	var measuredDelivery retrieval.ContextResult
	var measuredAdoption retrieval.AdoptionReceipt
	var measuredOutcomeEventID string
	for _, agent := range []ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode} {
		contextValue := retrieval.Context{
			Agent: agent, ThreadID: "acceptance-" + string(agent), Channel: channels[agent],
		}
		delivered, err := retrieval.BuildContext(deviceBStore, deviceBRepository, retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local",
			Context: contextValue, Limit: 3, TokenBudget: 512, ByteBudget: 2048,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(delivered.Memories) != 1 || delivered.InjectionID == "" ||
			!strings.Contains(delivered.Content, rememberedText) {
			t.Fatalf("%s did not receive the synchronized memory: %+v", agent, delivered)
		}
		outcomeEventID := "acceptance-outcome-" + string(agent)
		payload := ledger.InlinePayload("utf-8", "text/plain", "completed without exporting raw evidence")
		if _, err := deviceBStore.Append(ledger.Event{
			SchemaVersion: ledger.SchemaVersion, EventID: outcomeEventID, Kind: ledger.KindToolResult,
			ObservedAt: time.Now().UTC(), RecordedAt: time.Now().UTC(),
			Source: ledger.Source{
				Agent: agent, Adapter: "acceptance-harness", AdapterVersion: "acceptance-harness/v1",
				DeviceID: deviceBStore.DeviceID(), ThreadID: contextValue.ThreadID,
			},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Causality: &ledger.Causality{ParentEventIDs: []string{delivered.InjectionID}},
			Privacy:   ledger.Privacy{Classification: "local_only"},
		}); err != nil {
			t.Fatal(err)
		}
		adoption, err := retrieval.RecordAdoption(deviceBStore, contextValue, retrieval.AdoptionRequest{
			SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
			Reporter:           retrieval.Reporter{Kind: "harness", ID: "cross-agent-acceptance"},
			RetrievalReceiptID: delivered.Retrieval.ReceiptID,
			InjectionID:        delivered.InjectionID,
			Items: []retrieval.AdoptionItem{{
				MemoryReference: delivered.Memories[0], Adoption: retrieval.AdoptionAdopted,
				Outcome: retrieval.OutcomeHelpful, Reason: "The delivered constraint was followed.",
			}},
			OutcomeEvidenceEventIDs: []string{outcomeEventID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if agent == ledger.AgentCodex {
			measuredDelivery = delivered
			measuredAdoption = adoption
			measuredOutcomeEventID = outcomeEventID
		}
	}

	receipts := retrieval.Verify(deviceBStore)
	if len(receipts.Issues) != 0 || len(receipts.OpenRetrievalIDs) != 0 ||
		receipts.RetrievalsChecked != 3 || receipts.InjectionsChecked != 3 || receipts.AdoptionsChecked != 3 {
		t.Fatalf("cross-Agent delivery receipts did not close: %+v", receipts)
	}
	if report := deviceBStore.Verify(); len(report.Issues) != 0 {
		t.Fatalf("second-device evidence did not verify: %+v", report)
	}
	finalReport := diagnostics.Run(context.Background(), deviceBStore, diagnostics.Options{
		Repository: deviceBRepository, RequireRepository: true,
	})
	if !finalReport.Ready || finalReport.Retrieval.RetrievalsChecked != 3 ||
		finalReport.Retrieval.InjectionsChecked != 3 || finalReport.Retrieval.AdoptionsChecked != 3 {
		t.Fatalf("replacement device did not remain ready after cross-Agent retrieval: %+v", finalReport)
	}
	reexported, err := portable.Export(deviceBStore, deviceBRepository, portable.ExportOptions{
		MemoryIDs: []string{promoted.Revision.MemoryID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reexported.RevisionsUnchanged != 1 || reexported.RevisionsWritten != 0 {
		t.Fatalf("later recovery and retrieval receipts broke idempotent export: %+v", reexported)
	}
	assertSixCategoryQualityGate(t, deviceBStore, deviceBRepository, candidate,
		promoted.Revision.MemoryID, measuredDelivery.Memories[0].RevisionID, promoted.Revision.TextSHA256,
		measuredDelivery, measuredAdoption, measuredOutcomeEventID)
}

func assertSixCategoryQualityGate(
	t *testing.T, store *ledger.Store, repository string, candidate candidates.Candidate,
	memoryID, revisionID, textSHA256 string, delivered retrieval.ContextResult,
	adoption retrieval.AdoptionReceipt, outcomeEventID string,
) {
	t.Helper()
	now := time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)
	baselineAttempt, treatmentAttempt := recordComparableTaskAttempts(
		t, store, repository, candidate.SemanticKeySHA256, now)
	events := []ledger.Event{
		qualityEvidenceEvent(store, "quality-source-snapshot", ledger.KindSourceSnapshot,
			"complete source capture", now),
		qualityEvidenceEvent(store, "quality-compaction", ledger.KindCompaction,
			`{"summary":"The expected constraint was omitted and independently corrected."}`, now.Add(time.Second)),
	}
	records, err := store.AppendBatch(events)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]ledger.Record{}
	for _, record := range records {
		byID[record.Event.EventID] = record
	}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.EventID == delivered.Retrieval.ReceiptID ||
			record.Event.EventID == adoption.AdoptionID || record.Event.EventID == outcomeEventID ||
			record.Event.EventID == baselineAttempt.Receipt.ReceiptID ||
			record.Event.EventID == treatmentAttempt.Receipt.ReceiptID {
			byID[record.Event.EventID] = record
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range []string{delivered.Retrieval.ReceiptID, adoption.AdoptionID, outcomeEventID} {
		if _, exists := byID[eventID]; !exists {
			t.Fatalf("quality evidence %q is unavailable", eventID)
		}
	}

	memoryMeasurement := evaluation.MemoryMeasurement{
		MemoryID: memoryID, Label: evaluation.MemorySupported, Active: true, Retrieved: true,
	}
	correctionMeasurement := evaluation.CorrectionMeasurement{
		SemanticKeySHA256:             candidate.SemanticKeySHA256,
		AttemptIDs:                    []string{treatmentAttempt.Receipt.ReceiptID},
		EligibleFollowupOpportunities: 1, RepeatedCorrections: 0, RepeatedCorrectionsAfterMemory: 0,
	}
	driftMeasurement := evaluation.CompactionMeasurement{
		CheckpointID: "quality-checkpoint-1", Expected: evaluation.DriftDetected,
		Observed: evaluation.DriftDetected,
	}
	pairedMeasurement := evaluation.PairedOutcomeMeasurement{
		PairID: "quality-pair-1", BaselineAttemptID: baselineAttempt.Receipt.ReceiptID,
		TreatmentAttemptID: treatmentAttempt.Receipt.ReceiptID,
		Baseline:           baselineAttempt.Receipt.Measurement, Treatment: treatmentAttempt.Receipt.Measurement,
	}
	attestationRecords := map[string]evaluation.AttestationResult{}
	for _, attestation := range []evaluation.EvaluationAttestation{
		{
			SchemaVersion: evaluation.EvaluationAttestationSchema, AttestationID: "quality-memory-label",
			CaseID: "false-memory", Category: evaluation.CategoryFalseMemory, Agent: ledger.AgentCodex,
			Attestor: evaluation.Attestor{Kind: "human", ID: "owner"}, AttestedAt: now.Add(4 * time.Second),
			Reason: "The promoted memory was checked against its supporting evidence.", Memory: &memoryMeasurement,
		},
		{
			SchemaVersion: evaluation.EvaluationAttestationSchema, AttestationID: "quality-drift-label",
			CaseID: "compaction-drift", Category: evaluation.CategoryCompactionDrift, Agent: ledger.AgentCodex,
			Attestor: evaluation.Attestor{Kind: "human", ID: "owner"}, AttestedAt: now.Add(6 * time.Second),
			Reason:     "The expected and observed continuity labels were independently reviewed.",
			Compaction: &driftMeasurement,
		},
	} {
		attested, err := evaluation.RecordAttestation(store, attestation,
			func() time.Time { return now.Add(8 * time.Second) })
		if err != nil {
			t.Fatal(err)
		}
		attestationRecords[attestation.CaseID] = attested
	}
	ledgerReference := func(id string) evaluation.EvidenceReference {
		if record, exists := byID[id]; exists {
			return evaluation.EvidenceReference{Kind: "ledger_event", ID: id, SHA256: record.RecordHash}
		}
		attested, exists := attestationRecords[id]
		if !exists {
			t.Fatalf("quality evidence reference %q is unavailable", id)
		}
		return evaluation.EvidenceReference{
			Kind: "ledger_event", ID: attested.EventID, SHA256: attested.RecordHash,
		}
	}
	one, zero, maximumTokens, minimumDelta := 1.0, 0.0, 4096.0, 0.1
	minimumSamples, maximumCorrectionDelta := 1.0, 0.0
	input := evaluation.EvaluationInput{
		SchemaVersion: evaluation.EvaluationInputSchemaVersion,
		SuiteID:       "six-category-quality", RunID: "quality-run-1", CreatedAt: now.Add(9 * time.Second),
		SystemVersion: "acceptance-v1", QualityProfile: evaluation.QualityProfileContinuousLearning,
		Privacy: "local_only",
		Thresholds: evaluation.EvaluationThresholds{
			MinimumCaptureCoverage: &one, MaximumFalseMemoryRate: &zero,
			MaximumUnknownMemoryRate: &zero, MaximumRepeatedCorrectionRate: &zero,
			MinimumDriftPrecision: &one, MinimumDriftRecall: &one,
			MaximumMeanRetrievalTokens: &maximumTokens, MinimumMeanOutcomeScoreDelta: &minimumDelta,
			MaximumMeanCorrectionDelta: &maximumCorrectionDelta, MaximumHarmfulOutcomes: &zero,
			MinimumCorrectionOpportunities: &minimumSamples, MinimumPairedOutcomePairs: &minimumSamples,
		},
		Cases: []evaluation.EvaluationCase{
			{
				CaseID: "capture", Category: evaluation.CategoryCaptureCoverage, Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{ledgerReference("quality-source-snapshot")},
				Capture: &evaluation.CaptureMeasurement{
					Unit: evaluation.CaptureUnitEvidenceEvents, Expected: 1, Complete: 1,
				},
			},
			{
				CaseID: "false-memory", Category: evaluation.CategoryFalseMemory, Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{
					{Kind: "portable_revision", ID: revisionID, SHA256: textSHA256},
					ledgerReference("false-memory"),
				},
				Memory: &memoryMeasurement,
			},
			{
				CaseID: "repeated-correction", Category: evaluation.CategoryRepeatedCorrection,
				Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{
					ledgerReference(treatmentAttempt.Receipt.ReceiptID),
				},
				Correction: &correctionMeasurement,
			},
			{
				CaseID: "compaction-drift", Category: evaluation.CategoryCompactionDrift,
				Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{
					ledgerReference("quality-compaction"), ledgerReference("compaction-drift"),
				},
				Compaction: &driftMeasurement,
			},
			{
				CaseID: "retrieval-cost", Category: evaluation.CategoryRetrievalCost,
				Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{
					ledgerReference(delivered.Retrieval.ReceiptID), ledgerReference(adoption.AdoptionID),
					ledgerReference(outcomeEventID),
				},
				Retrieval: &evaluation.RetrievalMeasurement{
					RetrievalID:     delivered.Retrieval.ReceiptID,
					EstimatedTokens: delivered.Retrieval.SelectedEstimatedTokens,
					UTF8Bytes:       delivered.Retrieval.SelectedBytes,
					SelectedItems:   len(delivered.Retrieval.Selected), Adopted: true,
					Outcome: evaluation.OutcomeHelpful,
				},
			},
			{
				CaseID: "paired-outcome", Category: evaluation.CategoryPairedOutcome, Agent: ledger.AgentCodex,
				Evidence: []evaluation.EvidenceReference{
					ledgerReference(baselineAttempt.Receipt.ReceiptID),
					ledgerReference(treatmentAttempt.Receipt.ReceiptID),
				},
				PairedOutcome: &pairedMeasurement,
			},
		},
	}
	result, err := evaluation.Run(store, input, evaluation.RunOptions{
		PortableRoot: repository, Now: func() time.Time { return now.Add(10 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.ReleaseReady || result.Report.Authority != evaluation.EvaluationAuthorityMeasurementOnly ||
		len(result.Report.Issues) != 1 || result.Report.Issues[0] != evaluation.ContinuousLearningEfficacyIssue ||
		result.Report.CasesChecked != 6 || result.Report.AttestedReferences != 2 ||
		len(result.Report.Gates) != 12 {
		t.Fatalf("six-category diagnostic report crossed its authority boundary: %+v", result.Report)
	}
	for _, gate := range result.Report.Gates {
		if gate.Status != "pass" {
			t.Fatalf("quality gate %q did not pass: %+v", gate.Name, gate)
		}
	}
	if verification := evaluation.VerifyRun(store, input.SuiteID, input.RunID, repository); len(verification.Issues) != 0 {
		t.Fatalf("quality evaluation did not verify: %+v", verification)
	}
	reexported, err := portable.Export(store, repository, portable.ExportOptions{MemoryIDs: []string{memoryID}})
	if err != nil {
		t.Fatal(err)
	}
	if reexported.RevisionsWritten != 0 || reexported.RevisionsUnchanged != 1 {
		t.Fatalf("local task attempts changed portable projection: %+v", reexported)
	}
	assertStringsAbsentInPortableRepository(t, repository, []string{
		baselineAttempt.Receipt.ReceiptID, treatmentAttempt.Receipt.ReceiptID,
		baselineAttempt.Receipt.TaskID, baselineAttempt.Receipt.AttemptID,
		treatmentAttempt.Receipt.AttemptID, treatmentAttempt.Receipt.Oracle.ID,
		baselineAttempt.Receipt.RequestSHA256, treatmentAttempt.Receipt.RequestSHA256,
		baselineAttempt.Receipt.Verdict.RecordHash, treatmentAttempt.Receipt.Verdict.RecordHash,
	})
}

func recordComparableTaskAttempts(t *testing.T, store *ledger.Store, repository,
	semanticKey string, now time.Time) (evaluation.TaskAttemptResult, evaluation.TaskAttemptResult) {
	t.Helper()
	taskSpec := sha256.Sum256([]byte("write one deterministic local report"))
	criteria := sha256.Sum256([]byte("the report remains local and tests pass"))
	config := sha256.Sum256([]byte("acceptance-harness/v1|deterministic"))
	contract := func(attemptID string, condition evaluation.TaskCondition,
		startID, endID, verdictID string) evaluation.TaskAttemptRequest {
		return evaluation.TaskAttemptRequest{
			SchemaVersion: evaluation.TaskAttemptRequestSchema, TaskID: "quality-task",
			AttemptID: attemptID, Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
			TaskSpecSHA256:           hex.EncodeToString(taskSpec[:]),
			AcceptanceCriteriaSHA256: hex.EncodeToString(criteria[:]),
			ExecutionConfigSHA256:    hex.EncodeToString(config[:]), Condition: condition,
			WindowStartEventID: startID, WindowEndEventID: endID,
			Oracle: evaluation.TaskOracle{Kind: "harness", ID: "quality-acceptance",
				Version: "quality-acceptance/v1", VerdictEventID: verdictID}, Privacy: "local_only",
		}
	}
	appendEvent := func(id string, kind ledger.EventKind, thread, content string,
		at time.Time, parents ...string) {
		event := qualityEvidenceEvent(store, id, kind, content, at)
		event.Source.ThreadID = thread
		event.Source.SessionID = thread
		if len(parents) != 0 {
			event.Causality = &ledger.Causality{ParentEventIDs: parents}
		}
		if _, err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	appendVerdict := func(request evaluation.TaskAttemptRequest, verdict evaluation.TaskAttemptVerdict,
		at time.Time, parent string) {
		data, err := json.Marshal(verdict)
		if err != nil {
			t.Fatal(err)
		}
		appendEvent(request.Oracle.VerdictEventID, ledger.KindToolResult,
			request.WindowStartEventID, string(data), at, parent)
	}

	baseline := contract("quality-baseline", evaluation.TaskConditionBaseline,
		"quality-baseline", "quality-baseline-result", "quality-baseline-verdict")
	baselineContract, err := json.Marshal(evaluation.NewTaskAttemptContract(baseline))
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(baseline.WindowStartEventID, ledger.KindSystemEvent,
		baseline.WindowStartEventID, string(baselineContract), now)
	appendEvent(baseline.WindowEndEventID, ledger.KindToolResult,
		baseline.WindowStartEventID, "acceptance check failed", now.Add(time.Second), baseline.WindowStartEventID)
	appendVerdict(baseline, evaluation.TaskAttemptVerdict{
		SchemaVersion: evaluation.TaskAttemptVerdictSchema, TaskID: baseline.TaskID,
		AttemptID: baseline.AttemptID, Verdict: evaluation.TaskVerdictFail, Score: 0.4,
		TotalTokens: 500,
		ResultEvents: []evaluation.LabeledResultEvent{{EventID: baseline.WindowEndEventID,
			Label: evaluation.ResultLabelError}}, UserMessages: []evaluation.LabeledUserMessage{},
		TaskSpecSHA256: baseline.TaskSpecSHA256, CriteriaSHA256: baseline.AcceptanceCriteriaSHA256,
		ConfigSHA256: baseline.ExecutionConfigSHA256, Privacy: "local_only",
	}, now.Add(2*time.Second), baseline.WindowEndEventID)
	baselineResult, err := evaluation.RecordTaskAttempt(store, baseline,
		func() time.Time { return now.Add(3 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	treatment := contract("quality-treatment", evaluation.TaskConditionMemory,
		"quality-treatment", "quality-treatment-result", "quality-treatment-verdict")
	treatmentContract, err := json.Marshal(evaluation.NewTaskAttemptContract(treatment))
	if err != nil {
		t.Fatal(err)
	}
	appendEvent(treatment.WindowStartEventID, ledger.KindSystemEvent,
		treatment.WindowStartEventID, string(treatmentContract), now.Add(4*time.Second))
	contextValue := retrieval.Context{Agent: ledger.AgentCodex, ThreadID: treatment.WindowStartEventID,
		SessionID: treatment.WindowStartEventID, Channel: retrieval.ChannelHarness}
	delivered, err := retrieval.BuildContext(store, repository, retrieval.Request{
		SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local",
		Context: contextValue, Limit: 1, TokenBudget: 512, ByteBudget: 2048,
	})
	if err != nil || len(delivered.Memories) != 1 {
		t.Fatalf("controlled memory delivery failed: %+v, %v", delivered, err)
	}
	treatment.RetrievalReceiptID = delivered.Retrieval.ReceiptID
	treatment.InjectionID = delivered.InjectionID
	treatment.MemoryReferences = delivered.Memories
	appendEvent(treatment.WindowEndEventID, ledger.KindToolResult,
		treatment.WindowStartEventID, "acceptance check passed", now.Add(5*time.Second), treatment.InjectionID)
	appendVerdict(treatment, evaluation.TaskAttemptVerdict{
		SchemaVersion: evaluation.TaskAttemptVerdictSchema, TaskID: treatment.TaskID,
		AttemptID: treatment.AttemptID, Verdict: evaluation.TaskVerdictPass, Score: 0.9,
		TotalTokens: 400,
		ResultEvents: []evaluation.LabeledResultEvent{{EventID: treatment.WindowEndEventID,
			Label: evaluation.ResultLabelSuccess}}, UserMessages: []evaluation.LabeledUserMessage{},
		TaskSpecSHA256: treatment.TaskSpecSHA256, CriteriaSHA256: treatment.AcceptanceCriteriaSHA256,
		ConfigSHA256: treatment.ExecutionConfigSHA256, Privacy: "local_only",
	}, now.Add(6*time.Second), treatment.WindowEndEventID)
	adoption, err := retrieval.RecordAdoption(store, contextValue, retrieval.AdoptionRequest{
		SchemaVersion:      retrieval.AdoptionRequestSchemaVersion,
		Reporter:           retrieval.Reporter{Kind: "harness", ID: "deterministic-checker"},
		RetrievalReceiptID: treatment.RetrievalReceiptID, InjectionID: treatment.InjectionID,
		Items: []retrieval.AdoptionItem{{MemoryReference: treatment.MemoryReferences[0],
			Adoption: retrieval.AdoptionAdopted, Outcome: retrieval.OutcomeUnknown,
			Reason: "The controlled treatment consumed the injected memory."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	treatment.AdoptionID = adoption.AdoptionID
	treatmentResult, err := evaluation.RecordTaskAttempt(store, treatment,
		func() time.Time { return now.Add(7 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	return baselineResult, treatmentResult
}

func qualityEvidenceEvent(
	store *ledger.Store, id string, kind ledger.EventKind, content string, observed time.Time,
) ledger.Event {
	payload := ledger.InlinePayload("utf-8", "text/plain", content)
	return ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
		ObservedAt: observed, RecordedAt: observed,
		Source: ledger.Source{
			Agent: ledger.AgentCodex, Adapter: "quality-acceptance", AdapterVersion: "quality-acceptance/v1",
			DeviceID: store.DeviceID(), ThreadID: "quality-acceptance",
		},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"},
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("", "agent-memory-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Errorf("remove temporary acceptance directory: %v", err)
		}
	})
	return path
}

func importThreeAgentSources(t *testing.T, store *ledger.Store, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	codexSource := filepath.Join(root, "rollout-2026-08-05T00-00-00-11111111-1111-1111-1111-111111111111.jsonl")
	codexData := `{"timestamp":"2026-08-05T00:00:00Z","type":"session_meta","payload":{"id":"11111111-1111-1111-1111-111111111111"}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"` + rememberedText + `"}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex-private-model-response"}]}}` + "\n" +
		`{"timestamp":"2026-08-05T00:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-private","output":"codex-private-tool-output"}}` + "\n"
	writeFile(t, codexSource, []byte(codexData))
	if result, err := codex.ImportPath(store, codexSource, codex.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("Codex source did not import completely: %+v, %v", result, err)
	}

	claudeSource := filepath.Join(root, "22222222-2222-2222-2222-222222222222.jsonl")
	claudeData := `{"type":"user","uuid":"claude-user","sessionId":"22222222-2222-2222-2222-222222222222","timestamp":"2026-08-05T00:01:00Z","message":{"role":"user","content":"claude-private-user-instruction"}}` + "\n" +
		`{"type":"assistant","uuid":"claude-assistant","sessionId":"22222222-2222-2222-2222-222222222222","timestamp":"2026-08-05T00:01:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"claude-private-reasoning"},{"type":"text","text":"claude-private-model-response"}]}}` + "\n"
	writeFile(t, claudeSource, []byte(claudeData))
	if result, err := claudecode.ImportPath(store, claudeSource, claudecode.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("Claude Code source did not import completely: %+v, %v", result, err)
	}

	openCodeSource := filepath.Join(root, "ses_acceptance.json")
	openCodeData := `{
  "info": {"id":"ses_acceptance","projectID":"project_acceptance","directory":"/private/source/path","title":"Acceptance","version":"1","time":{"created":1785888120000,"updated":1785888122000}},
  "messages": [
    {"info":{"id":"oc-user","sessionID":"ses_acceptance","role":"user","time":{"created":1785888120000}},"parts":[{"id":"oc-user-text","sessionID":"ses_acceptance","messageID":"oc-user","type":"text","text":"opencode-private-user-instruction"}]},
    {"info":{"id":"oc-assistant","sessionID":"ses_acceptance","role":"assistant","time":{"created":1785888121000,"completed":1785888122000}},"parts":[{"id":"oc-reasoning","sessionID":"ses_acceptance","messageID":"oc-assistant","type":"reasoning","text":"opencode-private-reasoning"},{"id":"oc-text","sessionID":"ses_acceptance","messageID":"oc-assistant","type":"text","text":"opencode-private-model-response"}]}
  ]
}` + "\n"
	writeFile(t, openCodeSource, []byte(openCodeData))
	if result, err := opencode.ImportPath(store, openCodeSource, opencode.Options{}); err != nil ||
		result.EventsAppended == 0 || result.GapsAppended != 0 {
		t.Fatalf("OpenCode source did not import completely: %+v, %v", result, err)
	}
}

func findCandidate(t *testing.T, generationPath, text string) candidates.Candidate {
	t.Helper()
	file, err := os.Open(filepath.Join(generationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var candidate candidates.Candidate
		if err := decoder.Decode(&candidate); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if candidate.Text == text {
			return candidate
		}
	}
	t.Fatalf("candidate %q was not derived", text)
	return candidates.Candidate{}
}

func containsSupport(values []candidates.SupportType, wanted candidates.SupportType) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func initBareRemote(t *testing.T, path string) string {
	t.Helper()
	command := exec.Command("git", "init", "--bare", "--initial-branch", gitsync.DefaultBranch, path)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize bare remote: %v: %s", err, output)
	}
	return path
}

func cloneRepository(t *testing.T, remote, target string) {
	t.Helper()
	command := exec.Command("git", "clone", "--branch", gitsync.DefaultBranch, "--", remote, target)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone portable repository: %v: %s", err, output)
	}
}

func assertNoRawEvidenceInPortableRepository(t *testing.T, root string) {
	t.Helper()
	forbidden := []string{
		"codex-private-model-response", "codex-private-tool-output",
		"claude-private-user-instruction", "claude-private-model-response",
		"claude-private-reasoning", "opencode-private-user-instruction",
		"opencode-private-model-response", "opencode-private-reasoning",
		"/private/source/path",
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".agentmem") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if strings.Contains(string(data), value) {
				t.Fatalf("portable repository contains raw-only marker %q", value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertStringsAbsentInPortableRepository(t *testing.T, root string, forbidden []string) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".agentmem") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if value != "" && strings.Contains(string(data), value) {
				t.Fatalf("portable repository contains local measurement marker %q", value)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
