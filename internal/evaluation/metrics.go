package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func DecodeInput(reader io.Reader) (EvaluationInput, error) {
	if reader == nil {
		return EvaluationInput{}, errors.New("evaluation input reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var input EvaluationInput
	if err := decoder.Decode(&input); err != nil {
		return EvaluationInput{}, fmt.Errorf("decode evaluation input: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return EvaluationInput{}, err
	}
	if err := validateInput(input); err != nil {
		return EvaluationInput{}, err
	}
	return input, nil
}

func Calculate(input EvaluationInput) (EvaluationReport, error) {
	if err := validateInput(input); err != nil {
		return EvaluationReport{}, err
	}
	inputData, err := json.Marshal(input)
	if err != nil {
		return EvaluationReport{}, fmt.Errorf("encode evaluation input: %w", err)
	}
	inputDigest := sha256.Sum256(inputData)
	report := EvaluationReport{
		SchemaVersion: EvaluationReportSchema, EvaluationVersion: EvaluationVersion,
		SuiteID: input.SuiteID, RunID: input.RunID,
		InputSHA256: hex.EncodeToString(inputDigest[:]), SystemVersion: input.SystemVersion,
		CorpusID: input.CorpusID, CasesChecked: len(input.Cases), Issues: []string{},
		Capture: CaptureMetrics{Unit: CaptureUnitNone}, Privacy: "local_only",
	}

	tokens := []int{}
	var scoreDelta, successDelta, errorDelta, correctionDelta, tokenDelta float64
	for _, evaluationCase := range input.Cases {
		switch evaluationCase.Category {
		case CategoryCaptureCoverage:
			measurement := evaluationCase.Capture
			if report.Capture.Unit == CaptureUnitNone {
				report.Capture.Unit = measurement.Unit
			}
			report.Capture.Expected += measurement.Expected
			report.Capture.Complete += measurement.Complete
			report.Capture.Partial += measurement.Partial
			report.Capture.Missing += measurement.Missing
			report.Capture.AccountedMissing += measurement.AccountedMissing
		case CategoryFalseMemory:
			measurement := evaluationCase.Memory
			report.FalseMemory.Total++
			switch measurement.Label {
			case MemorySupported:
				report.FalseMemory.Supported++
			case MemoryIncorrect, MemoryUnsupported, MemoryStale:
				report.FalseMemory.False++
			case MemoryUnknown:
				report.FalseMemory.Unknown++
			}
		case CategoryRepeatedCorrection:
			measurement := evaluationCase.Correction
			report.Corrections.EligibleFollowupOpportunities += measurement.EligibleFollowupOpportunities
			report.Corrections.RepeatedCorrections += measurement.RepeatedCorrections
			report.Corrections.RepeatedCorrectionsAfterMemory += measurement.RepeatedCorrectionsAfterMemory
		case CategoryCompactionDrift:
			measurement := evaluationCase.Compaction
			if measurement.Expected == DriftInsufficientEvidence ||
				measurement.Observed == DriftInsufficientEvidence {
				report.CompactionDrift.Excluded++
				break
			}
			switch {
			case measurement.Expected == DriftDetected && measurement.Observed == DriftDetected:
				report.CompactionDrift.TruePositive++
			case measurement.Expected == DriftPreserved && measurement.Observed == DriftDetected:
				report.CompactionDrift.FalsePositive++
			case measurement.Expected == DriftPreserved && measurement.Observed == DriftPreserved:
				report.CompactionDrift.TrueNegative++
			case measurement.Expected == DriftDetected && measurement.Observed == DriftPreserved:
				report.CompactionDrift.FalseNegative++
			}
		case CategoryRetrievalCost:
			measurement := evaluationCase.Retrieval
			report.Retrieval.Deliveries++
			report.Retrieval.SelectedItems += measurement.SelectedItems
			report.Retrieval.TotalTokens += measurement.EstimatedTokens
			report.Retrieval.TotalUTF8Bytes += measurement.UTF8Bytes
			tokens = append(tokens, measurement.EstimatedTokens)
			if measurement.Adopted {
				report.Retrieval.Adopted++
			}
			switch measurement.Outcome {
			case OutcomeHelpful:
				report.Retrieval.Helpful++
			case OutcomeNeutral:
				report.Retrieval.Neutral++
			case OutcomeHarmful:
				report.Retrieval.Harmful++
			case OutcomeUnknown:
				report.Retrieval.Unknown++
			}
		case CategoryPairedOutcome:
			measurement := evaluationCase.PairedOutcome
			report.Outcomes.Pairs++
			if measurement.Baseline.Success {
				report.Outcomes.BaselineSuccesses++
			}
			if measurement.Treatment.Success {
				report.Outcomes.TreatmentSuccesses++
			}
			delta := measurement.Treatment.Score - measurement.Baseline.Score
			scoreDelta += delta
			switch {
			case delta > 1e-12:
				report.Outcomes.Wins++
			case delta < -1e-12:
				report.Outcomes.Losses++
			default:
				report.Outcomes.Ties++
			}
			successDelta += boolFloat(measurement.Treatment.Success) - boolFloat(measurement.Baseline.Success)
			errorDelta += float64(measurement.Treatment.Errors - measurement.Baseline.Errors)
			correctionDelta += float64(measurement.Treatment.UserCorrections - measurement.Baseline.UserCorrections)
			tokenDelta += float64(measurement.Treatment.TotalTokens - measurement.Baseline.TotalTokens)
		}
	}

	report.Capture.Coverage = ratio(report.Capture.Complete, report.Capture.Expected)
	report.Capture.ObservedCoverage = ratio(
		report.Capture.Complete+report.Capture.Partial, report.Capture.Expected,
	)
	if report.Capture.AccountedMissing > 0 {
		accounted := ratio(
			report.Capture.Complete+report.Capture.Partial+report.Capture.AccountedMissing,
			report.Capture.Expected,
		)
		report.Capture.AccountedCoverage = &accounted
	}
	knownMemories := report.FalseMemory.Total - report.FalseMemory.Unknown
	report.FalseMemory.FalseRate = ratio(report.FalseMemory.False, knownMemories)
	report.FalseMemory.UnknownRate = ratio(report.FalseMemory.Unknown, report.FalseMemory.Total)
	report.Corrections.RepeatedCorrectionRate = ratio(
		report.Corrections.RepeatedCorrectionsAfterMemory,
		report.Corrections.EligibleFollowupOpportunities,
	)
	report.CompactionDrift.Precision = ratio(
		report.CompactionDrift.TruePositive,
		report.CompactionDrift.TruePositive+report.CompactionDrift.FalsePositive,
	)
	report.CompactionDrift.Recall = ratio(
		report.CompactionDrift.TruePositive,
		report.CompactionDrift.TruePositive+report.CompactionDrift.FalseNegative,
	)
	if report.Retrieval.Deliveries > 0 {
		mean := float64(report.Retrieval.TotalTokens) / float64(report.Retrieval.Deliveries)
		report.Retrieval.MeanTokens = roundedPointer(mean)
		sort.Ints(tokens)
		index := int(math.Ceil(0.95*float64(len(tokens)))) - 1
		if index < 0 {
			index = 0
		}
		p95 := float64(tokens[index])
		report.Retrieval.P95Tokens = roundedPointer(p95)
	}
	if report.Retrieval.TotalTokens > 0 {
		value := float64(report.Retrieval.Helpful) * 1000 / float64(report.Retrieval.TotalTokens)
		report.Retrieval.HelpfulPerKToken = roundedPointer(value)
	}
	if report.Outcomes.Pairs > 0 {
		pairs := float64(report.Outcomes.Pairs)
		report.Outcomes.MeanScoreDelta = roundedPointer(scoreDelta / pairs)
		report.Outcomes.SuccessRateDelta = roundedPointer(successDelta / pairs)
		report.Outcomes.MeanErrorDelta = roundedPointer(errorDelta / pairs)
		report.Outcomes.MeanCorrectionDelta = roundedPointer(correctionDelta / pairs)
		report.Outcomes.MeanTotalTokenDelta = roundedPointer(tokenDelta / pairs)
	}

	report.Gates = evaluateGates(input.Thresholds, report)
	// Metric calculation alone never proves a release gate. Run resolves every
	// referenced artifact before it may set ReleaseReady.
	report.ReleaseReady = false
	if err := setReportHash(&report); err != nil {
		return EvaluationReport{}, err
	}
	return report, nil
}

func validateInput(input EvaluationInput) error {
	if input.SchemaVersion != EvaluationInputSchemaVersion {
		return fmt.Errorf("unsupported evaluation input schema %q", input.SchemaVersion)
	}
	if !safeIdentifier(input.SuiteID) || !safeIdentifier(input.RunID) || strings.TrimSpace(input.SystemVersion) == "" {
		return errors.New("suite_id, run_id, and system_version are required")
	}
	if input.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	if input.Privacy != "local_only" {
		return errors.New("evaluation input privacy must be local_only")
	}
	if input.CorpusID != "" && !strings.HasPrefix(input.CorpusID, "corpus-") {
		return errors.New("corpus_id must use the corpus- prefix")
	}
	if len(input.Cases) == 0 {
		return errors.New("at least one evaluation case is required")
	}
	if err := validateThresholds(input.Thresholds); err != nil {
		return err
	}
	seenCases := map[string]struct{}{}
	captureUnit := CaptureUnitNone
	for index, evaluationCase := range input.Cases {
		if !safeIdentifier(evaluationCase.CaseID) {
			return fmt.Errorf("case %d has an invalid case_id", index)
		}
		if _, exists := seenCases[evaluationCase.CaseID]; exists {
			return fmt.Errorf("duplicate case_id %q", evaluationCase.CaseID)
		}
		seenCases[evaluationCase.CaseID] = struct{}{}
		if !validAgent(evaluationCase.Agent) {
			return fmt.Errorf("case %q has an invalid agent", evaluationCase.CaseID)
		}
		if len(evaluationCase.Evidence) == 0 {
			return fmt.Errorf("case %q has no evidence references", evaluationCase.CaseID)
		}
		seenReferences := map[string]struct{}{}
		hasObservedArtifact := false
		for _, reference := range evaluationCase.Evidence {
			if reference.Kind != "ledger_event" && reference.Kind != "corpus_artifact" &&
				reference.Kind != "portable_revision" {
				return fmt.Errorf("case %q has unsupported evidence kind %q", evaluationCase.CaseID, reference.Kind)
			}
			hasObservedArtifact = true
			if strings.TrimSpace(reference.ID) == "" || !validSHA256(reference.SHA256) {
				return fmt.Errorf("case %q has an invalid evidence reference", evaluationCase.CaseID)
			}
			key := reference.Kind + "\x00" + reference.ID
			if _, exists := seenReferences[key]; exists {
				return fmt.Errorf("case %q repeats evidence reference %q", evaluationCase.CaseID, reference.ID)
			}
			seenReferences[key] = struct{}{}
		}
		if !hasObservedArtifact {
			return fmt.Errorf("case %q has no observed artifact reference", evaluationCase.CaseID)
		}
		if err := validateCase(evaluationCase); err != nil {
			return fmt.Errorf("case %q: %w", evaluationCase.CaseID, err)
		}
		if evaluationCase.Category == CategoryCaptureCoverage {
			if captureUnit == CaptureUnitNone {
				captureUnit = evaluationCase.Capture.Unit
			} else if captureUnit != evaluationCase.Capture.Unit {
				return errors.New("capture cases in one run must use the same unit")
			}
		}
	}
	return nil
}

func validateCase(evaluationCase EvaluationCase) error {
	if err := validateCaseMeasurement(evaluationCase); err != nil {
		return err
	}
	if evaluationCase.Category == CategoryFalseMemory &&
		!hasReferenceKind(evaluationCase.Evidence, "portable_revision") {
		return errors.New("memory labels require a portable revision")
	}
	return nil
}

func validateCaseMeasurement(evaluationCase EvaluationCase) error {
	measurements := 0
	for _, present := range []bool{
		evaluationCase.Capture != nil, evaluationCase.Memory != nil,
		evaluationCase.Correction != nil, evaluationCase.Compaction != nil,
		evaluationCase.Retrieval != nil, evaluationCase.PairedOutcome != nil,
	} {
		if present {
			measurements++
		}
	}
	if measurements != 1 {
		return errors.New("exactly one measurement is required")
	}
	switch evaluationCase.Category {
	case CategoryCaptureCoverage:
		measurement := evaluationCase.Capture
		if measurement == nil ||
			(measurement.Unit != CaptureUnitEvidenceEvents && measurement.Unit != CaptureUnitLegacyRollouts) ||
			measurement.Expected < 0 || measurement.Complete < 0 ||
			measurement.Partial < 0 || measurement.Missing < 0 || measurement.AccountedMissing < 0 ||
			measurement.AccountedMissing > measurement.Missing ||
			measurement.Complete+measurement.Partial+measurement.Missing != measurement.Expected {
			return errors.New("capture counts must be non-negative and sum to expected")
		}
	case CategoryFalseMemory:
		measurement := evaluationCase.Memory
		if measurement == nil || strings.TrimSpace(measurement.MemoryID) == "" ||
			(!measurement.Active && !measurement.Retrieved) {
			return errors.New("memory measurement must identify an active or retrieved memory")
		}
		switch measurement.Label {
		case MemorySupported, MemoryIncorrect, MemoryUnsupported, MemoryStale, MemoryUnknown:
		default:
			return errors.New("memory label is invalid")
		}
	case CategoryRepeatedCorrection:
		measurement := evaluationCase.Correction
		if measurement == nil || !validSHA256(measurement.SemanticKeySHA256) ||
			measurement.EligibleFollowupOpportunities < 0 || measurement.RepeatedCorrections < 0 ||
			measurement.RepeatedCorrectionsAfterMemory < 0 ||
			measurement.RepeatedCorrections > measurement.EligibleFollowupOpportunities ||
			measurement.RepeatedCorrectionsAfterMemory > measurement.RepeatedCorrections {
			return errors.New("correction measurement is inconsistent")
		}
	case CategoryCompactionDrift:
		measurement := evaluationCase.Compaction
		if measurement == nil || strings.TrimSpace(measurement.CheckpointID) == "" ||
			!validDriftLabel(measurement.Expected) || !validDriftLabel(measurement.Observed) {
			return errors.New("compaction measurement is invalid")
		}
	case CategoryRetrievalCost:
		measurement := evaluationCase.Retrieval
		if measurement == nil || strings.TrimSpace(measurement.RetrievalID) == "" ||
			measurement.EstimatedTokens < 0 || measurement.UTF8Bytes < 0 || measurement.SelectedItems < 0 {
			return errors.New("retrieval measurement is invalid")
		}
		if measurement.SelectedItems == 0 && measurement.Adopted {
			return errors.New("a zero-result retrieval cannot be adopted")
		}
		if !measurement.Adopted && measurement.Outcome != OutcomeUnknown {
			return errors.New("a non-adopted retrieval cannot claim an outcome")
		}
		switch measurement.Outcome {
		case OutcomeHelpful, OutcomeNeutral, OutcomeHarmful, OutcomeUnknown:
		default:
			return errors.New("retrieval outcome is invalid")
		}
	case CategoryPairedOutcome:
		measurement := evaluationCase.PairedOutcome
		if measurement == nil || !safeIdentifier(measurement.PairID) ||
			!validTrial(measurement.Baseline) || !validTrial(measurement.Treatment) {
			return errors.New("paired outcome measurement is invalid")
		}
	default:
		return fmt.Errorf("unsupported category %q", evaluationCase.Category)
	}
	return nil
}

func evaluateGates(thresholds EvaluationThresholds, report EvaluationReport) []GateResult {
	gates := []GateResult{}
	addMinimum := func(name string, threshold *float64, actual *float64) {
		if threshold == nil {
			return
		}
		gates = append(gates, gate(name, ">=", *threshold, actual))
	}
	addMaximum := func(name string, threshold *float64, actual *float64) {
		if threshold == nil {
			return
		}
		gates = append(gates, gate(name, "<=", *threshold, actual))
	}
	addMinimum("capture_coverage", thresholds.MinimumCaptureCoverage, report.Capture.Coverage.Value)
	addMaximum("false_memory_rate", thresholds.MaximumFalseMemoryRate, report.FalseMemory.FalseRate.Value)
	addMaximum("unknown_memory_rate", thresholds.MaximumUnknownMemoryRate, report.FalseMemory.UnknownRate.Value)
	addMaximum("repeated_correction_rate", thresholds.MaximumRepeatedCorrectionRate,
		report.Corrections.RepeatedCorrectionRate.Value)
	addMinimum("compaction_drift_precision", thresholds.MinimumDriftPrecision, report.CompactionDrift.Precision.Value)
	addMinimum("compaction_drift_recall", thresholds.MinimumDriftRecall, report.CompactionDrift.Recall.Value)
	addMaximum("mean_retrieval_tokens", thresholds.MaximumMeanRetrievalTokens, report.Retrieval.MeanTokens)
	addMinimum("mean_outcome_score_delta", thresholds.MinimumMeanOutcomeScoreDelta, report.Outcomes.MeanScoreDelta)
	if thresholds.MaximumHarmfulOutcomes != nil {
		var actual *float64
		if report.Retrieval.Deliveries > 0 {
			value := float64(report.Retrieval.Harmful)
			actual = &value
		}
		addMaximum("harmful_outcomes", thresholds.MaximumHarmfulOutcomes, actual)
	}
	sort.Slice(gates, func(left, right int) bool { return gates[left].Name < gates[right].Name })
	return gates
}

func gate(name, comparison string, threshold float64, actual *float64) GateResult {
	result := GateResult{Name: name, Comparison: comparison, Threshold: threshold, Actual: actual}
	if actual == nil {
		result.Status = "not_evaluable"
		result.Reason = "no eligible observations"
		return result
	}
	passed := comparison == ">=" && *actual >= threshold || comparison == "<=" && *actual <= threshold
	if passed {
		result.Status = "pass"
	} else {
		result.Status = "fail"
	}
	return result
}

func gatesPassed(gates []GateResult) bool {
	if len(gates) == 0 {
		return false
	}
	for _, gate := range gates {
		if gate.Status != "pass" {
			return false
		}
	}
	return true
}

func ratio(numerator, denominator int) RatioMetric {
	metric := RatioMetric{Numerator: numerator, Denominator: denominator, Status: "not_evaluable"}
	if denominator <= 0 {
		return metric
	}
	value := float64(numerator) / float64(denominator)
	metric.Value = roundedPointer(value)
	metric.Status = "measured"
	return metric
}

func setReportHash(report *EvaluationReport) error {
	report.ReportSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode evaluation report: %w", err)
	}
	digest := sha256.Sum256(data)
	report.ReportSHA256 = hex.EncodeToString(digest[:])
	return nil
}

func validateReportHash(report EvaluationReport) bool {
	wanted := report.ReportSHA256
	if !validSHA256(wanted) {
		return false
	}
	report.ReportSHA256 = ""
	data, err := json.Marshal(report)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(data)
	return bytes.Equal([]byte(wanted), []byte(hex.EncodeToString(digest[:])))
}

func validateThresholds(thresholds EvaluationThresholds) error {
	for name, value := range map[string]*float64{
		"minimum_capture_coverage":         thresholds.MinimumCaptureCoverage,
		"maximum_false_memory_rate":        thresholds.MaximumFalseMemoryRate,
		"maximum_unknown_memory_rate":      thresholds.MaximumUnknownMemoryRate,
		"maximum_repeated_correction_rate": thresholds.MaximumRepeatedCorrectionRate,
		"minimum_drift_precision":          thresholds.MinimumDriftPrecision,
		"minimum_drift_recall":             thresholds.MinimumDriftRecall,
	} {
		if value != nil && (*value < 0 || *value > 1 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Errorf("threshold %s must be between 0 and 1", name)
		}
	}
	for name, value := range map[string]*float64{
		"maximum_mean_retrieval_tokens": thresholds.MaximumMeanRetrievalTokens,
		"maximum_harmful_outcomes":      thresholds.MaximumHarmfulOutcomes,
	} {
		if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return fmt.Errorf("threshold %s must be non-negative", name)
		}
	}
	if value := thresholds.MinimumMeanOutcomeScoreDelta; value != nil &&
		(*value < -1 || *value > 1 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
		return errors.New("minimum_mean_outcome_score_delta must be between -1 and 1")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing evaluation input: %w", err)
	}
	return errors.New("evaluation input contains multiple JSON values")
}

func validAgent(agent ledger.Agent) bool {
	switch agent {
	case ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode, ledger.AgentUnknown:
		return true
	default:
		return false
	}
}

func validDriftLabel(label DriftLabel) bool {
	return label == DriftPreserved || label == DriftDetected || label == DriftInsufficientEvidence
}

func validTrial(trial TrialMeasurement) bool {
	return trial.Score >= 0 && trial.Score <= 1 && !math.IsNaN(trial.Score) &&
		!math.IsInf(trial.Score, 0) && trial.Errors >= 0 && trial.UserCorrections >= 0 && trial.TotalTokens >= 0
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func safeIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 160 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func roundedPointer(value float64) *float64 {
	rounded := math.Round(value*1_000_000) / 1_000_000
	return &rounded
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func hasReferenceKind(references []EvidenceReference, kind string) bool {
	for _, reference := range references {
		if reference.Kind == kind {
			return true
		}
	}
	return false
}
