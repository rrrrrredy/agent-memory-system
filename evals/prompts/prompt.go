package prompts

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// AgentAssessmentV1 is the fixed instruction used by the controlled
// assessment harness.
//
//go:embed agent-assessment-v1.md
var AgentAssessmentV1 string

func AgentAssessmentV1SHA256() string {
	digest := sha256.Sum256([]byte(AgentAssessmentV1))
	return hex.EncodeToString(digest[:])
}
