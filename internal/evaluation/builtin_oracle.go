package evaluation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

const builtinEvidenceScoreCriteriaSchema = "builtin-evidence-score-criteria/v1"

type builtinEvidenceScoreCriteria struct {
	SchemaVersion      string                      `json:"schema_version"`
	AllowedEventOrders [][]builtinEventExpectation `json:"allowed_event_orders"`
	Results            []builtinResultExpectation  `json:"results"`
	Users              []builtinUserExpectation    `json:"users"`
	PassThreshold      float64                     `json:"pass_threshold"`
}

type builtinEventExpectation struct {
	Kind          ledger.EventKind `json:"kind"`
	PayloadSHA256 string           `json:"payload_sha256"`
}

type builtinResultExpectation struct {
	PayloadSHA256 string      `json:"payload_sha256"`
	Label         ResultLabel `json:"label"`
}

type builtinUserExpectation struct {
	PayloadSHA256 string           `json:"payload_sha256"`
	Label         UserMessageLabel `json:"label"`
}

// oracleObservation deliberately excludes task, attempt, condition, event ID,
// record hash, timestamps, and source identity.
type oracleObservation struct {
	Kind          ledger.EventKind
	PayloadSHA256 string
}

type builtinJudgment struct {
	ResultLabels        []ResultLabel
	UserLabels          []UserMessageLabel
	Verdict             TaskVerdict
	Score               float64
	TokenCountEvaluated bool
	TotalTokens         int
}

