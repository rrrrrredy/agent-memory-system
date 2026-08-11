package agentassessment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"sort"
	"strings"
	"unicode/utf8"

	assessmentprompts "github.com/rrrrrredy/agent-memory-system/evals/prompts"
)

const (
	maximumOpenAIRequestBytes  = 4 << 20
	maximumOpenAIResponseBytes = 32 << 20
	openAIMaximumOutputTokens  = 32768
	minimumOutputBytesPerToken = 1
)

type responsesRequest struct {
	Model           string                  `json:"model"`
	Store           bool                    `json:"store"`
	Input           []responsesInputMessage `json:"input"`
	Tools           []any                   `json:"tools"`
	ToolChoice      string                  `json:"tool_choice"`
	Reasoning       responsesReasoning      `json:"reasoning"`
	Text            responsesText           `json:"text"`
	Truncation      string                  `json:"truncation"`
	MaxOutputTokens int                     `json:"max_output_tokens"`
}

type responsesInputMessage struct {
	Role    string                  `json:"role"`
	Content []responsesInputContent `json:"content"`
}

type responsesInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesText struct {
	Format responsesTextFormat `json:"format"`
}

type responsesTextFormat struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

type responsesEnvelope struct {
	ID                string                `json:"id"`
	Object            string                `json:"object"`
	Model             string                `json:"model"`
	Status            string                `json:"status"`
	Error             json.RawMessage       `json:"error"`
	IncompleteDetails json.RawMessage       `json:"incomplete_details"`
	Output            []responsesOutputItem `json:"output"`
}

type responsesOutputItem struct {
	Type    string                   `json:"type"`
	Role    string                   `json:"role"`
	Status  string                   `json:"status"`
	Content []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func buildOpenAIRequest(model string, payload BlindPayload, payloadBytes []byte) ([]byte, string, error) {
	if !safeMetadata(model) {
		return nil, "", errors.New("OpenAI model is invalid")
	}
	if !supportsReasoningNone(model) {
		return nil, "", errors.New("OpenAI model is not approved for the required reasoning.effort none policy")
	}
	canonical, err := marshalIndented(payload)
	if err != nil || !bytes.Equal(canonical, payloadBytes) {
		return nil, "", errors.New("blind payload is not in canonical encoding")
	}
	if err := validateRemoteOutputFeasibility(payload); err != nil {
		return nil, "", err
	}
	outputSchema := providerSubmissionSchema(payload.PayloadID)
	outputSchemaBytes, err := json.Marshal(outputSchema)
	if err != nil {
		return nil, "", err
	}
	request := responsesRequest{
		Model: model, Store: false,
		Input: []responsesInputMessage{
			{Role: "developer", Content: []responsesInputContent{{
				Type: "input_text", Text: assessmentprompts.AgentAssessmentV1,
			}}},
			{Role: "user", Content: []responsesInputContent{{
				Type: "input_text", Text: string(payloadBytes),
			}}},
		},
		Tools: []any{}, ToolChoice: "none", Reasoning: responsesReasoning{Effort: "none"},
		Text: responsesText{Format: responsesTextFormat{
			Type: "json_schema", Name: "legacy_agent_assessment_submission",
			Strict: true, Schema: outputSchema,
		}},
		Truncation: "disabled", MaxOutputTokens: openAIMaximumOutputTokens,
	}
	data, err := marshalIndented(request)
	if err != nil {
		return nil, "", fmt.Errorf("encode OpenAI request: %w", err)
	}
	if len(data) > maximumOpenAIRequestBytes {
		return nil, "", errors.New("OpenAI assessment request exceeds the remote safety limit")
	}
	return data, hashBytes(outputSchemaBytes), nil
}

func supportsReasoningNone(model string) bool {
	if strings.Contains(model, "-pro") {
		return false
	}
	for _, family := range []string{"gpt-5.1", "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-5.6"} {
		if model == family || strings.HasPrefix(model, family+"-") {
			return true
		}
	}
	return false
}

func validateRemoteOutputFeasibility(payload BlindPayload) error {
	minimum := Submission{SchemaVersion: SubmissionSchema, PayloadID: payload.PayloadID}
	for _, item := range payload.CandidateItems {
		minimum.CandidateAssessments = append(minimum.CandidateAssessments, CandidateAssessment{
			ItemID: item.ItemID, Judgment: "insufficient_evidence",
			ReasonCodes: []string{"evidence_gap"}, EvidenceBlockIDs: []string{},
		})
	}
	for _, item := range payload.CompactionItems {
		for _, unit := range item.Units {
			minimum.CompactionAssessments = append(minimum.CompactionAssessments, CompactionAssessment{
				ItemID: item.ItemID, UnitID: unit.UnitID, Judgment: "insufficient_evidence",
				ReasonCodes: []string{"evidence_gap"}, EvidenceBlockIDs: []string{},
			})
		}
	}
	data, err := json.Marshal(minimum)
	if err != nil {
		return fmt.Errorf("estimate OpenAI assessment output: %w", err)
	}
	if len(data) > openAIMaximumOutputTokens*minimumOutputBytesPerToken {
		return fmt.Errorf("minimum complete assessment output is %d bytes and exceeds the conservative remote output budget; prepare a smaller review queue", len(data))
	}
	return nil
}

func providerSubmissionSchema(payloadID string) map[string]any {
	stringArray := func(values []string) map[string]any {
		items := make([]any, len(values))
		for index := range values {
			items[index] = values[index]
		}
		return map[string]any{"type": "array", "items": map[string]any{
			"type": "string", "enum": items,
		}}
	}
	blockIDs := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	candidate := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"item_id", "judgment", "reason_codes", "evidence_block_ids"},
		"properties": map[string]any{
			"item_id":            map[string]any{"type": "string"},
			"judgment":           map[string]any{"type": "string", "enum": sortedSetKeys(candidateJudgments)},
			"reason_codes":       stringArray(sortedSetKeys(candidateReasonCodes)),
			"evidence_block_ids": blockIDs,
		},
	}
	compaction := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"item_id", "unit_id", "judgment", "reason_codes", "evidence_block_ids"},
		"properties": map[string]any{
			"item_id":            map[string]any{"type": "string"},
			"unit_id":            map[string]any{"type": "string"},
			"judgment":           map[string]any{"type": "string", "enum": sortedSetKeys(compactionJudgments)},
			"reason_codes":       stringArray(sortedSetKeys(compactionReasonCodes)),
			"evidence_block_ids": blockIDs,
		},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"schema_version", "payload_id", "candidate_assessments", "compaction_assessments"},
		"properties": map[string]any{
			"schema_version":         map[string]any{"type": "string", "enum": []string{SubmissionSchema}},
			"payload_id":             map[string]any{"type": "string", "enum": []string{payloadID}},
			"candidate_assessments":  map[string]any{"type": "array", "items": candidate},
			"compaction_assessments": map[string]any{"type": "array", "items": compaction},
		},
	}
}

