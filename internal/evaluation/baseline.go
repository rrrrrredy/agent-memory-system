package evaluation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func BuildLegacyCaptureInput(store *ledger.Store, corpusID string,
	options LegacyCaptureInputOptions) (EvaluationInput, error) {
	if store == nil {
		return EvaluationInput{}, errors.New("store is required")
	}
	if !validCorpusID(corpusID) {
		return EvaluationInput{}, errors.New("a valid corpus_id is required")
	}
	if !safeIdentifier(options.SuiteID) || !safeIdentifier(options.RunID) ||
		strings.TrimSpace(options.SystemVersion) == "" {
		return EvaluationInput{}, errors.New("suite_id, run_id, and system_version are required")
	}
	if options.MinimumCoverage < 0 || options.MinimumCoverage > 1 {
		return EvaluationInput{}, errors.New("minimum coverage must be between 0 and 1")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	verification := VerifyCorpus(store, corpusID)
	if len(verification.Issues) != 0 {
		return EvaluationInput{}, fmt.Errorf("verify legacy corpus: %s", strings.Join(verification.Issues, "; "))
	}
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		return EvaluationInput{}, err
	}
	var index *CorpusArtifact
	for position := range manifest.Artifacts {
		if manifest.Artifacts[position].Role == RoleLegacyIndex {
			if index != nil {
				return EvaluationInput{}, errors.New("legacy corpus contains multiple index artifacts")
			}
			index = &manifest.Artifacts[position]
		}
	}
	if index == nil {
		return EvaluationInput{}, errors.New("legacy corpus has no index artifact")
	}
	minimum := options.MinimumCoverage
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion,
		SuiteID:       options.SuiteID,
		RunID:         options.RunID,
		CreatedAt:     options.Now().UTC(),
		SystemVersion: options.SystemVersion,
		CorpusID:      corpusID,
		Thresholds: EvaluationThresholds{
			MinimumCaptureCoverage: &minimum,
		},
		Privacy: "local_only",
		Cases: []EvaluationCase{{
			CaseID: "legacy-rollout-capture", Category: CategoryCaptureCoverage,
			Agent: ledger.AgentCodex,
			Evidence: []EvidenceReference{{
				Kind: "corpus_artifact", ID: index.ArtifactID, SHA256: index.Blob.SHA256,
			}},
			Capture: &CaptureMeasurement{
				Unit: CaptureUnitLegacyRollouts, Expected: manifest.Counts.RolloutReferences,
				Complete: manifest.Counts.CapturedRollouts, Partial: manifest.Counts.PartialRollouts,
				Missing: manifest.Counts.MissingRollouts,
			},
		}},
	}
	if err := validateInput(input); err != nil {
		return EvaluationInput{}, err
	}
	return input, nil
}
