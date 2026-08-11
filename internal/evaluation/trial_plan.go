package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	EvaluationTrialPlanSchema       = "evaluation-trial-plan/v1alpha1"
	TrialPlanPreregistrationSchema  = "evaluation-trial-plan-preregistration/v1alpha1"
	TrialCorpusSelectionSchema      = "evaluation-trial-corpus-selection/v1alpha1"
	continuousTrialCorpusSampleSize = 20
)

type TrialPlanPairInput struct {
	PairID             string
	TaskID             string
	Agent              ledger.Agent
	CorpusArtifactID   string
	SemanticKeySHA256  string
	BaselineThreadID   string
	BaselineSessionID  string
	TreatmentThreadID  string
	TreatmentSessionID string
	TaskSpec           []byte
	AcceptanceCriteria []byte
	ExecutionConfig    []byte
	SystemUnderTest    []byte
}

type TrialPlanPreregisterOptions struct {
	SuiteID            string
	CorpusID           string
	Pairs              []TrialPlanPairInput
	OracleRegistryPath string
	Now                func() time.Time
}

type TrialPlanArm struct {
	Condition TaskCondition       `json:"condition"`
	ThreadID  string              `json:"thread_id"`
	SessionID string              `json:"session_id"`
	Contract  TaskAttemptContract `json:"contract"`
}

