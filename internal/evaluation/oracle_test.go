package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func TestIndependentOracleReplayMatchesEvidenceBoundVerdict(t *testing.T) {
	store, request, receipt, registryPath := recordedOracleAttempt(t, "pass")
	registry, err := loadOracleRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := rerunTaskAttempt(store, receipt, records, ordered, registry); err != nil {
		t.Fatalf("independent replay rejected the recorded verdict: %v", err)
	}
	if request.Oracle.RegistryEntrySHA256 == "" || receipt.TaskSpecBlob == nil {
		t.Fatal("attempt did not retain its oracle and exact-task bindings")
	}
}

func TestIndependentOracleReplayRejectsChangedVerdict(t *testing.T) {
	store, _, receipt, registryPath := recordedOracleAttempt(t, "fail")
	registry, err := loadOracleRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	records, ordered, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	err = rerunTaskAttempt(store, receipt, records, ordered, registry)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed oracle verdict was accepted: %v", err)
	}
}

func TestOracleRegistryRejectsExecutableHashMismatch(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	entry := OracleRegistryEntry{Kind: "harness", ID: "attempt-test", Version: "attempt-test/v1",
		Executable: executable, ExecutableSHA256: strings.Repeat("0", 64),
		Arguments: []string{}, TimeoutSeconds: 30}
	path := writeOracleRegistry(t, entry)
	if _, err := loadOracleRegistry(path); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("registry accepted a changed executable: %v", err)
	}
}

func TestBuiltinExactOracleIsBlindToConditionIdentifiersAndOrderSensitive(t *testing.T) {
	firstPayload := []byte(`{"result":"first"}`)
	secondPayload := []byte(`{"result":"second"}`)
	userPayload := []byte(`{"message":"correction"}`)
	resultExpectations := []builtinResultExpectation{
		{PayloadSHA256: sha256Hex(firstPayload), Label: ResultLabelSuccess},
		{PayloadSHA256: sha256Hex(secondPayload), Label: ResultLabelError},
	}
	sort.Slice(resultExpectations, func(i, j int) bool {
		return resultExpectations[i].PayloadSHA256 < resultExpectations[j].PayloadSHA256
	})
	criteria := builtinEvidenceScoreCriteria{
		SchemaVersion: builtinEvidenceScoreCriteriaSchema,
		AllowedEventOrders: [][]builtinEventExpectation{
			{
				{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(firstPayload)},
				{Kind: ledger.KindUserMessage, PayloadSHA256: sha256Hex(userPayload)},
			},
			{{Kind: ledger.KindToolResult, PayloadSHA256: sha256Hex(secondPayload)}},
		},
		Results: resultExpectations,
		Users: []builtinUserExpectation{
			{PayloadSHA256: sha256Hex(userPayload), Label: UserMessageCorrection},
		},
		PassThreshold: 1,
	}
	criteriaData, err := json.Marshal(criteria)
	if err != nil {
		t.Fatal(err)
	}
	results := []OracleReplayEvent{
		{EventID: "memory-treatment-result", Kind: ledger.KindToolResult, Payload: firstPayload},
	}
	users := []OracleReplayEvent{
		{EventID: "memory-treatment-user", Kind: ledger.KindUserMessage, Payload: userPayload},
	}
	ordered := []OracleReplayEvent{results[0], users[0]}
	judgment, err := runBuiltinEvidenceScoreOracle(criteriaData, ordered, results, users)
	if err != nil || judgment.Verdict != TaskVerdictPass ||
		judgment.ResultLabels[0] != ResultLabelSuccess || judgment.UserLabels[0] != UserMessageCorrection {
		t.Fatalf("blind evidence-score oracle rejected matching payloads: %+v, %v", judgment, err)
	}
	results[0].EventID = "baseline-control-result-renamed"
	ordered[0].EventID = results[0].EventID
	if _, err := runBuiltinEvidenceScoreOracle(criteriaData, ordered, results, users); err != nil {
		t.Fatalf("oracle used a condition-bearing event id: %v", err)
	}
	ordered[0], ordered[1] = ordered[1], ordered[0]
	if _, err := runBuiltinEvidenceScoreOracle(criteriaData, ordered, results, users); err == nil ||
		!strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("oracle accepted observations in the wrong causal order: %v", err)
	}
	failed, err := runBuiltinEvidenceScoreOracle(criteriaData,
		[]OracleReplayEvent{{Kind: ledger.KindToolResult, Payload: secondPayload}},
		[]OracleReplayEvent{{Kind: ledger.KindToolResult, Payload: secondPayload}}, nil)
	if err != nil || failed.Verdict != TaskVerdictFail || failed.Score != 0 {
		t.Fatalf("scorer did not derive a lower baseline result: %+v, %v", failed, err)
	}
}