func extractOpenAISubmission(data []byte) (responsesEnvelope, []byte, string, error) {
	var envelope responsesEnvelope
	if err := validateJSONLexicalForm(data); err != nil {
		return envelope, nil, "invalid_response", errors.New("OpenAI response JSON is unsafe")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, nil, "invalid_response", errors.New("decode OpenAI response")
	}
	if err := requireEOF(decoder); err != nil {
		return envelope, nil, "invalid_response", errors.New("OpenAI response contains trailing JSON")
	}
	if envelope.Object != "response" {
		return envelope, nil, "invalid_response", errors.New("OpenAI response object is invalid")
	}
	if envelope.Status == "failed" || (len(envelope.Error) != 0 && !explicitJSONNull(envelope.Error)) {
		return envelope, nil, "provider_response_failed", errors.New("OpenAI response failed")
	}
	if envelope.Status != "completed" ||
		(len(envelope.IncompleteDetails) != 0 && !explicitJSONNull(envelope.IncompleteDetails)) {
		return envelope, nil, "incomplete", errors.New("OpenAI response is not complete")
	}
	if !explicitJSONNull(envelope.Error) || !explicitJSONNull(envelope.IncompleteDetails) {
		return envelope, nil, "invalid_response", errors.New("OpenAI response completion state is missing")
	}
	messageCount := 0
	outputTexts := []string{}
	for _, item := range envelope.Output {
		switch item.Type {
		case "reasoning":
			continue
		case "message":
			messageCount++
			if item.Role != "assistant" || item.Status != "completed" {
				return envelope, nil, "invalid_response", errors.New("OpenAI response message role is invalid")
			}
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					outputTexts = append(outputTexts, content.Text)
				case "refusal":
					return envelope, nil, "refusal", errors.New("OpenAI assessment was refused")
				default:
					return envelope, nil, "invalid_response", errors.New("OpenAI response content type is invalid")
				}
			}
		default:
			return envelope, nil, "tool_output", errors.New("OpenAI response contains a non-message output item")
		}
	}
	if messageCount != 1 || len(outputTexts) != 1 || outputTexts[0] == "" ||
		!safeMetadata(envelope.ID) || !safeMetadata(envelope.Model) {
		return envelope, nil, "invalid_response", errors.New("OpenAI response does not contain one structured message")
	}
	return envelope, []byte(outputTexts[0]), "validated_submission", nil
}

func validateJSONLexicalForm(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is invalid")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("JSON object key %q is duplicated", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("JSON object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("JSON array is incomplete")
			}
		default:
			return errors.New("JSON delimiter is invalid")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON has more than one top-level value")
		}
		return err
	}
	return nil
}

func explicitJSONNull(value json.RawMessage) bool {
	return len(value) != 0 && bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func sortedSetKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func isJSONMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}