func runBuiltinEvidenceScoreOracle(criteriaData []byte, ordered, results,
	users []OracleReplayEvent) (builtinJudgment, error) {
	var criteria builtinEvidenceScoreCriteria
	decoder := json.NewDecoder(bytes.NewReader(criteriaData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&criteria); err != nil {
		return builtinJudgment{}, fmt.Errorf("decode builtin evidence-score criteria: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return builtinJudgment{}, err
	}
	if criteria.SchemaVersion != builtinEvidenceScoreCriteriaSchema ||
		len(criteria.AllowedEventOrders) == 0 || len(criteria.Results) == 0 || criteria.Users == nil ||
		criteria.PassThreshold < 0 || criteria.PassThreshold > 1 ||
		!validBuiltinAllowedEventOrders(criteria.AllowedEventOrders) ||
		!validBuiltinResultExpectations(criteria.Results) ||
		!validBuiltinUserExpectations(criteria.Users) {
		return builtinJudgment{}, errors.New("builtin evidence-score criteria are invalid")
	}
	orderedObservations := blindOracleObservations(ordered)
	resultObservations := blindOracleObservations(results)
	userObservations := blindOracleObservations(users)
	if len(resultObservations) == 0 {
		return builtinJudgment{}, errors.New("builtin evidence-score requires at least one result observation")
	}
	orderMatched := false
	for _, allowed := range criteria.AllowedEventOrders {
		if len(allowed) != len(orderedObservations) {
			continue
		}
		matched := true
		for index, expected := range allowed {
			if !validSHA256(expected.PayloadSHA256) || expected.Kind != orderedObservations[index].Kind ||
				expected.PayloadSHA256 != orderedObservations[index].PayloadSHA256 {
				matched = false
				break
			}
		}
		if matched {
			orderMatched = true
			break
		}
	}
	if !orderMatched {
		return builtinJudgment{}, errors.New("builtin evidence-score ordered observations are not allowed")
	}
	resultLabels := map[string]ResultLabel{}
	for _, expected := range criteria.Results {
		resultLabels[expected.PayloadSHA256] = expected.Label
	}
	userLabels := map[string]UserMessageLabel{}
	for _, expected := range criteria.Users {
		userLabels[expected.PayloadSHA256] = expected.Label
	}
	judgment := builtinJudgment{ResultLabels: make([]ResultLabel, len(resultObservations)),
		UserLabels: make([]UserMessageLabel, len(userObservations)), TotalTokens: 0}
	points := 0.0
	for index, observed := range resultObservations {
		label, exists := resultLabels[observed.PayloadSHA256]
		if !exists {
			return builtinJudgment{}, fmt.Errorf("builtin evidence-score result %d is unclassified", index)
		}
		judgment.ResultLabels[index] = label
		switch label {
		case ResultLabelSuccess:
			points++
		case ResultLabelNeutral:
			points += 0.5
		}
	}
	for index, observed := range userObservations {
		label, exists := userLabels[observed.PayloadSHA256]
		if !exists {
			return builtinJudgment{}, fmt.Errorf("builtin evidence-score user message %d is unclassified", index)
		}
		judgment.UserLabels[index] = label
	}
	judgment.Score = points / float64(len(resultObservations))
	judgment.Verdict = TaskVerdictFail
	if judgment.Score >= criteria.PassThreshold {
		judgment.Verdict = TaskVerdictPass
	}
	return judgment, nil
}

func validBuiltinAllowedEventOrders(orders [][]builtinEventExpectation) bool {
	for _, order := range orders {
		if len(order) == 0 {
			return false
		}
		for _, item := range order {
			if !validSHA256(item.PayloadSHA256) ||
				(item.Kind != ledger.KindToolResult && item.Kind != ledger.KindFileChange &&
					item.Kind != ledger.KindUserMessage) {
				return false
			}
		}
	}
	return true
}

func validBuiltinResultExpectations(items []builtinResultExpectation) bool {
	for index, item := range items {
		if !validSHA256(item.PayloadSHA256) ||
			(item.Label != ResultLabelSuccess && item.Label != ResultLabelError && item.Label != ResultLabelNeutral) ||
			(index > 0 && items[index-1].PayloadSHA256 >= item.PayloadSHA256) {
			return false
		}
	}
	return true
}

func validBuiltinUserExpectations(items []builtinUserExpectation) bool {
	for index, item := range items {
		if !validSHA256(item.PayloadSHA256) ||
			(item.Label != UserMessageCorrection && item.Label != UserMessageNotCorrection) ||
			(index > 0 && items[index-1].PayloadSHA256 >= item.PayloadSHA256) {
			return false
		}
	}
	return true
}

func blindOracleObservations(events []OracleReplayEvent) []oracleObservation {
	result := make([]oracleObservation, len(events))
	for index, event := range events {
		result[index] = oracleObservation{Kind: event.Kind, PayloadSHA256: sha256Hex(event.Payload)}
	}
	return result
}

func materializeBuiltinVerdict(request TaskAttemptRequest, judgment builtinJudgment,
	results, users []OracleReplayEvent) TaskAttemptVerdict {
	verdict := TaskAttemptVerdict{SchemaVersion: TaskAttemptVerdictSchema,
		TaskID: request.TaskID, AttemptID: request.AttemptID, Verdict: judgment.Verdict,
		Score: judgment.Score, TokenCountEvaluated: judgment.TokenCountEvaluated,
		TotalTokens: judgment.TotalTokens, TaskSpecSHA256: request.TaskSpecSHA256,
		CriteriaSHA256: request.AcceptanceCriteriaSHA256,
		ConfigSHA256:   request.ExecutionConfigSHA256, ResultEvents: []LabeledResultEvent{},
		UserMessages: []LabeledUserMessage{}, Privacy: "local_only"}
	for index, event := range results {
		verdict.ResultEvents = append(verdict.ResultEvents,
			LabeledResultEvent{EventID: event.EventID, Label: judgment.ResultLabels[index]})
	}
	for index, event := range users {
		verdict.UserMessages = append(verdict.UserMessages,
			LabeledUserMessage{EventID: event.EventID, Label: judgment.UserLabels[index]})
	}
	sort.Slice(verdict.ResultEvents, func(i, j int) bool {
		return verdict.ResultEvents[i].EventID < verdict.ResultEvents[j].EventID
	})
	sort.Slice(verdict.UserMessages, func(i, j int) bool {
		return verdict.UserMessages[i].EventID < verdict.UserMessages[j].EventID
	})
	return verdict
}

func verifyReplayedVerdict(store *ledger.Store, replayed TaskAttemptVerdict,
	receipt TaskAttemptReceipt, records map[string]indexedRecord) error {
	request := taskAttemptRequestFromReceipt(receipt)
	if err := validateTaskAttemptVerdict(replayed, request); err != nil {
		return err
	}
	verdictRecord := records[receipt.Verdict.EventID]
	verdictData, err := eventPayload(store, verdictRecord.Record.Event)
	if err != nil {
		return err
	}
	var recorded TaskAttemptVerdict
	if err := decodeStrictEvaluationJSON(verdictData, &recorded); err != nil || !reflect.DeepEqual(recorded, replayed) {
		return errors.New("independent oracle replay does not match the recorded verdict")
	}
	return nil
}
