package evaluation

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/promotion"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func TestFixedContinuousPolicyRequiresMeasuredImprovement(t *testing.T) {
	thresholds := FixedContinuousLearningThresholds()
	if thresholds.MinimumMeanOutcomeScoreDelta == nil || *thresholds.MinimumMeanOutcomeScoreDelta <= 0 ||
		thresholds.MaximumMeanCorrectionDelta == nil || *thresholds.MaximumMeanCorrectionDelta >= 0 {
		t.Fatalf("continuous policy allows a no-improvement tie: %+v", thresholds)
	}
	issues := efficacyPrerequisiteIssues(EfficacyPrerequisites{})
	if !strings.Contains(strings.Join(issues, "; "), "verified native Agent execution provenance") {
		t.Fatalf("continuous policy omitted the native Agent execution provenance gate: %v", issues)
	}
}

func TestPrepareContinuousInputRequiresFrozenCorpus(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000, 0).UTC()
	appendPopulationEvent(t, store, "population-task", ledger.KindUserMessage, ledger.AgentCodex,
		"capture", "capture/v1", "capture-task", `{"message":"run the task"}`, now)
	_, err = PrepareContinuousInput(store, PrepareContinuousOptions{
		SuiteID: "population-suite", RunID: "population-run", SystemVersion: "population-system/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "verified frozen regression corpus is required") {
		t.Fatalf("continuous input accepted no frozen corpus: %v", err)
	}
}

