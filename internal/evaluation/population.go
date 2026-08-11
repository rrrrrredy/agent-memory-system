package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/capturesupervisor"
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

type PrepareContinuousOptions struct {
	SuiteID        string
	RunID          string
	SystemVersion  string
	CorpusID       string
	PortableRoot   string
	OracleRegistry string
	Now            func() time.Time
}

func PrepareContinuousInput(store *ledger.Store, options PrepareContinuousOptions) (EvaluationInput, error) {
	if store == nil {
		return EvaluationInput{}, errors.New("store is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	_, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return EvaluationInput{}, err
	}
	if len(ordered) == 0 {
		return EvaluationInput{}, errors.New("continuous-learning population requires evidence records")
	}
	if _, err := episodes.Build(store, episodes.BuildOptions{}); err != nil {
		return EvaluationInput{}, fmt.Errorf("build episode detector input: %w", err)
	}
	cases, population, err := buildContinuousPopulation(store, len(ordered), options.PortableRoot,
		options.OracleRegistry, options.CorpusID, nil)
	if err != nil {
		return EvaluationInput{}, err
	}
	input := EvaluationInput{
		SchemaVersion: EvaluationInputSchemaVersion, SuiteID: options.SuiteID, RunID: options.RunID,
		CreatedAt: options.Now().UTC(), SystemVersion: options.SystemVersion,
		QualityProfile: QualityProfileContinuousLearning, PolicyID: ContinuousLearningPolicyV1,
		CorpusID:   options.CorpusID,
		Population: &population, Cases: cases, Thresholds: FixedContinuousLearningThresholds(),
		Privacy: "local_only",
	}
	if !safeIdentifier(input.SuiteID) || !safeIdentifier(input.RunID) || strings.TrimSpace(input.SystemVersion) == "" {
		return EvaluationInput{}, errors.New("suite id, run id, and system version are required")
	}
	return input, nil
}

func FixedContinuousLearningThresholds() EvaluationThresholds {
	value := func(number float64) *float64 { return &number }
	return EvaluationThresholds{
		MinimumCaptureCoverage: value(1), MaximumFalseMemoryRate: value(0),
		MaximumUnknownMemoryRate: value(0), MaximumRepeatedCorrectionRate: value(0.10),
		MinimumDriftPrecision: value(0.90), MinimumDriftRecall: value(0.90),
		MaximumMeanRetrievalTokens: value(800), MinimumMeanOutcomeScoreDelta: value(0.01),
		MaximumMeanCorrectionDelta: value(-0.01), MaximumHarmfulOutcomes: value(0),
		MinimumCorrectionOpportunities: value(20), MinimumPairedOutcomePairs: value(10),
	}
}

func verifyContinuousPopulation(store *ledger.Store, input EvaluationInput,
	portableRoot, oracleRegistry string, requireCurrentLedger bool) []string {
	if input.QualityProfile != QualityProfileContinuousLearning {
		return nil
	}
	if input.Population == nil {
		return []string{"continuous-learning population binding is missing"}
	}
	issues := []string{}
	if requireCurrentLedger {
		_, ordered, err := loadIndexedRecords(store)
		if err != nil {
			return []string{"continuous-learning ledger freshness check failed: " + err.Error()}
		}
		prefix := input.Population.LedgerRecordCount
		if prefix < 0 || prefix > len(ordered) {
			issues = append(issues, "continuous-learning population is stale relative to the current ledger")
		} else {
			for _, record := range ordered[prefix:] {
				if record.Event.Kind != ledger.KindEvaluationRun {
					issues = append(issues, "continuous-learning population is stale relative to the current ledger")
					break
				}
			}
		}
	}
	wantedCases, wantedPopulation, err := buildContinuousPopulation(store,
		input.Population.LedgerRecordCount, portableRoot, oracleRegistry, input.CorpusID, input.Population)
	if err != nil {
		return []string{"continuous-learning population replay failed: " + err.Error()}
	}
	issues = append(issues, wantedPopulation.PopulationIssues...)
	if input.PolicyID != ContinuousLearningPolicyV1 || input.Population.PolicyID != ContinuousLearningPolicyV1 ||
		!reflect.DeepEqual(input.Thresholds, FixedContinuousLearningThresholds()) {
		issues = append(issues, "continuous-learning fixed policy binding is invalid")
	}
	if !reflect.DeepEqual(*input.Population, wantedPopulation) || !reflect.DeepEqual(input.Cases, wantedCases) {
		issues = append(issues, "continuous-learning population does not match the complete deterministic replay")
	}
	return uniqueSorted(issues)
}

func buildContinuousPopulation(store *ledger.Store, prefixCount int, portableRoot,
	oracleRegistryPath, corpusID string, bound *EvaluationPopulation) ([]EvaluationCase, EvaluationPopulation, error) {
	population := EvaluationPopulation{
		SchemaVersion: EvaluationPopulationSchema, PolicyID: ContinuousLearningPolicyV1,
		CategoryCounts: map[CaseCategory]int{}, RequiredAgents: []ledger.Agent{
			ledger.AgentCodex, ledger.AgentClaudeCode, ledger.AgentOpenCode,
		}, SystemUnderTestSHA256: map[ledger.Agent]string{}, UnpairedAttemptIDs: []string{}, PopulationIssues: []string{},
	}
	_, ordered, err := loadIndexedRecords(store)
	if err != nil {
		return nil, population, err
	}
	if prefixCount < 1 || prefixCount > len(ordered) {
		return nil, population, errors.New("evaluation ledger prefix is unavailable")
	}
	ordered = ordered[:prefixCount]
	records := make(map[string]indexedRecord, len(ordered))
	for index, record := range ordered {
		if _, duplicate := records[record.Event.EventID]; duplicate {
			return nil, population, errors.New("evaluation ledger prefix repeats an event id")
		}
		records[record.Event.EventID] = indexedRecord{Record: record, Index: index + 1}
	}
	systemArtifactSHA256, systemArtifactErr := CurrentSystemArtifactSHA256()
	if systemArtifactErr != nil {
		population.PopulationIssues = append(population.PopulationIssues,
			"system artifact manifest unavailable: "+systemArtifactErr.Error())
	} else {
		population.SystemArtifactSHA256 = systemArtifactSHA256
		population.Prerequisites.SystemArtifactManifest = true
	}
	population.LedgerRecordCount = prefixCount
	population.LedgerLastRecordHash = ordered[len(ordered)-1].RecordHash

	if !validCorpusID(corpusID) {
		return nil, population, errors.New("verified frozen regression corpus is required")
	}
	verification := VerifyCorpus(store, corpusID)
	if len(verification.Issues) != 0 {
		return nil, population, errors.New("frozen regression corpus verification failed")
	}
	manifest, err := LoadCorpusManifest(store, corpusID)
	if err != nil {
		return nil, population, errors.New("frozen regression corpus manifest is unavailable")
	}
	population.CorpusID = corpusID
	population.CorpusContentSHA256 = manifest.CorpusContentSHA256
	population.Prerequisites.FrozenCorpusVerified = true

	var registry loadedOracleRegistry
	if bound != nil {
		if bound.OracleRegistryBlob == nil {
			return nil, population, errors.New("bound oracle registry snapshot is required")
		}
		registry, err = loadOracleRegistryBlob(store, *bound.OracleRegistryBlob)
	} else {
		registry, err = loadOracleRegistry(oracleRegistryPath)
	}
	if err != nil {
		return nil, population, err
	}
	population.OracleRegistrySHA256 = registry.SHA256
	if bound == nil {
		blob, putErr := store.PutBlob(bytes.NewReader(registry.Data))
		if putErr != nil {
			return nil, population, putErr
		}
		population.OracleRegistryBlob = &blob
	} else {
		copy := *bound.OracleRegistryBlob
		population.OracleRegistryBlob = &copy
	}

	var portableRevisions map[string]portable.Revision
	var activeRevisions []portable.Revision
	if bound != nil {
		if bound.PortableStateBlob == nil {
			return nil, population, errors.New("bound portable state snapshot is required")
		}
		portableRevisions, activeRevisions, err = loadPortablePopulationSnapshot(store, *bound.PortableStateBlob)
		if err != nil {
			return nil, population, err
		}
		copy := *bound.PortableStateBlob
		population.PortableStateBlob = &copy
	} else {
		var portableIssues []string
		portableRevisions, portableIssues = loadPortableRevisions(store.Root(), portableRoot, true)
		if len(portableIssues) != 0 {
			return nil, population, errors.New(strings.Join(portableIssues, "; "))
		}
		activeRevisions, portableReport := portable.LoadActiveRevisions(portableRoot)
		if len(portableReport.Issues) != 0 {
			return nil, population, errors.New("portable repository verification failed")
		}
		if err := validatePortablePopulationCompleteness(store, portableRevisions, activeRevisions); err != nil {
			return nil, population, err
		}
		blob, putErr := storePortablePopulationSnapshot(store, portableRevisions, activeRevisions)
		if putErr != nil {
			return nil, population, putErr
		}
		population.PortableStateBlob = &blob
	}
	population.Prerequisites.CompletePortablePopulation = true
	population.PortableStateSHA256 = population.PortableStateBlob.SHA256

	attestations := loadPopulationAttestations(store, records)
	retrievalReceipts, adoptions, selectedRevisions := loadPopulationRetrievals(store, records, &population)
	attempts := loadPopulationAttempts(store, records, ordered, registry, &population)
	population.Prerequisites.ExecutionSupervisorReceipts =
		validatePopulationExecutionReceipts(store, records, ordered, attempts, &population)
	// The local execution supervisor binds exact adapter bytes, input, output,
	// and ledger causality. It does not yet prove that Codex, Claude Code, or
	// OpenCode itself produced the result, so this prerequisite deliberately
	// remains false until an Agent-specific bridge supplies verifiable provenance.
	population.Prerequisites.VerifiedAgentExecution = false
	if population.Prerequisites.ExecutionSupervisorReceipts {
		population.PopulationIssues = append(population.PopulationIssues,
			"verified native Agent execution provenance is unavailable; supervised local adapter results are diagnostic only")
	}

	var captureSnapshot capturesupervisor.EvaluationSnapshot
	if bound != nil {
		if bound.CaptureSnapshotBlob == nil {
			return nil, population, errors.New("bound capture evaluation snapshot is required")
		}
		captureSnapshot, err = loadCapturePopulationSnapshot(
			store, *bound.CaptureSnapshotBlob, population.RequiredAgents,
		)
		if err != nil {
			return nil, population, err
		}
		copy := *bound.CaptureSnapshotBlob
		population.CaptureSnapshotBlob = &copy
	} else {
		captureSnapshot, err = capturesupervisor.LoadEvaluationSnapshot(store, population.RequiredAgents)
		if err != nil {
			return nil, population, fmt.Errorf("load independent capture inventory: %w", err)
		}
		blob, putErr := storeCapturePopulationSnapshot(store, captureSnapshot)
		if putErr != nil {
			return nil, population, putErr
		}
		population.CaptureSnapshotBlob = &blob
	}
	population.CaptureSnapshotSHA256 = captureSnapshot.SnapshotSHA256

	cases := []EvaluationCase{}
	captureCases, independentInventory, projectionCoverage, captureIssues :=
		buildCapturePopulation(store, ordered, captureSnapshot, population.RequiredAgents,
			latestTrialEvidenceTime(attempts, records))
	population.Prerequisites.IndependentCaptureInventory = independentInventory
	population.Prerequisites.NormalizedProjectionCoverage = projectionCoverage
	population.PopulationIssues = append(population.PopulationIssues, captureIssues...)
	cases = append(cases, captureCases...)
	if independentInventory && len(captureCases) == 0 {
		population.PopulationIssues = append(population.PopulationIssues,
			"independent capture inventory has no eligible subjects")
	}

	active := map[string]portable.Revision{}
	for _, revision := range activeRevisions {
		active[revision.RevisionID] = revision
	}
	memoryRevisionIDs := map[string]struct{}{}
	for revisionID := range active {
		memoryRevisionIDs[revisionID] = struct{}{}
	}
	for revisionID := range selectedRevisions {
		memoryRevisionIDs[revisionID] = struct{}{}
	}
	for _, revisionID := range sortedKeys(memoryRevisionIDs) {
		revision, exists := portableRevisions[revisionID]
		if !exists {
			population.PopulationIssues = append(population.PopulationIssues, "retrieved portable revision is unavailable: "+revisionID)
			continue
		}
		caseID := populationCaseID(CategoryFalseMemory, revisionID)
		measurement := &MemoryMeasurement{MemoryID: revision.MemoryID, Label: MemoryUnknown,
			Active: active[revisionID].RevisionID != "", Retrieved: selectedRevisions[revisionID]}
		evidence := []EvidenceReference{{Kind: "portable_revision", ID: revisionID, SHA256: revision.TextSHA256}}
		if attestation, record, ok := onePopulationAttestation(attestations, caseID); ok {
			if attestation.Category == CategoryFalseMemory && attestation.Memory != nil &&
				attestation.Memory.MemoryID == measurement.MemoryID && attestation.Memory.Active == measurement.Active &&
				attestation.Memory.Retrieved == measurement.Retrieved {
				measurement = attestation.Memory
				evidence = append(evidence, ledgerEvidence(record.Record))
			} else {
				population.PopulationIssues = append(population.PopulationIssues, "false-memory attestation subject mismatch: "+caseID)
			}
		} else {
			population.PopulationIssues = append(population.PopulationIssues, "false-memory attestation missing or ambiguous: "+caseID)
		}
		cases = append(cases, EvaluationCase{CaseID: caseID, Category: CategoryFalseMemory,
			Agent: ledger.AgentUnknown, Evidence: evidence, Memory: measurement})
	}

	correctionGroups := map[string][]TaskAttemptReceipt{}
	for _, attempt := range attempts {
		if attempt.SemanticKeySHA256 != "" {
			key := string(attempt.Agent) + "\x00" + attempt.SemanticKeySHA256
			correctionGroups[key] = append(correctionGroups[key], attempt)
		}
	}
	for _, key := range sortedKeys(correctionGroups) {
		group := correctionGroups[key]
		sort.Slice(group, func(i, j int) bool {
			return records[group[i].ReceiptID].Index < records[group[j].ReceiptID].Index
		})
		initialIndex := -1
		for index, attempt := range group {
			if attempt.Measurement.UserCorrections > 0 {
				initialIndex = index
				break
			}
		}
		parts := strings.SplitN(key, "\x00", 2)
		if initialIndex < 0 {
			continue
		}
		var followup *TaskAttemptReceipt
		for index := initialIndex + 1; index < len(group); index++ {
			if group[index].Condition == TaskConditionMemory {
				candidate := group[index]
				followup = &candidate
				break
			}
		}
		if followup == nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"corrected semantic key has no later memory-conditioned follow-up: "+parts[1])
			continue
		}
		repeated := 0
		if followup.Measurement.UserCorrections > 0 {
			repeated = 1
		}
		initial := group[initialIndex]
		evidence := []EvidenceReference{
			ledgerEvidence(records[initial.ReceiptID].Record),
			ledgerEvidence(records[followup.ReceiptID].Record),
		}
		cases = append(cases, EvaluationCase{
			CaseID: populationCaseID(CategoryRepeatedCorrection, key), Category: CategoryRepeatedCorrection,
			Agent: ledger.Agent(parts[0]), Evidence: evidence,
			Correction: &CorrectionMeasurement{SemanticKeySHA256: parts[1],
				InitialCorrectionAttemptID:    initial.ReceiptID,
				AttemptIDs:                    []string{followup.ReceiptID},
				EligibleFollowupOpportunities: 1, RepeatedCorrections: repeated,
				RepeatedCorrectionsAfterMemory: repeated},
		})
	}

	groundTruth, groundTruthRecord, groundTruthIssues := loadSealedCompactionGroundTruth(
		store, records, ordered, manifest.CorpusID, manifest.CorpusContentSHA256)
	population.PopulationIssues = append(population.PopulationIssues, groundTruthIssues...)
	population.Prerequisites.SealedCompactionGroundTruth =
		groundTruthRecord != nil && len(groundTruthIssues) == 0
	compactionCases, derivation, independentDetector, compactionIssues :=
		buildCompactionPopulation(store, prefixCount, population.LedgerLastRecordHash, ordered,
			groundTruth, groundTruthRecord)
	population.EpisodeDerivationVersion = derivation.DerivationVersion
	population.EpisodesSHA256 = derivation.EpisodesSHA256
	population.TimelineSHA256 = derivation.TimelineSHA256
	population.Prerequisites.IndependentCompactionDetector = independentDetector
	population.PopulationIssues = append(population.PopulationIssues, compactionIssues...)
	cases = append(cases, compactionCases...)
	frozenRolloutSources := map[string]RolloutReference{}
	for _, rollout := range manifest.Rollouts {
		if rollout.Status == "captured" {
			frozenRolloutSources[rollout.SourcePathSHA256] = rollout
		}
	}
	freezePrefix := 0
	for index, record := range ordered {
		if record.RecordHash == manifest.SourceLedgerLastRecordHash {
			freezePrefix = index + 1
			break
		}
	}
	if freezePrefix == 0 {
		population.PopulationIssues = append(population.PopulationIssues,
			"frozen corpus ledger prefix is unavailable")
	}
	for _, evaluationCase := range compactionCases {
		for _, reference := range evaluationCase.Evidence {
			indexed, exists := records[reference.ID]
			if !exists || freezePrefix == 0 || indexed.Index > freezePrefix ||
				indexed.Record.Event.Kind != ledger.KindCompaction {
				continue
			}
			event := indexed.Record.Event
			rollout, frozen := frozenRolloutSources[event.Source.SourcePathHash]
			if !frozen || !eventInsideFrozenRollout(event, rollout) {
				continue
			}
			switch evaluationCase.Compaction.Expected {
			case DriftDetected:
				population.Prerequisites.FrozenCompactionDriftControl = true
			case DriftPreserved:
				population.Prerequisites.FrozenCompactionPreserveControl = true
			}
		}
	}

	for _, retrievalID := range sortedKeys(retrievalReceipts) {
		receipt := retrievalReceipts[retrievalID]
		evidence := []EvidenceReference{ledgerEvidence(records[retrievalID].Record)}
		outcomes := map[OutcomeLabel]struct{}{}
		adopted := false
		for _, adoption := range adoptions[retrievalID] {
			evidence = append(evidence, ledgerEvidence(records[adoption.AdoptionID].Record))
			for _, item := range adoption.Items {
				if item.Adoption == retrieval.AdoptionAdopted {
					adopted = true
					outcomes[OutcomeLabel(item.Outcome)] = struct{}{}
				}
			}
		}
		cases = append(cases, EvaluationCase{CaseID: populationCaseID(CategoryRetrievalCost, retrievalID),
			Category: CategoryRetrievalCost, Agent: receipt.Request.Context.Agent, Evidence: evidence,
			Retrieval: &RetrievalMeasurement{RetrievalID: retrievalID,
				EstimatedTokens: receipt.Result.SelectedEstimatedTokens, UTF8Bytes: receipt.Result.SelectedBytes,
				SelectedItems: len(receipt.Result.Selected), Adopted: adopted, Outcome: summarizedOutcome(outcomes)}})
	}

	pairGroups := map[string]struct{ baseline, treatment []TaskAttemptReceipt }{}
	for _, attempt := range attempts {
		key := attempt.TrialPlanID + "\x00" + attempt.TrialPairID
		group := pairGroups[key]
		if attempt.Condition == TaskConditionBaseline {
			group.baseline = append(group.baseline, attempt)
		} else {
			group.treatment = append(group.treatment, attempt)
		}
		pairGroups[key] = group
	}
	pairedTaskSpecs := map[string]string{}
	for _, key := range sortedKeys(pairGroups) {
		group := pairGroups[key]
		sort.Slice(group.baseline, func(i, j int) bool {
			return records[group.baseline[i].ReceiptID].Index < records[group.baseline[j].ReceiptID].Index
		})
		sort.Slice(group.treatment, func(i, j int) bool {
			return records[group.treatment[i].ReceiptID].Index < records[group.treatment[j].ReceiptID].Index
		})
		if len(group.baseline) != 1 || len(group.treatment) != 1 {
			for _, attempt := range append(group.baseline, group.treatment...) {
				population.UnpairedAttemptIDs = append(population.UnpairedAttemptIDs, attempt.ReceiptID)
			}
			continue
		}
		baseline, treatment := group.baseline[0], group.treatment[0]
		pairID := adapterjsonl.DeterministicID("pair", baseline.TrialPlanID, baseline.TrialPairID)
		if previousPair, reused := pairedTaskSpecs[baseline.TaskSpecSHA256]; reused {
			population.UnpairedAttemptIDs = append(population.UnpairedAttemptIDs,
				baseline.ReceiptID, treatment.ReceiptID)
			population.PopulationIssues = append(population.PopulationIssues,
				"task specification is reused across paired evaluation units: "+previousPair+":"+pairID)
			continue
		}
		pairedTaskSpecs[baseline.TaskSpecSHA256] = pairID
		cases = append(cases, EvaluationCase{CaseID: populationCaseID(CategoryPairedOutcome, pairID),
			Category: CategoryPairedOutcome, Agent: baseline.Agent,
			Evidence: []EvidenceReference{ledgerEvidence(records[baseline.ReceiptID].Record),
				ledgerEvidence(records[treatment.ReceiptID].Record)},
			PairedOutcome: &PairedOutcomeMeasurement{PairID: pairID,
				BaselineAttemptID: baseline.ReceiptID, TreatmentAttemptID: treatment.ReceiptID,
				Baseline: baseline.Measurement, Treatment: treatment.Measurement}})
	}
	if len(population.UnpairedAttemptIDs) != 0 {
		sort.Strings(population.UnpairedAttemptIDs)
		population.PopulationIssues = append(population.PopulationIssues, "task attempt population contains unpaired attempts")
	}
	population.PopulationIssues = append(population.PopulationIssues,
		efficacyPrerequisiteIssues(population.Prerequisites)...)

	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Category != cases[j].Category {
			return cases[i].Category < cases[j].Category
		}
		return cases[i].CaseID < cases[j].CaseID
	})
	pairedByAgent := map[ledger.Agent]int{}
	correctionsByAgent := map[ledger.Agent]int{}
	for _, evaluationCase := range cases {
		population.CategoryCounts[evaluationCase.Category]++
		switch evaluationCase.Category {
		case CategoryPairedOutcome:
			pairedByAgent[evaluationCase.Agent]++
		case CategoryRepeatedCorrection:
			correctionsByAgent[evaluationCase.Agent]++
		}
	}
	for _, agent := range population.RequiredAgents {
		if pairedByAgent[agent] < 3 {
			population.PopulationIssues = append(population.PopulationIssues,
				"paired evaluation stratum has fewer than 3 independent tasks: "+string(agent))
		}
		if correctionsByAgent[agent] < 3 {
			population.PopulationIssues = append(population.PopulationIssues,
				"correction evaluation stratum has fewer than 3 independent semantic keys: "+string(agent))
		}
	}
	for _, category := range []CaseCategory{CategoryCaptureCoverage, CategoryFalseMemory,
		CategoryRepeatedCorrection, CategoryCompactionDrift, CategoryRetrievalCost, CategoryPairedOutcome} {
		if population.CategoryCounts[category] == 0 {
			population.PopulationIssues = append(population.PopulationIssues, "population category has no eligible subjects: "+string(category))
		}
	}
	caseData, _ := json.Marshal(cases)
	population.CaseSetSHA256 = sha256Hex(caseData)
	population.PopulationIssues = uniqueSorted(population.PopulationIssues)
	return cases, population, nil
}

