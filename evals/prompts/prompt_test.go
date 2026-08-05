package prompts

import "testing"

func TestAgentAssessmentPromptIsEmbeddedAndContentAddressed(t *testing.T) {
	if AgentAssessmentV1 == "" || len(AgentAssessmentV1SHA256()) != 64 {
		t.Fatal("Agent assessment prompt is unavailable")
	}
}
