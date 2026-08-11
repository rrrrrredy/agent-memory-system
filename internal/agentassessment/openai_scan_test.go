package agentassessment

import (
	"reflect"
	"strings"
	"testing"
)

func TestRemotePreflightScansOnlyAllowlistedTextAndIgnoresValidOpaqueIDs(t *testing.T) {
	_, _, payload, _, _ := openAITestFixture(t, "Clean evidence without local identifiers.")
	observation, err := scanBlindPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Findings != 0 || observation.FieldsScanned != 4 ||
		observation.UniqueTexts != 3 || observation.ContentSetSHA256 == "" {
		t.Fatalf("unexpected preflight observation: %+v", observation)
	}
	payload.EvidenceBlocks[0].Text = "OPENAI_API_KEY=sk-" + strings.Repeat("Ab3_", 8)
	observation, err = scanBlindPayload(payload)
	if err != nil || observation.Findings == 0 {
		t.Fatalf("sensitive text was not blocked: observation=%+v err=%v", observation, err)
	}
}

func TestRemotePreflightTypeClassificationFailsClosedForNewStringField(t *testing.T) {
	type extendedPayload struct {
		SchemaVersion string `json:"schema_version"`
		NewText       string `json:"new_text"`
	}
	if err := classifyBlindPayloadStrings(reflect.TypeOf(extendedPayload{}), ""); err == nil || !strings.Contains(err.Error(), "new_text") {
		t.Fatal("new string field bypassed the remote preflight allowlist")
	}
}

func TestRemotePreflightRejectsInvalidUTF8(t *testing.T) {
	_, _, payload, _, _ := openAITestFixture(t, "Clean local evidence.")
	payload.EvidenceBlocks[0].Text = string([]byte{0xff, 0xfe})
	if _, err := scanBlindPayload(payload); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatal("invalid UTF-8 reached remote request construction")
	}
}