const portablePopulationSnapshotSchema = "portable-population-snapshot/v1alpha1"

type portablePopulationSnapshot struct {
	SchemaVersion string              `json:"schema_version"`
	Active        []string            `json:"active_revision_ids"`
	Revisions     []portable.Revision `json:"revisions"`
	Privacy       string              `json:"privacy"`
}

func storePortablePopulationSnapshot(store *ledger.Store, revisions map[string]portable.Revision,
	active []portable.Revision) (ledger.BlobRef, error) {
	activeIDs := make([]string, 0, len(active))
	for _, revision := range active {
		activeIDs = append(activeIDs, revision.RevisionID)
	}
	sort.Strings(activeIDs)
	all := make([]portable.Revision, 0, len(revisions))
	for _, revision := range revisions {
		all = append(all, revision)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].RevisionID < all[j].RevisionID })
	snapshot := portablePopulationSnapshot{SchemaVersion: portablePopulationSnapshotSchema,
		Active: activeIDs, Revisions: all, Privacy: "local_only"}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return ledger.BlobRef{}, err
	}
	return store.PutBlob(bytes.NewReader(data))
}

func loadPortablePopulationSnapshot(store *ledger.Store, reference ledger.BlobRef) (
	map[string]portable.Revision, []portable.Revision, error,
) {
	data, err := readAttemptBlob(store, reference)
	if err != nil {
		return nil, nil, fmt.Errorf("read portable population snapshot: %w", err)
	}
	var snapshot portablePopulationSnapshot
	if decodeStrictEvaluationJSON(data, &snapshot) != nil ||
		snapshot.SchemaVersion != portablePopulationSnapshotSchema || snapshot.Active == nil ||
		snapshot.Revisions == nil || snapshot.Privacy != "local_only" {
		return nil, nil, errors.New("portable population snapshot is invalid")
	}
	revisions := make(map[string]portable.Revision, len(snapshot.Revisions))
	for index, revision := range snapshot.Revisions {
		if strings.TrimSpace(revision.RevisionID) == "" ||
			(index > 0 && snapshot.Revisions[index-1].RevisionID >= revision.RevisionID) {
			return nil, nil, errors.New("portable population revisions are not sorted and unique")
		}
		local, _, err := portable.ResolveLocalRevision(store, revision.MemoryID, revision.RevisionID)
		if err != nil {
			return nil, nil, errors.New("portable population revision lacks local promoted evidence")
		}
		if !reflect.DeepEqual(local, revision) {
			return nil, nil, errors.New("portable population revision differs from local promoted evidence")
		}
		revisions[revision.RevisionID] = revision
	}
	active := make([]portable.Revision, 0, len(snapshot.Active))
	for index, revisionID := range snapshot.Active {
		if strings.TrimSpace(revisionID) == "" || (index > 0 && snapshot.Active[index-1] >= revisionID) {
			return nil, nil, errors.New("portable population active revisions are not sorted and unique")
		}
		revision, exists := revisions[revisionID]
		if !exists || revision.Status != portable.StatusActive {
			return nil, nil, errors.New("portable population active revision is unavailable")
		}
		active = append(active, revision)
	}
	if err := validatePortablePopulationCompleteness(store, revisions, active); err != nil {
		return nil, nil, err
	}
	canonical, err := json.Marshal(snapshot)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, nil, errors.New("portable population snapshot is not canonical")
	}
	return revisions, active, nil
}