type TrialCorpusSelectionItem struct {
	ArtifactID     string         `json:"artifact_id"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	Blob           ledger.BlobRef `json:"blob"`
	Agent          ledger.Agent   `json:"agent"`
	PairID         string         `json:"pair_id"`
	TaskID         string         `json:"task_id"`
}

type TrialCorpusSelection struct {
	SchemaVersion       string                     `json:"schema_version"`
	CorpusID            string                     `json:"corpus_id"`
	CorpusContentSHA256 string                     `json:"corpus_content_sha256"`
	SeedSHA256          string                     `json:"seed_sha256"`
	Items               []TrialCorpusSelectionItem `json:"items"`
	Privacy             string                     `json:"privacy"`
}

type TrialPlanPair struct {
	PairID               string          `json:"pair_id"`
	CorpusArtifactID     string          `json:"corpus_artifact_id"`
	CorpusArtifactSHA256 string          `json:"corpus_artifact_sha256"`
	ExecutionOrder       []TaskCondition `json:"execution_order"`
	Baseline             TrialPlanArm    `json:"baseline"`
	Treatment            TrialPlanArm    `json:"treatment"`
}

type EvaluationTrialPlan struct {
	SchemaVersion        string          `json:"schema_version"`
	PlanID               string          `json:"plan_id"`
	SuiteID              string          `json:"suite_id"`
	CorpusID             string          `json:"corpus_id"`
	CorpusContentSHA256  string          `json:"corpus_content_sha256"`
	SeedSHA256           string          `json:"seed_sha256"`
	SystemArtifactSHA256 string          `json:"system_artifact_sha256"`
	OracleRegistrySHA256 string          `json:"oracle_registry_sha256"`
	CreatedAt            time.Time       `json:"created_at"`
	Pairs                []TrialPlanPair `json:"pairs"`
	Privacy              string          `json:"privacy"`
}

type TrialPlanPreregistration struct {
	SchemaVersion string                       `json:"schema_version"`
	Plan          EvaluationTrialPlan          `json:"plan"`
	EventID       string                       `json:"event_id"`
	RecordHash    string                       `json:"record_hash"`
	Attempts      []TaskAttemptPreregistration `json:"attempts"`
	Privacy       string                       `json:"privacy"`
}

type trialPlanIdentityPair struct {
	PairID                    string       `json:"pair_id"`
	CorpusArtifactID          string       `json:"corpus_artifact_id"`
	CorpusArtifactSHA256      string       `json:"corpus_artifact_sha256"`
	Agent                     ledger.Agent `json:"agent"`
	TaskID                    string       `json:"task_id"`
	SemanticKeySHA256         string       `json:"semantic_key_sha256"`
	TaskSpecSHA256            string       `json:"task_spec_sha256"`
	AcceptanceCriteriaSHA256  string       `json:"acceptance_criteria_sha256"`
	ExecutionConfigSHA256     string       `json:"execution_config_sha256"`
	SystemUnderTestSHA256     string       `json:"system_under_test_sha256"`
	OracleRegistryEntrySHA256 string       `json:"oracle_registry_entry_sha256"`
	BaselineThreadID          string       `json:"baseline_thread_id"`
	BaselineSessionID         string       `json:"baseline_session_id"`
	TreatmentThreadID         string       `json:"treatment_thread_id"`
	TreatmentSessionID        string       `json:"treatment_session_id"`
}

func SelectTrialCorpus(store *ledger.Store, corpusID string) (TrialCorpusSelection, error) {
	result := TrialCorpusSelection{SchemaVersion: TrialCorpusSelectionSchema,
		CorpusID: corpusID, Items: []TrialCorpusSelectionItem{}, Privacy: "local_only"}
	if store == nil || !validCorpusID(corpusID) {
		return result, errors.New("store and verified frozen corpus are required")
	}
	verification := VerifyCorpus(store, corpusID)
	if len(verification.Issues) != 0 {
		return result, errors.New("trial selection frozen corpus verification failed")
	}
	corpus, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		return result, err
	}
	result.CorpusContentSHA256 = corpus.CorpusContentSHA256
	result.SeedSHA256 = trialCorpusAssignmentSHA256(corpus)
	for _, selected := range selectedTrialCorpusArtifacts(corpus, result.SeedSHA256) {
		result.Items = append(result.Items, TrialCorpusSelectionItem{
			ArtifactID: selected.Artifact.ArtifactID, ArtifactSHA256: selected.Artifact.Blob.SHA256,
			Blob:   selected.Artifact.Blob,
			Agent:  selected.Agent,
			PairID: corpusTrialPairID(corpus.CorpusID, result.SeedSHA256, selected.Artifact),
			TaskID: corpusTrialTaskID(corpus.CorpusID, selected.Artifact),
		})
	}
	if len(result.Items) == 0 {
		return result, errors.New("frozen corpus has no eligible legacy-card trial artifacts")
	}
	return result, nil
}

func PreregisterTrialPlan(store *ledger.Store, options TrialPlanPreregisterOptions) (
	TrialPlanPreregistration, error,
) {
	result := TrialPlanPreregistration{SchemaVersion: TrialPlanPreregistrationSchema,
		Attempts: []TaskAttemptPreregistration{}, Privacy: "local_only"}
	if store == nil || !safeIdentifier(options.SuiteID) || !validCorpusID(options.CorpusID) ||
		len(options.Pairs) == 0 {
		return result, errors.New("store, suite id, frozen corpus, and at least one trial pair are required")
	}
	verification := VerifyCorpus(store, options.CorpusID)
	if len(verification.Issues) != 0 {
		return result, errors.New("trial plan frozen corpus verification failed")
	}
	corpus, err := LoadCorpusManifest(store, options.CorpusID)
	if err != nil {
		return result, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	seedSHA := trialCorpusAssignmentSHA256(corpus)
	registry, err := loadOracleRegistry(options.OracleRegistryPath)
	if err != nil {
		return result, err
	}
	entry, exists := registry.Entries[oracleRegistryKey("builtin", "evidence-score", "v1")]
	if !exists {
		return result, errors.New("builtin evidence-score registry entry is required")
	}
	systemSHA, err := CurrentSystemArtifactSHA256()
	if err != nil {
		return result, err
	}
	selectedArtifacts := selectedTrialCorpusArtifacts(corpus, seedSHA)
	selectedByID := map[string]selectedTrialArtifact{}
	for _, selected := range selectedArtifacts {
		selectedByID[selected.Artifact.ArtifactID] = selected
	}

	inputs := append([]TrialPlanPairInput{}, options.Pairs...)
	for index := range inputs {
		selected, exists := selectedByID[inputs[index].CorpusArtifactID]
		if !exists {
			continue
		}
		if inputs[index].PairID == "" {
			inputs[index].PairID = corpusTrialPairID(corpus.CorpusID, seedSHA, selected.Artifact)
		}
		if inputs[index].TaskID == "" {
			inputs[index].TaskID = corpusTrialTaskID(corpus.CorpusID, selected.Artifact)
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].PairID < inputs[j].PairID })
	identities := make([]trialPlanIdentityPair, 0, len(inputs))
	prepared := make([]struct {
		input                       TrialPlanPairInput
		task, criteria, config, sut ledger.BlobRef
	}, 0, len(inputs))
	for index, input := range inputs {
		selected, selectedExists := selectedByID[input.CorpusArtifactID]
		expectedPairID := corpusTrialPairID(corpus.CorpusID, seedSHA, selected.Artifact)
		expectedTaskID := corpusTrialTaskID(corpus.CorpusID, selected.Artifact)
		artifactData, artifactErr := readAttemptBlob(store, selected.Artifact.Blob)
		if !selectedExists || artifactErr != nil || input.PairID != expectedPairID ||
			input.TaskID != expectedTaskID || input.Agent != selected.Agent ||
			!bytes.Equal(input.TaskSpec, artifactData) ||
			(index > 0 && inputs[index-1].PairID >= input.PairID) ||
			!validAgent(input.Agent) || input.Agent == ledger.AgentUnknown ||
			!safeIdentifier(input.CorpusArtifactID) ||
			!validSHA256(input.SemanticKeySHA256) || strings.TrimSpace(input.BaselineThreadID) == "" ||
			strings.TrimSpace(input.BaselineSessionID) == "" || strings.TrimSpace(input.TreatmentThreadID) == "" ||
			strings.TrimSpace(input.TreatmentSessionID) == "" {
			return result, errors.New("trial pair identity and separate arm contexts are required")
		}
		for name, data := range map[string][]byte{"task spec": input.TaskSpec,
			"acceptance criteria": input.AcceptanceCriteria, "execution config": input.ExecutionConfig,
			"system-under-test manifest": input.SystemUnderTest} {
			if len(data) == 0 {
				return result, fmt.Errorf("trial pair %s %s is empty", input.PairID, name)
			}
		}
		put := func(data []byte) (ledger.BlobRef, error) { return store.PutBlob(bytes.NewReader(data)) }
		task, putErr := put(input.TaskSpec)
		if putErr != nil {
			return result, putErr
		}
		criteria, putErr := put(input.AcceptanceCriteria)
		if putErr != nil {
			return result, putErr
		}
		config, putErr := put(input.ExecutionConfig)
		if putErr != nil {
			return result, putErr
		}
		sut, putErr := put(input.SystemUnderTest)
		if putErr != nil {
			return result, putErr
		}
		request := TaskAttemptRequest{SchemaVersion: TaskAttemptRequestSchema, TaskID: input.TaskID,
			AttemptID: "planned-placeholder", Agent: input.Agent, SemanticKeySHA256: input.SemanticKeySHA256,
			TaskSpecSHA256: task.SHA256, AcceptanceCriteriaSHA256: criteria.SHA256,
			ExecutionConfigSHA256: config.SHA256, SystemArtifactSHA256: systemSHA,
			SystemUnderTestSHA256: sut.SHA256, SystemUnderTestBlob: &sut, TaskSpecBlob: &task,
			AcceptanceCriteriaBlob: &criteria, ExecutionConfigBlob: &config, Condition: TaskConditionBaseline,
			WindowStartEventID: "planned-start", WindowEndEventID: "planned-result", MemoryReferences: []retrieval.MemoryReference{},
			Oracle: TaskOracle{Kind: "builtin", ID: "evidence-score", Version: "v1",
				RegistryEntrySHA256: oracleEntrySHA256(entry), VerdictEventID: "planned-verdict"}, Privacy: "local_only"}
		if err := validateAttemptBlobs(store, request); err != nil {
			return result, err
		}
		identities = append(identities, trialPlanIdentityPair{PairID: input.PairID,
			CorpusArtifactID: selected.Artifact.ArtifactID, CorpusArtifactSHA256: selected.Artifact.Blob.SHA256,
			Agent:  input.Agent,
			TaskID: input.TaskID, SemanticKeySHA256: input.SemanticKeySHA256, TaskSpecSHA256: task.SHA256,
			AcceptanceCriteriaSHA256: criteria.SHA256, ExecutionConfigSHA256: config.SHA256,
			SystemUnderTestSHA256: sut.SHA256, OracleRegistryEntrySHA256: oracleEntrySHA256(entry),
			BaselineThreadID: input.BaselineThreadID, BaselineSessionID: input.BaselineSessionID,
			TreatmentThreadID: input.TreatmentThreadID, TreatmentSessionID: input.TreatmentSessionID})
		prepared = append(prepared, struct {
			input                       TrialPlanPairInput
			task, criteria, config, sut ledger.BlobRef
		}{
			input: input, task: task, criteria: criteria, config: config, sut: sut})
	}
	identityData, _ := json.Marshal(identities)
	planID := adapterjsonl.DeterministicID("evaluation-trial-plan", options.SuiteID,
		options.CorpusID, corpus.CorpusContentSHA256, seedSHA, systemSHA, registry.SHA256,
		sha256Hex(identityData))
	now := options.Now().UTC()
	plan := EvaluationTrialPlan{SchemaVersion: EvaluationTrialPlanSchema, PlanID: planID,
		SuiteID: options.SuiteID, CorpusID: options.CorpusID, CorpusContentSHA256: corpus.CorpusContentSHA256,
		SeedSHA256: seedSHA, SystemArtifactSHA256: systemSHA,
		OracleRegistrySHA256: registry.SHA256, CreatedAt: now, Pairs: []TrialPlanPair{},
		Privacy: "local_only"}
	requests := []TaskAttemptRequest{}
	contexts := [][2]string{}
	for _, item := range prepared {
		makeRequest := func(condition TaskCondition) TaskAttemptRequest {
			attemptID := adapterjsonl.DeterministicID("planned-task-attempt", planID, item.input.PairID, string(condition))
			request := TaskAttemptRequest{SchemaVersion: TaskAttemptRequestSchema, TaskID: item.input.TaskID,
				AttemptID: attemptID, TrialPlanID: planID, TrialPairID: item.input.PairID, Agent: item.input.Agent,
				SemanticKeySHA256: item.input.SemanticKeySHA256, TaskSpecSHA256: item.task.SHA256,
				AcceptanceCriteriaSHA256: item.criteria.SHA256, ExecutionConfigSHA256: item.config.SHA256,
				SystemArtifactSHA256: systemSHA, SystemUnderTestSHA256: item.sut.SHA256,
				SystemUnderTestBlob: &item.sut, TaskSpecBlob: &item.task, AcceptanceCriteriaBlob: &item.criteria,
				ExecutionConfigBlob: &item.config, Condition: condition, MemoryReferences: []retrieval.MemoryReference{},
				Oracle: TaskOracle{Kind: "builtin", ID: "evidence-score", Version: "v1",
					RegistryEntrySHA256: oracleEntrySHA256(entry)}, Privacy: "local_only"}
			request.WindowStartEventID = adapterjsonl.DeterministicID("task-attempt-contract", planID, item.input.PairID, string(condition))
			request.WindowEndEventID = adapterjsonl.DeterministicID("task-attempt-result", request.WindowStartEventID)
			request.Oracle.VerdictEventID = adapterjsonl.DeterministicID("task-attempt-verdict", request.WindowStartEventID)
			return request
		}
		baseline, treatment := makeRequest(TaskConditionBaseline), makeRequest(TaskConditionMemory)
		if err := validatePreregisteredTaskAttemptRequest(baseline); err != nil {
			return result, err
		}
		if err := validatePreregisteredTaskAttemptRequest(treatment); err != nil {
			return result, err
		}
		order := []TaskCondition{TaskConditionBaseline, TaskConditionMemory}
		if assignmentDigest(seedSHA, item.input.PairID)[0]&1 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		selected := selectedByID[item.input.CorpusArtifactID]
		plan.Pairs = append(plan.Pairs, TrialPlanPair{PairID: item.input.PairID,
			CorpusArtifactID: selected.Artifact.ArtifactID, CorpusArtifactSHA256: selected.Artifact.Blob.SHA256, ExecutionOrder: order,
			Baseline: TrialPlanArm{Condition: TaskConditionBaseline, ThreadID: item.input.BaselineThreadID,
				SessionID: item.input.BaselineSessionID, Contract: NewTaskAttemptContract(baseline)},
			Treatment: TrialPlanArm{Condition: TaskConditionMemory, ThreadID: item.input.TreatmentThreadID,
				SessionID: item.input.TreatmentSessionID, Contract: NewTaskAttemptContract(treatment)}})
		requests = append(requests, baseline, treatment)
		contexts = append(contexts, [2]string{item.input.BaselineThreadID, item.input.BaselineSessionID},
			[2]string{item.input.TreatmentThreadID, item.input.TreatmentSessionID})
	}
	if err := validateEvaluationTrialPlan(plan); err != nil {
		return result, err
	}
	freezeEventID, err := verifiedCorpusFreezeEventID(store, plan.CorpusID)
	if err != nil {
		return result, err
	}
	planData, err := marshalIndented(plan)
	if err != nil {
		return result, err
	}
	planPayload := ledger.InlinePayload("utf-8", "application/json", string(planData))
	events := []ledger.Event{{SchemaVersion: ledger.SchemaVersion, EventID: plan.PlanID,
		Kind: ledger.KindEvaluationTrialPlan, ObservedAt: now, RecordedAt: now,
		Source: ledger.Source{Agent: ledger.AgentUnknown, Adapter: evaluationAdapterName,
			AdapterVersion: evaluationAdapterVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: plan.PlanID, SessionID: plan.PlanID, SourceEventID: plan.PlanID,
			SourceCursor: "evaluation-trial-plan:" + plan.PlanID},
		Payload: &planPayload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Causality: &ledger.Causality{ParentEventIDs: []string{freezeEventID}},
		Privacy:   ledger.Privacy{Classification: "local_only"}}}
	for index, request := range requests {
		data, marshalErr := marshalIndented(NewTaskAttemptContract(request))
		if marshalErr != nil {
			return result, marshalErr
		}
		payload := ledger.InlinePayload("utf-8", "application/json", string(data))
		events = append(events, ledger.Event{SchemaVersion: ledger.SchemaVersion, EventID: request.WindowStartEventID,
			Kind: ledger.KindTaskAttemptContract, ObservedAt: now, RecordedAt: now,
			Source: ledger.Source{Agent: request.Agent, Adapter: request.Oracle.ID,
				AdapterVersion: request.Oracle.Version, DeviceID: store.DeviceID(), OS: runtime.GOOS,
				ThreadID: contexts[index][0], SessionID: contexts[index][1], SourceEventID: request.AttemptID,
				SourceCursor: "task-attempt-contract:" + request.AttemptID},
			Payload: &payload, Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
			Causality: &ledger.Causality{ParentEventIDs: []string{plan.PlanID}},
			Privacy:   ledger.Privacy{Classification: "local_only"}})
	}
	known := map[string]struct{}{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		known[record.Event.EventID] = struct{}{}
		return nil
	})
	if err != nil {
		return result, err
	}
	for _, event := range events {
		if _, duplicate := known[event.EventID]; duplicate {
			_ = appender.Close()
			return result, fmt.Errorf("trial plan event already exists: %s", event.EventID)
		}
	}
	records, appendErr := appender.AppendBatch(events)
	closeErr := appender.Close()
	if appendErr != nil || closeErr != nil {
		return result, errors.Join(appendErr, closeErr)
	}
	result.Plan, result.EventID, result.RecordHash = plan, plan.PlanID, records[0].RecordHash
	for index, request := range requests {
		result.Attempts = append(result.Attempts, TaskAttemptPreregistration{SchemaVersion: TaskAttemptPreregisterSchema,
			Request: request, EventID: request.WindowStartEventID, RecordHash: records[index+1].RecordHash,
			Privacy: "local_only"})
	}
	return result, nil
}

func assignmentDigest(seedSHA, pairID string) [32]byte {
	return sha256.Sum256([]byte(seedSHA + "\x00" + pairID))
}

func trialCorpusAssignmentSHA256(corpus CorpusManifest) string {
	return sha256Hex([]byte(ContinuousLearningPolicyV1 + "\x00" + corpus.CorpusContentSHA256))
}

func validateEvaluationTrialPlan(plan EvaluationTrialPlan) error {
	if plan.SchemaVersion != EvaluationTrialPlanSchema || !safeIdentifier(plan.PlanID) ||
		!safeIdentifier(plan.SuiteID) || !validCorpusID(plan.CorpusID) ||
		!validSHA256(plan.CorpusContentSHA256) || !validSHA256(plan.SeedSHA256) ||
		!validSHA256(plan.SystemArtifactSHA256) || !validSHA256(plan.OracleRegistrySHA256) ||
		plan.CreatedAt.IsZero() || len(plan.Pairs) == 0 ||
		plan.Privacy != "local_only" {
		return errors.New("evaluation trial plan envelope is invalid")
	}
	for index, pair := range plan.Pairs {
		expectedOrder := []TaskCondition{TaskConditionBaseline, TaskConditionMemory}
		if assignmentDigest(plan.SeedSHA256, pair.PairID)[0]&1 == 1 {
			expectedOrder[0], expectedOrder[1] = expectedOrder[1], expectedOrder[0]
		}
		if !safeIdentifier(pair.PairID) || !safeIdentifier(pair.CorpusArtifactID) ||
			!validSHA256(pair.CorpusArtifactSHA256) ||
			(index > 0 && plan.Pairs[index-1].PairID >= pair.PairID) ||
			len(pair.ExecutionOrder) != 2 || pair.ExecutionOrder[0] == pair.ExecutionOrder[1] ||
			!reflect.DeepEqual(pair.ExecutionOrder, expectedOrder) ||
			!validTrialPlanArm(plan, pair, pair.Baseline, TaskConditionBaseline) ||
			!validTrialPlanArm(plan, pair, pair.Treatment, TaskConditionMemory) {
			return errors.New("evaluation trial plan pair is invalid")
		}
		left, right := pair.Baseline.Contract, pair.Treatment.Contract
		left.Condition, right.Condition = "", ""
		left.AttemptID, right.AttemptID = "", ""
		left.WindowEndEventID, right.WindowEndEventID = "", ""
		left.Oracle.VerdictEventID, right.Oracle.VerdictEventID = "", ""
		if !reflect.DeepEqual(left, right) {
			return errors.New("evaluation trial plan arms do not bind the same task and system")
		}
	}
	identities := trialPlanIdentities(plan)
	identityData, _ := json.Marshal(identities)
	expectedID := adapterjsonl.DeterministicID("evaluation-trial-plan", plan.SuiteID,
		plan.CorpusID, plan.CorpusContentSHA256, plan.SeedSHA256, plan.SystemArtifactSHA256,
		plan.OracleRegistrySHA256, sha256Hex(identityData))
	if expectedID != plan.PlanID {
		return errors.New("evaluation trial plan id does not match its complete contents")
	}
	return nil
}

func validTrialPlanArm(plan EvaluationTrialPlan, pair TrialPlanPair, arm TrialPlanArm,
	condition TaskCondition) bool {
	contract := arm.Contract
	startID := contractEventID(contract)
	attemptID := adapterjsonl.DeterministicID("planned-task-attempt", plan.PlanID,
		pair.PairID, string(condition))
	return arm.Condition == condition && contract.Condition == condition &&
		contract.TrialPlanID == plan.PlanID && contract.TrialPairID == pair.PairID &&
		contract.AttemptID == attemptID &&
		contract.WindowEndEventID == adapterjsonl.DeterministicID("task-attempt-result", startID) &&
		contract.Oracle.VerdictEventID == adapterjsonl.DeterministicID("task-attempt-verdict", startID) &&
		strings.TrimSpace(arm.ThreadID) != "" && strings.TrimSpace(arm.SessionID) != "" &&
		contract.SystemArtifactSHA256 == plan.SystemArtifactSHA256
}

func trialPlanIdentities(plan EvaluationTrialPlan) []trialPlanIdentityPair {
	result := make([]trialPlanIdentityPair, 0, len(plan.Pairs))
	for _, pair := range plan.Pairs {
		contract := pair.Baseline.Contract
		result = append(result, trialPlanIdentityPair{PairID: pair.PairID,
			CorpusArtifactID: pair.CorpusArtifactID, CorpusArtifactSHA256: pair.CorpusArtifactSHA256,
			Agent:  contract.Agent,
			TaskID: contract.TaskID, SemanticKeySHA256: contract.SemanticKeySHA256,
			TaskSpecSHA256:            contract.TaskSpecSHA256,
			AcceptanceCriteriaSHA256:  contract.AcceptanceCriteriaSHA256,
			ExecutionConfigSHA256:     contract.ExecutionConfigSHA256,
			SystemUnderTestSHA256:     contract.SystemUnderTestSHA256,
			OracleRegistryEntrySHA256: contract.Oracle.RegistryEntrySHA256,
			BaselineThreadID:          pair.Baseline.ThreadID, BaselineSessionID: pair.Baseline.SessionID,
			TreatmentThreadID: pair.Treatment.ThreadID, TreatmentSessionID: pair.Treatment.SessionID})
	}
	return result
}

type populationPlannedArm struct {
	PlanID    string
	PairID    string
	Arm       TrialPlanArm
	PlanEvent indexedRecord
}

type selectedTrialArtifact struct {
	Artifact CorpusArtifact
	Agent    ledger.Agent
	Rank     string
}

func selectedTrialCorpusArtifacts(corpus CorpusManifest, seedSHA string) []selectedTrialArtifact {
	selected := []selectedTrialArtifact{}
	for _, artifact := range corpus.Artifacts {
		if artifact.Role != RoleLegacyCard {
			continue
		}
		selected = append(selected, selectedTrialArtifact{Artifact: artifact,
			Rank: sha256Hex([]byte(seedSHA + "\x00" + artifact.ArtifactID + "\x00" + artifact.Blob.SHA256))})
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].Rank != selected[j].Rank {
			return selected[i].Rank < selected[j].Rank
		}
		return selected[i].Artifact.ArtifactID < selected[j].Artifact.ArtifactID
	})
	if len(selected) > continuousTrialCorpusSampleSize {
		selected = selected[:continuousTrialCorpusSampleSize]
	}
	agents := []ledger.Agent{ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode}
	for index := range selected {
		selected[index].Agent = agents[index%len(agents)]
	}
	return selected
}

func corpusTrialPairID(corpusID, seedSHA string, artifact CorpusArtifact) string {
	return adapterjsonl.DeterministicID("corpus-trial-pair", corpusID, seedSHA,
		artifact.ArtifactID, artifact.Blob.SHA256)
}

func corpusTrialTaskID(corpusID string, artifact CorpusArtifact) string {
	return adapterjsonl.DeterministicID("corpus-trial-task", corpusID,
		artifact.ArtifactID, artifact.Blob.SHA256)
}

func loadPopulationTrialPlans(store *ledger.Store, records map[string]indexedRecord,
	ordered []ledger.Record, population *EvaluationPopulation) (map[string]populationPlannedArm, bool) {
	planned := map[string]populationPlannedArm{}
	issuesBefore := len(population.PopulationIssues)
	corpusFreezeID, corpusErr := verifiedCorpusFreezeEventID(store, population.CorpusID)
	if corpusErr != nil {
		population.PopulationIssues = append(population.PopulationIssues,
			"evaluation trial plan corpus binding is unavailable")
		return planned, false
	}
	corpus, corpusLoadErr := LoadCorpusManifest(store, population.CorpusID)
	if corpusLoadErr != nil {
		population.PopulationIssues = append(population.PopulationIssues,
			"evaluation trial corpus manifest is unavailable")
		return planned, false
	}
	populationSeed := ""
	populationSuite := ""
	observedArtifacts := map[string]int{}
	observedPairs := map[string][]TrialPlanPair{}
	plans := 0
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindEvaluationTrialPlan {
			continue
		}
		indexed := records[record.Event.EventID]
		data, err := eventPayload(store, record.Event)
		var plan EvaluationTrialPlan
		if err != nil || decodeStrictEvaluationJSON(data, &plan) != nil ||
			validateEvaluationTrialPlan(plan) != nil || plan.PlanID != record.Event.EventID ||
			plan.CorpusID != population.CorpusID ||
			plan.CorpusContentSHA256 != population.CorpusContentSHA256 ||
			plan.SeedSHA256 != trialCorpusAssignmentSHA256(corpus) ||
			record.Event.Causality == nil ||
			!sameStrings(record.Event.Causality.ParentEventIDs, []string{corpusFreezeID}) ||
			plan.SystemArtifactSHA256 != population.SystemArtifactSHA256 ||
			record.Event.Source.Agent != ledger.AgentUnknown ||
			record.Event.Source.Adapter != evaluationAdapterName ||
			record.Event.Source.AdapterVersion != evaluationAdapterVersion {
			population.PopulationIssues = append(population.PopulationIssues,
				"evaluation trial plan is invalid: "+record.Event.EventID)
			continue
		}
		if populationSeed == "" {
			populationSeed, populationSuite = plan.SeedSHA256, plan.SuiteID
		} else if plan.SeedSHA256 != populationSeed || plan.SuiteID != populationSuite {
			population.PopulationIssues = append(population.PopulationIssues,
				"evaluation trial plans do not share one corpus assignment seed and suite")
		}
		plans++
		expectedIndex := indexed.Index + 1
		for _, pair := range plan.Pairs {
			observedArtifacts[pair.CorpusArtifactID]++
			observedPairs[pair.CorpusArtifactID] = append(observedPairs[pair.CorpusArtifactID], pair)
			for _, arm := range []TrialPlanArm{pair.Baseline, pair.Treatment} {
				contract := arm.Contract
				contractRecord, exists := records[contractEventID(contract)]
				if !exists || contractRecord.Index != expectedIndex ||
					contractRecord.Record.Event.Kind != ledger.KindTaskAttemptContract ||
					contractRecord.Record.Event.Causality == nil ||
					!sameStrings(contractRecord.Record.Event.Causality.ParentEventIDs, []string{plan.PlanID}) ||
					contractRecord.Record.Event.Source.Agent != contract.Agent ||
					contractRecord.Record.Event.Source.ThreadID != arm.ThreadID ||
					contractRecord.Record.Event.Source.SessionID != arm.SessionID {
					population.PopulationIssues = append(population.PopulationIssues,
						"evaluation trial plan contract batch is incomplete: "+contract.AttemptID)
					expectedIndex++
					continue
				}
				contractData, readErr := eventPayload(store, contractRecord.Record.Event)
				var observed TaskAttemptContract
				if readErr != nil || decodeStrictEvaluationJSON(contractData, &observed) != nil ||
					!reflect.DeepEqual(observed, contract) {
					population.PopulationIssues = append(population.PopulationIssues,
						"evaluation trial plan contract differs from its sealed plan: "+contract.AttemptID)
					expectedIndex++
					continue
				}
				if _, duplicate := planned[contract.AttemptID]; duplicate {
					population.PopulationIssues = append(population.PopulationIssues,
						"evaluation trial plan repeats attempt: "+contract.AttemptID)
				} else {
					planned[contract.AttemptID] = populationPlannedArm{PlanID: plan.PlanID,
						PairID: pair.PairID, Arm: arm, PlanEvent: indexed}
				}
				expectedIndex++
			}
			first := pair.Baseline
			second := pair.Treatment
			if pair.ExecutionOrder[0] == TaskConditionMemory {
				first, second = second, first
			}
			firstResult, firstExists := records[first.Contract.WindowEndEventID]
			secondResult, secondExists := records[second.Contract.WindowEndEventID]
			if secondExists && !firstExists {
				population.PopulationIssues = append(population.PopulationIssues,
					"evaluation trial plan executed its second arm before the first: "+pair.PairID)
			}
			if firstExists && secondExists && firstResult.Index >= secondResult.Index {
				population.PopulationIssues = append(population.PopulationIssues,
					"evaluation trial plan arm result order is invalid: "+pair.PairID)
			}
		}
	}
	if populationSeed != "" {
		expected := selectedTrialCorpusArtifacts(corpus, populationSeed)
		expectedByID := map[string]selectedTrialArtifact{}
		for _, item := range expected {
			expectedByID[item.Artifact.ArtifactID] = item
		}
		for artifactID, count := range observedArtifacts {
			selected, exists := expectedByID[artifactID]
			pairs := observedPairs[artifactID]
			validBinding := exists && count == 1 && len(pairs) == 1
			if validBinding {
				pair := pairs[0]
				validBinding = pair.CorpusArtifactSHA256 == selected.Artifact.Blob.SHA256 &&
					pair.PairID == corpusTrialPairID(corpus.CorpusID, populationSeed, selected.Artifact) &&
					pair.Baseline.Contract.TaskID == corpusTrialTaskID(corpus.CorpusID, selected.Artifact) &&
					pair.Baseline.Contract.TaskSpecSHA256 == selected.Artifact.Blob.SHA256 &&
					pair.Baseline.Contract.Agent == selected.Agent && pair.Treatment.Contract.Agent == selected.Agent
			}
			if !validBinding {
				population.PopulationIssues = append(population.PopulationIssues,
					"evaluation trial plan contains an unselected, duplicate, or changed corpus artifact task: "+artifactID)
				continue
			}
			delete(expectedByID, artifactID)
		}
		for artifactID := range expectedByID {
			population.PopulationIssues = append(population.PopulationIssues,
				"evaluation trial plan omits a deterministically selected corpus artifact: "+artifactID)
		}
	}
	if plans == 0 {
		population.PopulationIssues = append(population.PopulationIssues, "sealed evaluation trial plan is unavailable")
		return planned, false
	}
	maxContractIndex, firstResultIndex := 0, len(ordered)+1
	for attemptID, arm := range planned {
		contractIndex := records[contractEventID(arm.Arm.Contract)].Index
		if contractIndex > maxContractIndex {
			maxContractIndex = contractIndex
		}
		if result, exists := records[arm.Arm.Contract.WindowEndEventID]; exists && result.Index < firstResultIndex {
			firstResultIndex = result.Index
		}
		if arm.Arm.Contract.AttemptID != attemptID {
			population.PopulationIssues = append(population.PopulationIssues,
				"evaluation trial plan attempt identity changed: "+attemptID)
		}
	}
	if firstResultIndex <= maxContractIndex {
		population.PopulationIssues = append(population.PopulationIssues,
			"evaluation trial plan was not fully sealed before the first result")
	}
	return planned, len(population.PopulationIssues) == issuesBefore
}

func contractEventID(contract TaskAttemptContract) string {
	return adapterjsonl.DeterministicID("task-attempt-contract", contract.TrialPlanID,
		contract.TrialPairID, string(contract.Condition))
}

func verifiedCorpusFreezeEventID(store *ledger.Store, corpusID string) (string, error) {
	data, err := os.ReadFile(corpusManifestPath(store.Root(), corpusID))
	if err != nil {
		return "", err
	}
	wanted := freezeEventID(corpusID, sha256Hex(data))
	records, _, err := loadIndexedRecords(store)
	if err != nil {
		return "", err
	}
	record, exists := records[wanted]
	if !exists || record.Record.Event.Kind != ledger.KindEvaluationCorpus {
		return "", errors.New("verified corpus freeze event is unavailable")
	}
	return wanted, nil
}