func TestPortablePopulationSnapshotRejectsAlteredLocalRevision(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2_000, 0).UTC()
	memoryText := "Please remember that generated reports stay local"
	seedPayload, _ := json.Marshal(map[string]string{"message": memoryText})
	appendPopulationEvent(t, store, "portable-memory-seed", ledger.KindUserMessage, ledger.AgentCodex,
		"capture", "capture/v1", "portable-seed", string(seedPayload), now)
	_, revision, _ := makePopulationPortableRepository(t, store, memoryText)

	forged := revision
	forged.Text = "Altered text that was never promoted"
	snapshot := portablePopulationSnapshot{SchemaVersion: portablePopulationSnapshotSchema,
		Active: []string{forged.RevisionID}, Revisions: []portable.Revision{forged}, Privacy: "local_only"}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := store.PutBlob(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPortablePopulationSnapshot(store, reference); err == nil ||
		!strings.Contains(err.Error(), "differs from local promoted evidence") {
		t.Fatalf("portable snapshot accepted altered promoted text: %v", err)
	}

	omitted := portablePopulationSnapshot{SchemaVersion: portablePopulationSnapshotSchema,
		Active: []string{}, Revisions: []portable.Revision{}, Privacy: "local_only"}
	data, err = json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	reference, err = store.PutBlob(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPortablePopulationSnapshot(store, reference); err == nil ||
		!strings.Contains(err.Error(), "omits or adds local promoted evidence") {
		t.Fatalf("portable snapshot accepted omission of an active promoted revision: %v", err)
	}
}

func TestPopulationIncludesPreregisteredAttemptWithoutReceipt(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry := OracleRegistryEntry{Kind: "builtin", ID: "evidence-score", Version: "v1", Arguments: []string{}}
	registryPath := writeOracleRegistry(t, entry)
	registry, err := loadOracleRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	corpusID := freezeMinimalTrialCorpus(t, store)
	artifact, taskSpec, pairID, taskID, assignedAgent := frozenTrialArtifactInput(
		t, store, corpusID,
	)
	sutData := bindPopulationSUT(t, store, ledger.AgentCodex)
	plan, err := PreregisterTrialPlan(store, TrialPlanPreregisterOptions{
		SuiteID: "missing-receipt-suite", CorpusID: corpusID,
		OracleRegistryPath: registryPath,
		Pairs: []TrialPlanPairInput{{PairID: pairID, TaskID: taskID,
			Agent: assignedAgent, CorpusArtifactID: artifact.ArtifactID, SemanticKeySHA256: sha256Hex([]byte("semantic")),
			BaselineThreadID: "baseline-thread", BaselineSessionID: "baseline-session",
			TreatmentThreadID: "treatment-thread", TreatmentSessionID: "treatment-session",
			TaskSpec: taskSpec, AcceptanceCriteria: []byte("criteria"),
			ExecutionConfig: []byte("config"), SystemUnderTest: sutData}},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		t.Fatal(err)
	}
	population := EvaluationPopulation{SystemArtifactSHA256: plan.Plan.SystemArtifactSHA256,
		CorpusID: corpusID, CorpusContentSHA256: manifest.CorpusContentSHA256}
	attempts := loadPopulationAttempts(store, records, ordered, registry, &population)
	if len(attempts) != 0 || !population.Prerequisites.PreregisteredAttemptUniverse ||
		!population.Prerequisites.PairedTrialPlanSealed || len(population.UnpairedAttemptIDs) != 0 ||
		!population.Prerequisites.BlindOracleProtocol || !population.Prerequisites.HermeticOracleExecution ||
		!strings.Contains(strings.Join(population.PopulationIssues, "; "), "has no receipt") {
		t.Fatalf("missing preregistered attempt was omitted from the population: %+v", population)
	}
}

func bindPopulationSUT(t *testing.T, store *ledger.Store, agent ledger.Agent) []byte {
	t.Helper()
	manifest, err := BindSystemUnderTest(store, agent, "test-provider", "test-model",
		SystemUnderTestArtifacts{
			SystemPrompt: []byte("prompt"), ToolRegistry: []byte("tools"),
			Harness: []byte("harness"), Adapter: []byte("adapter"),
		})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func recordPopulationAttempt(t *testing.T, store *ledger.Store, repository, entrySHA, semanticKey string,
	taskBlob, criteriaBlob, configBlob *ledger.BlobRef, condition TaskCondition, index int, now time.Time) {
	t.Helper()
	prefix := "population-" + string(condition) + "-" + twoDigits(index)
	request := TaskAttemptRequest{
		SchemaVersion: TaskAttemptRequestSchema, TaskID: "population-task", AttemptID: prefix,
		Agent: ledger.AgentCodex, SemanticKeySHA256: semanticKey,
		TaskSpecSHA256: taskBlob.SHA256, AcceptanceCriteriaSHA256: criteriaBlob.SHA256,
		ExecutionConfigSHA256: configBlob.SHA256, TaskSpecBlob: taskBlob,
		AcceptanceCriteriaBlob: criteriaBlob, ExecutionConfigBlob: configBlob,
		Condition: condition, WindowStartEventID: prefix + "-start", WindowEndEventID: prefix + "-result",
		MemoryReferences: []retrieval.MemoryReference{},
		Oracle: TaskOracle{Kind: "harness", ID: "population-oracle", Version: "population-oracle/v1",
			VerdictEventID: prefix + "-verdict", RegistryEntrySHA256: entrySHA}, Privacy: "local_only",
	}
	contractData, err := json.Marshal(NewTaskAttemptContract(request))
	if err != nil {
		t.Fatal(err)
	}
	appendPopulationEvent(t, store, request.WindowStartEventID, ledger.KindTaskAttemptContract, ledger.AgentCodex,
		request.Oracle.ID, request.Oracle.Version, prefix, string(contractData), now)
	parent := request.WindowStartEventID
	contextValue := retrieval.Context{Agent: ledger.AgentCodex, ThreadID: prefix,
		SessionID: prefix, Channel: retrieval.ChannelHarness}
	if condition == TaskConditionMemory {
		delivered, err := retrieval.BuildContext(store, repository, retrieval.Request{
			SchemaVersion: retrieval.RequestSchemaVersion, Query: "generated reports local",
			Context: contextValue, Limit: 1, TokenBudget: 512, ByteBudget: 2048,
		})
		if err != nil || len(delivered.Memories) != 1 {
			t.Fatalf("memory delivery failed: %+v, %v", delivered, err)
		}
		request.RetrievalReceiptID, request.InjectionID = delivered.Retrieval.ReceiptID, delivered.InjectionID
		request.MemoryReferences = delivered.Memories
		parent = delivered.InjectionID
	}
	resultText, verdictKind, score := "acceptance check failed", TaskVerdictFail, 0.5
	resultLabel := ResultLabelError
	if condition == TaskConditionMemory {
		resultText, verdictKind, score, resultLabel = "acceptance check passed", TaskVerdictPass, 1, ResultLabelSuccess
	}
	appendPopulationEventWithParents(t, store, request.WindowEndEventID, ledger.KindToolResult,
		ledger.AgentCodex, request.Oracle.ID, request.Oracle.Version, prefix, resultText,
		now.Add(time.Second), parent)
	verdict := TaskAttemptVerdict{SchemaVersion: TaskAttemptVerdictSchema, TaskID: request.TaskID,
		AttemptID: request.AttemptID, Verdict: verdictKind, Score: score, TokenCountEvaluated: true, TotalTokens: 7,
		ResultEvents: []LabeledResultEvent{{EventID: request.WindowEndEventID, Label: resultLabel}},
		UserMessages: []LabeledUserMessage{}, TaskSpecSHA256: request.TaskSpecSHA256,
		CriteriaSHA256: request.AcceptanceCriteriaSHA256, ConfigSHA256: request.ExecutionConfigSHA256,
		Privacy: "local_only"}
	verdictData, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	appendPopulationEventWithParents(t, store, request.Oracle.VerdictEventID, ledger.KindToolResult,
		ledger.AgentCodex, request.Oracle.ID, request.Oracle.Version, prefix, string(verdictData),
		now.Add(2*time.Second), request.WindowEndEventID)
	if condition == TaskConditionMemory {
		adoption, err := retrieval.RecordAdoption(store, contextValue, retrieval.AdoptionRequest{
			SchemaVersion: retrieval.AdoptionRequestSchemaVersion,
			Reporter:      ReporterForPopulation(), RetrievalReceiptID: request.RetrievalReceiptID,
			InjectionID: request.InjectionID, Items: []retrieval.AdoptionItem{{
				MemoryReference: request.MemoryReferences[0], Adoption: retrieval.AdoptionAdopted,
				Outcome: retrieval.OutcomeHelpful, Reason: "The bound treatment followed the injected memory.",
			}}, OutcomeEvidenceEventIDs: []string{request.WindowEndEventID},
		})
		if err != nil {
			t.Fatal(err)
		}
		request.AdoptionID = adoption.AdoptionID
	}
	if _, err := RecordTaskAttempt(store, request, func() time.Time { return now.Add(4 * time.Second) }); err != nil {
		t.Fatal(err)
	}
}

func ReporterForPopulation() retrieval.Reporter {
	return retrieval.Reporter{Kind: "harness", ID: "population-oracle"}
}

func appendPopulationEvent(t *testing.T, store *ledger.Store, id string, kind ledger.EventKind,
	agent ledger.Agent, adapter, version, thread, content string, now time.Time) {
	t.Helper()
	appendPopulationEventWithParents(t, store, id, kind, agent, adapter, version, thread, content, now)
}

func appendPopulationEventWithParents(t *testing.T, store *ledger.Store, id string, kind ledger.EventKind,
	agent ledger.Agent, adapter, version, thread, content string, now time.Time, parents ...string) {
	t.Helper()
	payload := ledger.InlinePayload("utf-8", "application/json", content)
	event := ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: id, Kind: kind,
		ObservedAt: now, RecordedAt: now, Source: ledger.Source{Agent: agent, Adapter: adapter,
			AdapterVersion: version, DeviceID: store.DeviceID(), ThreadID: thread, SessionID: thread},
		Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy: ledger.Privacy{Classification: "local_only"}}
	if len(parents) != 0 {
		event.Causality = &ledger.Causality{ParentEventIDs: parents}
	}
	if _, err := store.Append(event); err != nil {
		t.Fatal(err)
	}
}

func makePopulationPortableRepository(t *testing.T, store *ledger.Store,
	wantedText string) (string, portable.Revision, string) {
	t.Helper()
	episodeResult, err := episodes.Build(store, episodes.BuildOptions{ShardCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	candidateResult, err := candidates.Build(store, candidates.BuildOptions{
		EpisodeGenerationPath: episodeResult.GenerationPath, ShardCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(candidateResult.GenerationPath, "candidates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var candidate candidates.Candidate
	decoder := json.NewDecoder(file)
	for {
		var observed candidates.Candidate
		if err := decoder.Decode(&observed); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if observed.Text == wantedText {
			candidate = observed
			break
		}
	}
	if candidate.CandidateID == "" || candidate.Validation.Status != candidates.StatusReviewReady {
		t.Fatalf("explicit memory candidate was not review-ready: %+v", candidate)
	}
	generation := filepath.Base(candidateResult.GenerationPath)
	reviewed, err := review.Apply(store, generation, review.Request{
		SchemaVersion: review.RequestSchemaVersion, Reviewer: review.Reviewer{Kind: "human", ID: "owner"},
		Transitions: []review.TransitionRequest{{CandidateID: candidate.CandidateID,
			CandidateContentSHA256: candidate.ContentSHA256, ExpectedStatus: review.StatusPending,
			Action: review.ActionValidate, Scope: &review.Scope{Kind: review.ScopeGlobal, Value: "*"},
			Basis:  []review.Basis{review.BasisExplicitRemember},
			Reason: "The explicit instruction is safe for controlled retrieval evaluation."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := promotion.Apply(store, promotion.Request{
		SchemaVersion: promotion.RequestSchemaVersion,
		Approver:      promotion.Approver{Kind: "human", ID: "owner"}, Action: promotion.ActionPromote,
		Candidate: &promotion.CandidateReference{Generation: generation,
			CandidateID: candidate.CandidateID, CandidateContentSHA256: candidate.ContentSHA256,
			ExpectedReviewRecordSHA: reviewed.RecordSHA256},
		ExpectedTextSHA256: sha256Hex([]byte(candidate.Text)),
		Reason:             "The reviewed memory is eligible for controlled cross-Agent retrieval.",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := portable.InitRepository(root); err != nil {
		t.Fatal(err)
	}
	if _, err := portable.Export(store, root, portable.ExportOptions{
		MemoryIDs: []string{promoted.Revision.MemoryID},
	}); err != nil {
		t.Fatal(err)
	}
	if report := portable.VerifyRepository(root); len(report.Issues) != 0 {
		t.Fatalf("portable population fixture did not verify: %+v", report)
	}
	revisions, report := portable.LoadActiveRevisions(root)
	if len(report.Issues) != 0 || len(revisions) != 1 {
		t.Fatalf("portable population fixture did not expose one active revision: %+v %+v", revisions, report)
	}
	return root, revisions[0], candidate.SemanticKeySHA256
}

func twoDigits(value int) string {
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