func validatePortablePopulationCompleteness(store *ledger.Store, revisions map[string]portable.Revision,
	active []portable.Revision) error {
	expectedRevisions, expectedActive, err := portable.LoadLocalPopulation(store)
	if err != nil {
		return fmt.Errorf("reconstruct complete local portable population: %w", err)
	}
	if !reflect.DeepEqual(revisions, expectedRevisions) || !reflect.DeepEqual(active, expectedActive) {
		return errors.New("portable population omits or adds local promoted evidence")
	}
	return nil
}

func eventInsideFrozenRollout(event ledger.Event, rollout RolloutReference) bool {
	if event.Source.ByteStart == nil || event.Source.ByteEnd == nil ||
		*event.Source.ByteStart < 0 || *event.Source.ByteEnd <= *event.Source.ByteStart {
		return false
	}
	covered := false
	for _, interval := range rollout.CoveredRanges {
		if *event.Source.ByteStart >= interval.Start && *event.Source.ByteEnd <= interval.End {
			covered = true
			break
		}
	}
	if !covered || event.Causality == nil {
		return false
	}
	snapshots := map[string]struct{}{}
	for _, eventID := range rollout.SnapshotEventIDs {
		snapshots[eventID] = struct{}{}
	}
	for _, parent := range event.Causality.ParentEventIDs {
		if _, exists := snapshots[parent]; exists {
			return true
		}
	}
	return false
}

