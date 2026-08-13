package schemas_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/adapters/codex"
	"github.com/rrrrrredy/agent-memory-system/internal/agentbridge"
	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/dashboard"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	loadoutcontext "github.com/rrrrrredy/agent-memory-system/internal/loadout"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
	"github.com/rrrrrredy/agent-memory-system/internal/reviewpacket"
	"github.com/rrrrrredy/agent-memory-system/internal/study"
)

func runSchemaAgentBridgeHelper() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-schema-native 1.0")
		return
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if len(strings.TrimSpace(string(prompt))) == 0 || len(os.Args) < 2 || os.Args[1] != "exec" {
		os.Exit(2)
	}
	fmt.Println(`{"type":"thread.started","thread_id":"schema-native-thread"}`)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.completed","item":{"id":"message","type":"agent_message","text":"schema answer"}}`)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":2,"cached_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}`)
}

func TestV03ProducerInstancesMatchPublishedSchemas(t *testing.T) {
	store, err := ledger.Init(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "portable")
	if err := portable.InitRepository(repository); err != nil {
		t.Fatal(err)
	}
	revision := writeSchemaPortableRevision(t, repository, "Run focused verification before reporting completion.")
	created, err := portable.CreateLoadout(repository, portable.LoadoutCreateOptions{
		Name: "Completion evidence", Description: "Reviewed project completion guidance.",
		Agents:    []ledger.Agent{ledger.AgentCodex},
		Scope:     review.Scope{Kind: review.ScopeProject, Value: "schema-project"},
		MemoryIDs: []string{revision.MemoryID}, TokenBudget: 800, ByteBudget: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	loadoutList := portable.ListLoadoutStatus(repository)
	loadoutUse := portable.VerifyLoadoutUse(repository, created.Loadout.LoadoutID)
	contextResult, err := loadoutcontext.BuildContext(store, repository, created.Loadout.LoadoutID, retrieval.Context{
		Agent: ledger.AgentCodex, ThreadID: "schema-thread", SessionID: "schema-session",
		Project: "schema-project", Task: "schema-task", Channel: retrieval.ChannelHarness,
	})
	if err != nil {
		t.Fatal(err)
	}
	loadoutVerification := loadoutcontext.Verify(store)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	answerDigest := sha256.Sum256([]byte("schema answer"))
	acceptance := study.AcceptanceContract{SchemaVersion: study.AcceptanceSchema, Mode: "all",
		Assertions: []study.AcceptanceAssertion{{Kind: "agent_message_sha256",
			ExpectedSHA256: hex.EncodeToString(answerDigest[:])}}}
	task := func(id string) study.TaskDraft {
		return study.TaskDraft{TaskID: "schema-study-" + id, ClusterID: "schema-cluster-" + id,
			Prompt: "Return a schema answer.", Model: "default", Sandbox: "read-only",
			WorkingDirectory: workspace, TimeoutSeconds: 60, SkipGitRepositoryCheck: true,
			Acceptance: acceptance}
	}
	studyDraft := study.Draft{SchemaVersion: study.DraftSchema, Name: "Schema longitudinal study",
		Hypothesis: "Reviewed memory may reduce repeated mistakes across prospective tasks.",
		Agent:      ledger.AgentCodex, LoadoutID: created.Loadout.LoadoutID, MinimumElapsedDays: 7,
		Tasks: []study.TaskDraft{task("a"), task("b"), task("c"), task("d")}, Privacy: study.PrivacyLocalOnly}
	studyCreated, err := study.Create(store, studyDraft, study.CreateOptions{PortableRoot: repository,
		Now: func() time.Time { return time.Date(2026, 8, 13, 2, 30, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	var baselineTask study.PlannedTask
	for _, task := range studyCreated.Plan.Tasks {
		if task.Condition == study.ConditionBaseline {
			baselineTask = task
			break
		}
	}
	if baselineTask.TaskID == "" {
		t.Fatal("study producer emitted no baseline task")
	}

	reviewStore, generation := schemaReviewPacketFixture(t)
	packetResult, err := reviewpacket.Build(reviewStore, reviewpacket.BuildOptions{
		Generation: generation.Name, Statuses: []candidates.ReviewStatus{candidates.StatusReviewReady},
		Now: func() time.Time { return time.Date(2026, 8, 13, 2, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := reviewpacket.Open(reviewStore, packetResult.PacketID)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("AGENTMEM_SCHEMA_AGENTBRIDGE_HELPER", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := agentbridge.RunRequest{
		SchemaVersion: agentbridge.RunRequestSchema, TaskID: baselineTask.TaskID, Prompt: baselineTask.Prompt,
		Model: baselineTask.Model, Sandbox: baselineTask.Sandbox, WorkingDirectory: baselineTask.WorkingDirectory,
		TimeoutSeconds: baselineTask.TimeoutSeconds, SkipGitRepositoryCheck: baselineTask.SkipGitRepositoryCheck,
		Privacy: agentbridge.PrivacyLocalOnly,
	}
	nativeResult, err := agentbridge.Run(context.Background(), store, request, agentbridge.Options{
		CodexPath: executable, Now: func() time.Time { return time.Date(2026, 8, 13, 3, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	nativeVerification := agentbridge.Verify(store)
	nativeStart := loadNativeStart(t, store)
	observationRequest := study.ObservationRequest{SchemaVersion: study.ObserveRequestSchema,
		StudyID: studyCreated.Plan.StudyID, TaskID: baselineTask.TaskID,
		ExecutionReceiptID: nativeResult.Receipt.ReceiptID,
		Reporter:           study.Reporter{Kind: "caller_attestation", ID: "schema-reviewer"},
		Reason:             "The prospective task result was checked against its completion criterion.",
		Privacy:            study.PrivacyLocalOnly}
	studyObserved, err := study.Observe(store, observationRequest,
		func() time.Time { return time.Date(2026, 8, 13, 4, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	studyReport, err := study.BuildReport(store, studyCreated.Plan.StudyID,
		func() time.Time { return time.Date(2026, 8, 23, 4, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	studyVerification := study.Verify(store)
	dashboardSnapshot := dashboard.BuildSnapshot(store, dashboard.Options{Repository: repository,
		Now: func() time.Time { return time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC) }})

	instances := map[string]any{
		"portable-memory-loadout.schema.json":                  created.Loadout,
		"portable-memory-loadout-create-result.schema.json":    created,
		"portable-memory-loadout-list.schema.json":             loadoutList,
		"portable-memory-loadout-use-verification.schema.json": loadoutUse,
		"memory-loadout-context-receipt.schema.json":           contextResult.Receipt,
		"memory-loadout-context-result.schema.json":            contextResult,
		"memory-loadout-context-verification.schema.json":      loadoutVerification,
		"candidate-review-packet.schema.json":                  packet,
		"candidate-review-packet-build-result.schema.json":     packetResult,
		"native-agent-run-request.schema.json":                 request,
		"native-agent-execution-start.schema.json":             nativeStart,
		"native-agent-execution-receipt.schema.json":           nativeResult.Receipt,
		"native-agent-execution-result.schema.json":            nativeResult,
		"native-agent-execution-verification.schema.json":      nativeVerification,
		"longitudinal-study-draft.schema.json":                 studyDraft,
		"longitudinal-study-acceptance.schema.json":            acceptance,
		"longitudinal-study-plan.schema.json":                  studyCreated.Plan,
		"longitudinal-study-create-result.schema.json":         studyCreated,
		"longitudinal-study-observation-request.schema.json":   observationRequest,
		"longitudinal-study-outcome-evidence.schema.json":      studyObserved.Outcome,
		"longitudinal-study-observation.schema.json":           studyObserved.Observation,
		"longitudinal-study-observe-result.schema.json":        studyObserved,
		"longitudinal-study-report.schema.json":                studyReport,
		"longitudinal-study-verification.schema.json":          studyVerification,
		"agent-memory-dashboard-snapshot.schema.json":          dashboardSnapshot,
	}
	for schema, instance := range instances {
		t.Run(schema, func(t *testing.T) { validatePublishedInstance(t, schema, instance) })
	}

	wrongLoadout := created.Loadout
	wrongLoadout.SchemaVersion = "portable-memory-loadout/v9"
	rejectPublishedInstance(t, "portable-memory-loadout.schema.json", wrongLoadout)
	falseCompleted := nativeResult.Receipt
	falseCompleted.ProcessExitCode = 2
	rejectPublishedInstance(t, "native-agent-execution-receipt.schema.json", falseCompleted)
	falseAuthority := studyObserved.Observation
	falseAuthority.OutcomeAuthority = study.OutcomeAuthority("caller_attestation")
	rejectPublishedInstance(t, "longitudinal-study-observation.schema.json", falseAuthority)
	wrongPacket := packet
	wrongPacket.SourceEvidenceRecords = 0
	rejectPublishedInstance(t, "candidate-review-packet.schema.json", wrongPacket)
}

func loadNativeStart(t *testing.T, store *ledger.Store) agentbridge.Started {
	t.Helper()
	var result agentbridge.Started
	if err := store.VisitRecords(func(record ledger.Record) error {
		if record.Event.Payload == nil || record.Event.Payload.Blob == nil ||
			record.Event.Payload.MediaType != agentbridge.StartedMediaType {
			return nil
		}
		file, err := store.OpenBlob(*record.Event.Payload.Blob)
		if err != nil {
			return err
		}
		defer file.Close()
		return json.NewDecoder(file).Decode(&result)
	}); err != nil {
		t.Fatal(err)
	}
	if result.StartedEventID == "" {
		t.Fatal("native Agent producer emitted no start record")
	}
	return result
}

func writeSchemaPortableRevision(t *testing.T, root, text string) portable.Revision {
	t.Helper()
	textDigest := sha256.Sum256([]byte(text))
	textSHA := hex.EncodeToString(textDigest[:])
	memoryEnvelope := struct {
		Version            string       `json:"version"`
		RedactedTextSHA256 string       `json:"redacted_text_sha256"`
		Scope              review.Scope `json:"scope"`
	}{"memory-identity/v1alpha1", textSHA, review.Scope{Kind: review.ScopeGlobal, Value: "*"}}
	memoryData, _ := json.Marshal(memoryEnvelope)
	memoryDigest := sha256.Sum256(memoryData)
	revision := portable.Revision{
		SchemaVersion: portable.RevisionSchemaVersion,
		MemoryID:      "memory-" + hex.EncodeToString(memoryDigest[:]),
		Action:        portable.ActionPromote, Status: portable.StatusActive, Kind: candidates.KindDirective,
		ScopeKind: review.ScopeGlobal, ScopeValue: "*", EvidenceBasis: []review.Basis{review.BasisExplicitRemember},
		Text: text, TextSHA256: textSHA, RuleChangeAuthorization: "not_granted", Privacy: portable.PortablePrivacy,
	}
	identity := revision
	identity.Text, identity.RevisionID = "", ""
	revisionData, _ := json.Marshal(identity)
	revisionDigest := sha256.Sum256(revisionData)
	revision.RevisionID = "portable-revision-" + hex.EncodeToString(revisionDigest[:])
	data, err := portable.RenderRevision(revision)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash := strings.TrimPrefix(revision.MemoryID, "memory-")
	revisionHash := strings.TrimPrefix(revision.RevisionID, "portable-revision-")
	path := filepath.Join(root, "memories", memoryHash[:2], memoryHash, revisionHash+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return revision
}

func schemaReviewPacketFixture(t *testing.T) (*ledger.Store, candidates.Generation) {
	t.Helper()
	root := t.TempDir()
	store, err := ledger.Init(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "rollout.jsonl")
	rollout := `{"timestamp":"2026-08-13T00:00:00Z","type":"session_meta","payload":{"id":"11111111-2222-3333-4444-555555555555"}}
{"timestamp":"2026-08-13T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"Please remember that generated reports stay local."}}
{"timestamp":"2026-08-13T00:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"Understood."}}
`
	if err := os.WriteFile(source, []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codex.ImportPath(store, source, codex.Options{}); err != nil {
		t.Fatal(err)
	}
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
	generation, err := candidates.OpenGeneration(store, candidateResult.GenerationPath)
	if err != nil {
		t.Fatal(err)
	}
	return store, generation
}