func TestReplayEventsUsesLedgerOrderNotEventIDOrder(t *testing.T) {
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(900, 0).UTC()
	appendPopulationEvent(t, store, "z-first", ledger.KindToolResult, ledger.AgentCodex,
		"oracle-test", "oracle-test/v1", "ordered", `{"step":1}`, now)
	appendPopulationEvent(t, store, "a-second", ledger.KindToolResult, ledger.AgentCodex,
		"oracle-test", "oracle-test/v1", "ordered", `{"step":2}`, now.Add(time.Second))
	records, _, err := loadIndexedRecords(store)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := replayEvents(store, []BoundEventReference{
		boundReference(records["a-second"].Record), boundReference(records["z-first"].Record),
	}, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || replayed[0].EventID != "z-first" || replayed[1].EventID != "a-second" {
		t.Fatalf("oracle replay lost ledger order: %+v", replayed)
	}
}

func TestOracleReplayHelper(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "oracle-replay-helper" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
		}
	}
	if mode == "" {
		return
	}
	inputData, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var input OracleReplayInput
	if err := json.Unmarshal(inputData, &input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	verdict := TaskAttemptVerdict{
		SchemaVersion: TaskAttemptVerdictSchema, TaskID: input.TaskID, AttemptID: input.AttemptID,
		Verdict: TaskVerdictPass, Score: 1, TokenCountEvaluated: true, TotalTokens: 7,
		ResultEvents: []LabeledResultEvent{}, UserMessages: []LabeledUserMessage{},
		TaskSpecSHA256: sha256Hex(input.TaskSpec), CriteriaSHA256: sha256Hex(input.AcceptanceCriteria),
		ConfigSHA256: sha256Hex(input.ExecutionConfig), Privacy: "local_only",
	}
	for _, event := range input.ResultEvents {
		label := ResultLabelSuccess
		if mode == "payload" && strings.Contains(string(event.Payload), "failed") {
			label = ResultLabelError
			verdict.Verdict, verdict.Score = TaskVerdictFail, 0.5
		}
		if mode == "fail" {
			label = ResultLabelError
			verdict.Verdict, verdict.Score = TaskVerdictFail, 0
		}
		verdict.ResultEvents = append(verdict.ResultEvents, LabeledResultEvent{EventID: event.EventID, Label: label})
	}
	for _, event := range input.UserMessages {
		verdict.UserMessages = append(verdict.UserMessages,
			LabeledUserMessage{EventID: event.EventID, Label: UserMessageNotCorrection})
	}
	if err := json.NewEncoder(os.Stdout).Encode(verdict); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func recordedOracleAttempt(t *testing.T, replayMode string) (*ledger.Store, TaskAttemptRequest,
	TaskAttemptReceipt, string) {
	t.Helper()
	store, err := ledger.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	entry := OracleRegistryEntry{Kind: "harness", ID: "attempt-test", Version: "attempt-test/v1",
		Executable: executable, ExecutableSHA256: digest,
		Arguments:      []string{"-test.run=^TestOracleReplayHelper$", "--", "oracle-replay-helper", replayMode},
		TimeoutSeconds: 30}
	registryPath := writeOracleRegistry(t, entry)

	request := baselineAttemptRequest("oracle-start", "oracle-result", "oracle-verdict")
	request.Oracle.RegistryEntrySHA256 = oracleEntrySHA256(entry)
	request.TaskSpecBlob = putAttemptBlob(t, store, "task")
	request.AcceptanceCriteriaBlob = putAttemptBlob(t, store, "criteria")
	request.ExecutionConfigBlob = putAttemptBlob(t, store, "config")
	request.TaskSpecSHA256 = request.TaskSpecBlob.SHA256
	request.AcceptanceCriteriaSHA256 = request.AcceptanceCriteriaBlob.SHA256
	request.ExecutionConfigSHA256 = request.ExecutionConfigBlob.SHA256
	now := time.Unix(1_000, 0).UTC()
	appendAttemptContract(t, store, request, now)
	appendAttemptEvent(t, store, request.WindowEndEventID, ledger.KindToolResult, "passed",
		now.Add(time.Second), []string{request.WindowStartEventID})
	verdict := passingAttemptVerdict(request)
	verdict.TotalTokens = 7
	verdict.TokenCountEvaluated = true
	appendAttemptVerdict(t, store, request, verdict, now.Add(2*time.Second), request.WindowEndEventID)
	result, err := RecordTaskAttempt(store, request, func() time.Time { return now.Add(3 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	return store, request, result.Receipt, registryPath
}

func putAttemptBlob(t *testing.T, store *ledger.Store, content string) *ledger.BlobRef {
	t.Helper()
	reference, err := store.PutBlob(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return &reference
}

func writeOracleRegistry(t *testing.T, entry OracleRegistryEntry) string {
	t.Helper()
	registry := OracleRegistry{SchemaVersion: OracleRegistrySchema,
		Entries: []OracleRegistryEntry{entry}, Privacy: "local_only"}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oracle-registry.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