type populationAttestation struct {
	Attestation EvaluationAttestation
	Record      indexedRecord
}

func loadPopulationAttestations(store *ledger.Store,
	records map[string]indexedRecord) map[string][]populationAttestation {
	result := map[string][]populationAttestation{}
	for _, record := range records {
		if record.Record.Event.Kind != ledger.KindEvaluationAttestation {
			continue
		}
		data, err := eventPayload(store, record.Record.Event)
		if err != nil {
			continue
		}
		attestation, err := DecodeAttestation(bytes.NewReader(data))
		if err != nil || validateAttestationRecord(store, EvaluationCase{}, record.Record) != nil {
			continue
		}
		result[attestation.CaseID] = append(result[attestation.CaseID],
			populationAttestation{Attestation: attestation, Record: record})
	}
	return result
}

func onePopulationAttestation(attestations map[string][]populationAttestation,
	caseID string) (EvaluationAttestation, indexedRecord, bool) {
	items := attestations[caseID]
	if len(items) != 1 {
		return EvaluationAttestation{}, indexedRecord{}, false
	}
	return items[0].Attestation, items[0].Record, true
}

func loadPopulationRetrievals(store *ledger.Store, records map[string]indexedRecord,
	population *EvaluationPopulation) (map[string]retrieval.Receipt, map[string][]retrieval.AdoptionReceipt, map[string]bool) {
	receipts := map[string]retrieval.Receipt{}
	adoptions := map[string][]retrieval.AdoptionReceipt{}
	selected := map[string]bool{}
	for _, record := range records {
		data, err := eventPayload(store, record.Record.Event)
		if err != nil {
			if record.Record.Event.Kind == ledger.KindRetrieval || record.Record.Event.Kind == ledger.KindAdoption {
				population.PopulationIssues = append(population.PopulationIssues, "memory receipt payload is unavailable: "+record.Record.Event.EventID)
			}
			continue
		}
		switch record.Record.Event.Kind {
		case ledger.KindRetrieval:
			var receipt retrieval.Receipt
			if decodeStrictEvaluationJSON(data, &receipt) != nil || receipt.ReceiptID != record.Record.Event.EventID {
				population.PopulationIssues = append(population.PopulationIssues, "retrieval receipt is invalid: "+record.Record.Event.EventID)
				continue
			}
			receipts[receipt.ReceiptID] = receipt
			for _, match := range receipt.Result.Selected {
				selected[match.RevisionID] = true
			}
		case ledger.KindAdoption:
			var adoption retrieval.AdoptionReceipt
			if decodeStrictEvaluationJSON(data, &adoption) != nil || adoption.AdoptionID != record.Record.Event.EventID {
				population.PopulationIssues = append(population.PopulationIssues, "adoption receipt is invalid: "+record.Record.Event.EventID)
				continue
			}
			adoptions[adoption.RetrievalReceiptID] = append(adoptions[adoption.RetrievalReceiptID], adoption)
		}
	}
	for retrievalID := range adoptions {
		sort.Slice(adoptions[retrievalID], func(i, j int) bool {
			return adoptions[retrievalID][i].AdoptionID < adoptions[retrievalID][j].AdoptionID
		})
		if _, exists := receipts[retrievalID]; !exists {
			population.PopulationIssues = append(population.PopulationIssues, "adoption references retrieval outside the population prefix: "+retrievalID)
		}
	}
	return receipts, adoptions, selected
}

func loadPopulationAttempts(store *ledger.Store, records map[string]indexedRecord,
	ordered []ledger.Record, registry loadedOracleRegistry, population *EvaluationPopulation) []TaskAttemptReceipt {
	if population.SystemUnderTestSHA256 == nil {
		population.SystemUnderTestSHA256 = map[ledger.Agent]string{}
	}
	planned, sealed := loadPopulationTrialPlans(store, records, ordered, population)
	population.Prerequisites.PairedTrialPlanSealed = sealed

	contracts := map[string]struct {
		contract TaskAttemptContract
		eventID  string
	}{}
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindTaskAttemptContract {
			continue
		}
		if record.Event.Payload == nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt contract payload is unavailable: "+record.Event.EventID)
			continue
		}
		data, err := eventPayload(store, record.Event)
		if err != nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt contract payload is unavailable: "+record.Event.EventID)
			continue
		}
		var contract TaskAttemptContract
		if decodeStrictEvaluationJSON(data, &contract) != nil ||
			contract.SchemaVersion != TaskAttemptContractSchema {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt contract cannot be decoded: "+record.Event.EventID)
			continue
		}
		if contract.TrialPlanID == "" && contract.TrialPairID == "" {
			continue
		}
		plannedArm, exists := planned[contract.AttemptID]
		if !exists || plannedArm.PlanID != contract.TrialPlanID || plannedArm.PairID != contract.TrialPairID ||
			!reflect.DeepEqual(plannedArm.Arm.Contract, contract) {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt is outside its sealed trial plan: "+record.Event.EventID)
			continue
		}
		if !safeIdentifier(contract.TaskID) || !safeIdentifier(contract.AttemptID) ||
			!validAgent(contract.Agent) || contract.Agent == ledger.AgentUnknown ||
			contract.Privacy != "local_only" || !validSHA256(contract.SystemArtifactSHA256) ||
			contract.SystemArtifactSHA256 != population.SystemArtifactSHA256 ||
			!validSHA256(contract.SystemUnderTestSHA256) || contract.SystemUnderTestBlob == nil ||
			contract.SystemUnderTestBlob.SHA256 != contract.SystemUnderTestSHA256 ||
			verifyBlob(store, *contract.SystemUnderTestBlob) != nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt contract is invalid: "+record.Event.EventID)
			continue
		}
		request := taskAttemptRequestFromContract(contract, record.Event.EventID)
		if validateAttemptBlobs(store, request) != nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt system-under-test manifest is invalid: "+record.Event.EventID)
			continue
		}
		if previous := population.SystemUnderTestSHA256[contract.Agent]; previous != "" &&
			previous != contract.SystemUnderTestSHA256 {
			population.PopulationIssues = append(population.PopulationIssues,
				"evaluation population mixes system-under-test manifests for "+string(contract.Agent))
		} else {
			population.SystemUnderTestSHA256[contract.Agent] = contract.SystemUnderTestSHA256
		}
		if _, duplicate := contracts[contract.AttemptID]; duplicate {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt id is duplicated: "+contract.AttemptID)
			continue
		}
		contracts[contract.AttemptID] = struct {
			contract TaskAttemptContract
			eventID  string
		}{contract: contract, eventID: record.Event.EventID}
	}

	trustedBuiltin := len(contracts) != 0
	for _, preregistered := range contracts {
		oracle := preregistered.contract.Oracle
		entry, exists := registry.Entries[oracleRegistryKey(oracle.Kind, oracle.ID, oracle.Version)]
		if oracle.Kind != "builtin" || oracle.ID != "evidence-score" || oracle.Version != "v1" ||
			!validSHA256(oracle.RegistryEntrySHA256) || !exists ||
			oracleEntrySHA256(entry) != oracle.RegistryEntrySHA256 {
			trustedBuiltin = false
			break
		}
	}
	if trustedBuiltin {
		population.Prerequisites.BlindOracleProtocol = true
		population.Prerequisites.HermeticOracleExecution = true
	}

	result := []TaskAttemptReceipt{}
	receipts := map[string]struct{}{}
	for _, record := range ordered {
		if record.Event.Kind != ledger.KindTaskAttempt {
			continue
		}
		receipt, err := decodeTaskAttemptReceipt(store, record.Event)
		if err != nil {
			population.PopulationIssues = append(population.PopulationIssues, "task attempt receipt is invalid: "+record.Event.EventID)
			continue
		}
		if receipt.TrialPlanID == "" && receipt.TrialPairID == "" {
			continue
		}
		plannedArm, plannedExists := planned[receipt.AttemptID]
		if !plannedExists || plannedArm.PlanID != receipt.TrialPlanID || plannedArm.PairID != receipt.TrialPairID {
			population.PopulationIssues = append(population.PopulationIssues,
				"task attempt receipt is outside its sealed trial plan: "+receipt.ReceiptID)
			continue
		}
		start, exists := contracts[receipt.AttemptID]
		if !exists || start.eventID != receipt.WindowStart.EventID ||
			!taskAttemptContractMatches(start.contract, taskAttemptRequestFromReceipt(receipt)) {
			population.PopulationIssues = append(population.PopulationIssues,
				"task attempt receipt lacks its exact preregistered contract: "+receipt.ReceiptID)
		}
		if _, duplicate := receipts[receipt.AttemptID]; duplicate {
			population.PopulationIssues = append(population.PopulationIssues,
				"task attempt has multiple receipts: "+receipt.AttemptID)
		}
		receipts[receipt.AttemptID] = struct{}{}
		if err := rerunTaskAttempt(store, receipt, records, ordered, registry); err != nil {
			population.PopulationIssues = append(population.PopulationIssues,
				"task attempt oracle replay failed: "+receipt.ReceiptID+": "+err.Error())
		}
		result = append(result, receipt)
	}
	if len(contracts) == 0 {
		population.PopulationIssues = append(population.PopulationIssues,
			"preregistered task attempt universe is empty")
	}
	for attemptID := range contracts {
		if _, exists := receipts[attemptID]; !exists {
			population.PopulationIssues = append(population.PopulationIssues,
				"preregistered task attempt has no receipt: "+attemptID)
		}
	}
	if len(contracts) != 0 && len(contracts) == len(planned) {
		population.Prerequisites.PreregisteredAttemptUniverse = true
	}
	return result
}

func comparableAttemptKey(receipt TaskAttemptReceipt) string {
	values := []string{string(receipt.Agent), receipt.TaskSpecSHA256,
		receipt.AcceptanceCriteriaSHA256, receipt.ExecutionConfigSHA256, receipt.SemanticKeySHA256,
		receipt.Oracle.Kind, receipt.Oracle.ID, receipt.Oracle.Version, receipt.Oracle.RegistryEntrySHA256,
		receipt.SystemArtifactSHA256, receipt.SystemUnderTestSHA256}
	return strings.Join(values, "\x00")
}

func taskAttemptRequestFromContract(contract TaskAttemptContract, startEventID string) TaskAttemptRequest {
	return TaskAttemptRequest{
		SchemaVersion: TaskAttemptRequestSchema, TaskID: contract.TaskID, AttemptID: contract.AttemptID,
		TrialPlanID: contract.TrialPlanID, TrialPairID: contract.TrialPairID,
		Agent: contract.Agent, SemanticKeySHA256: contract.SemanticKeySHA256,
		TaskSpecSHA256: contract.TaskSpecSHA256, AcceptanceCriteriaSHA256: contract.AcceptanceCriteriaSHA256,
		ExecutionConfigSHA256: contract.ExecutionConfigSHA256,
		SystemArtifactSHA256:  contract.SystemArtifactSHA256,
		SystemUnderTestSHA256: contract.SystemUnderTestSHA256, SystemUnderTestBlob: contract.SystemUnderTestBlob,
		TaskSpecBlob: contract.TaskSpecBlob, AcceptanceCriteriaBlob: contract.AcceptanceCriteriaBlob,
		ExecutionConfigBlob: contract.ExecutionConfigBlob, Condition: contract.Condition,
		WindowStartEventID: startEventID, WindowEndEventID: contract.WindowEndEventID,
		MemoryReferences: []retrieval.MemoryReference{}, Oracle: contract.Oracle, Privacy: contract.Privacy,
	}
}

func latestTrialEvidenceTime(attempts []TaskAttemptReceipt, records map[string]indexedRecord) time.Time {
	latest := time.Time{}
	for _, attempt := range attempts {
		for _, eventID := range []string{attempt.WindowEnd.EventID, attempt.Verdict.EventID} {
			indexed, exists := records[eventID]
			if exists && indexed.Record.Event.RecordedAt.After(latest) {
				latest = indexed.Record.Event.RecordedAt.UTC()
			}
		}
	}
	return latest
}

func populationCaseID(category CaseCategory, subject string) string {
	digest := sha256.Sum256([]byte(string(category) + "\x00" + subject))
	return adapterjsonl.DeterministicID("evaluation-case", string(category), hex.EncodeToString(digest[:]))
}

func ledgerEvidence(record ledger.Record) EvidenceReference {
	return EvidenceReference{Kind: "ledger_event", ID: record.Event.EventID, SHA256: record.RecordHash}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
