package evaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rrrrrredy/agent-memory-system/internal/adapterjsonl"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/retrieval"
)

const (
	evaluationAdapterName    = "learning-evaluation"
	evaluationAdapterVersion = "learning-evaluation/v1alpha2"
	runVerificationSchema    = "learning-evaluation-verification/v1alpha1"
)

func Run(store *ledger.Store, input EvaluationInput, options RunOptions) (result RunResult, returnedErr error) {
	if store == nil {
		return result, errors.New("store is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	report, err := Calculate(input)
	if err != nil {
		return result, err
	}
	report.Issues = append(report.Issues, verifyContinuousPopulation(
		store, input, options.PortableRoot, options.OracleRegistry, true)...)
	inputData, err := marshalIndented(input)
	if err != nil {
		return result, fmt.Errorf("encode evaluation input: %w", err)
	}

	corpusArtifacts := map[string]CorpusArtifact{}
	var corpusManifest *CorpusManifest
	if input.CorpusID != "" {
		verification := VerifyCorpus(store, input.CorpusID)
		if len(verification.Issues) != 0 {
			for _, issue := range verification.Issues {
				report.Issues = append(report.Issues, "corpus: "+issue)
			}
		} else {
			manifest, loadErr := LoadCorpusManifest(store, input.CorpusID)
			if loadErr != nil {
				report.Issues = append(report.Issues, "corpus manifest unavailable")
			} else {
				corpusManifest = &manifest
				for _, artifact := range manifest.Artifacts {
					corpusArtifacts[artifact.ArtifactID] = artifact
				}
			}
		}
	}

	portableRevisions, portableIssues := loadEvaluationPortableRevisions(store, input, options.PortableRoot)
	report.Issues = append(report.Issues, portableIssues...)
	if hasCategory(input, CategoryRetrievalCost) || hasCategory(input, CategoryRepeatedCorrection) ||
		hasCategory(input, CategoryPairedOutcome) {
		receipts := retrieval.Verify(store)
		for _, issue := range receipts.Issues {
			report.Issues = append(report.Issues, "retrieval receipts: "+issue)
		}
	}

	wantedLedger := map[string]struct{}{}
	for _, evaluationCase := range input.Cases {
		for _, reference := range evaluationCase.Evidence {
			if reference.Kind == "ledger_event" {
				wantedLedger[reference.ID] = struct{}{}
			}
		}
	}
	ledgerRecords := map[string]ledger.Record{}
	allRecords := map[string]indexedRecord{}
	orderedRecords := []ledger.Record{}
	existingRuns := map[string]ledger.Event{}
	appender, err := store.NewAppenderAfterVisit(func(record ledger.Record) error {
		if _, duplicate := allRecords[record.Event.EventID]; duplicate {
			return fmt.Errorf("duplicate evidence event id %q", record.Event.EventID)
		}
		orderedRecords = append(orderedRecords, record)
		allRecords[record.Event.EventID] = indexedRecord{Record: record, Index: len(orderedRecords)}
		if _, wanted := wantedLedger[record.Event.EventID]; wanted {
			ledgerRecords[record.Event.EventID] = record
		}
		if record.Event.Kind == ledger.KindEvaluationRun {
			existingRuns[record.Event.EventID] = record.Event
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("scan evidence for evaluation: %w", err)
	}
	defer func() {
		if closeErr := appender.Close(); returnedErr == nil && closeErr != nil {
			returnedErr = closeErr
		}
	}()

	report.Issues = append(report.Issues, lockedContinuousLedgerIssues(input, orderedRecords)...)

	for _, evaluationCase := range input.Cases {
		caseRecords := map[string]ledger.Record{}
		caseArtifacts := map[string]CorpusArtifact{}
		for _, reference := range evaluationCase.Evidence {
			switch reference.Kind {
			case "ledger_event":
				record, exists := ledgerRecords[reference.ID]
				if !exists {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": ledger evidence missing: "+reference.ID)
					continue
				}
				if record.RecordHash != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": ledger evidence hash mismatch: "+reference.ID)
					continue
				}
				caseRecords[reference.ID] = record
				report.EvidenceReferencesChecked++
			case "corpus_artifact":
				artifact, exists := corpusArtifacts[reference.ID]
				if !exists || artifact.Blob.SHA256 != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": corpus artifact missing or changed: "+reference.ID)
					continue
				}
				caseArtifacts[reference.ID] = artifact
				report.EvidenceReferencesChecked++
			case "portable_revision":
				revision, exists := portableRevisions[reference.ID]
				if !exists || revision.TextSHA256 != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": portable revision missing or changed: "+reference.ID)
					continue
				}
				if evaluationCase.Memory != nil && revision.MemoryID != evaluationCase.Memory.MemoryID {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": portable revision memory_id mismatch")
					continue
				}
				report.EvidenceReferencesChecked++
			}
		}
		caseIssues, attestations := validateCaseEvidence(
			store, evaluationCase, caseRecords, caseArtifacts, corpusManifest,
			allRecords, orderedRecords)
		report.Issues = append(report.Issues, caseIssues...)
		report.AttestedReferences += attestations
	}

	report.Issues = uniqueSorted(report.Issues)
	report.ReleaseReady = len(report.Issues) == 0 && gatesPassed(report.Gates)
	inputBlob, err := store.PutBlob(bytes.NewReader(inputData))
	if err != nil {
		return result, fmt.Errorf("preserve evaluation input: %w", err)
	}
	report.InputBlob = &inputBlob
	if err := setReportHash(&report); err != nil {
		return result, err
	}
	reportData, err := marshalIndented(report)
	if err != nil {
		return result, fmt.Errorf("encode evaluation report: %w", err)
	}
	reportBlob, err := store.PutBlob(bytes.NewReader(reportData))
	if err != nil {
		return result, fmt.Errorf("preserve evaluation report: %w", err)
	}
	runPath := evaluationRunPath(store.Root(), input.SuiteID, input.RunID)
	if err := writeRunFiles(runPath, inputData, reportData); err != nil {
		return result, err
	}
	eventID := evaluationRunEventID(input, report)
	if existing, exists := existingRuns[eventID]; exists {
		if !eventMatchesBlob(existing, reportBlob) {
			return result, errors.New("evaluation run event id collision")
		}
		result = RunResult{Report: report, ReportPath: filepath.Join(runPath, "report.json"),
			ReportEventID: eventID, Reused: true}
		if err := appender.Close(); err != nil {
			return result, err
		}
		return result, nil
	}
	parents := []string{}
	for eventID := range ledgerRecords {
		parents = append(parents, eventID)
	}
	sort.Strings(parents)
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion, EventID: eventID, Kind: ledger.KindEvaluationRun,
		ObservedAt: input.CreatedAt.UTC(), RecordedAt: options.Now().UTC(),
		Source: ledger.Source{
			Agent: ledger.AgentUnknown, Adapter: evaluationAdapterName,
			AdapterVersion: evaluationAdapterVersion, DeviceID: store.DeviceID(), OS: runtime.GOOS,
			ThreadID: input.SuiteID, SessionID: input.RunID, SourceEventID: input.RunID,
			SourcePathHash: inputBlob.SHA256, SourceCursor: "run:" + input.SuiteID + "/" + input.RunID,
		},
		Payload: &ledger.Payload{Encoding: "json", MediaType: "application/json", Blob: &reportBlob,
			SHA256: reportBlob.SHA256, Bytes: reportBlob.Bytes},
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: "local_only"},
	}
	if len(parents) != 0 {
		event.Causality = &ledger.Causality{ParentEventIDs: parents}
	}
	if _, err := appender.AppendBatch([]ledger.Event{event}); err != nil {
		return result, fmt.Errorf("append evaluation run: %w", err)
	}
	if err := appender.Close(); err != nil {
		return result, err
	}
	return RunResult{Report: report, ReportPath: filepath.Join(runPath, "report.json"),
		ReportEventID: eventID}, nil
}

func VerifyRun(store *ledger.Store, suiteID, runID string,
	portableRoots ...string) RunVerificationReport {
	options := RunVerificationOptions{}
	if len(portableRoots) != 0 {
		options.PortableRoot = portableRoots[0]
	}
	return VerifyRunWithOptions(store, suiteID, runID, options)
}

func VerifyRunWithOptions(store *ledger.Store, suiteID, runID string,
	options RunVerificationOptions) RunVerificationReport {
	verification := RunVerificationReport{
		SchemaVersion: runVerificationSchema, SuiteID: suiteID, RunID: runID,
		Issues: []string{}, Privacy: "local_only",
	}
	if store == nil || !safeIdentifier(suiteID) || !safeIdentifier(runID) {
		verification.Issues = append(verification.Issues, "store, suite_id, and run_id are required")
		return verification
	}
	if ledgerVerification := store.Verify(); len(ledgerVerification.Issues) != 0 {
		verification.Issues = append(verification.Issues, "evidence ledger verification failed")
		return verification
	}
	runPath := evaluationRunPath(store.Root(), suiteID, runID)
	inputData, err := os.ReadFile(filepath.Join(runPath, "input.json"))
	if err != nil {
		verification.Issues = append(verification.Issues, "evaluation input is unavailable")
		return verification
	}
	reportData, err := os.ReadFile(filepath.Join(runPath, "report.json"))
	if err != nil {
		verification.Issues = append(verification.Issues, "evaluation report is unavailable")
		return verification
	}
	input, err := DecodeInput(bytes.NewReader(inputData))
	if err != nil {
		verification.Issues = append(verification.Issues, err.Error())
		return verification
	}
	var stored EvaluationReport
	decoder := json.NewDecoder(bytes.NewReader(reportData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		verification.Issues = append(verification.Issues, "decode evaluation report: "+err.Error())
		return verification
	}
	if err := requireJSONEOF(decoder); err != nil || !validateReportHash(stored) {
		verification.Issues = append(verification.Issues, "evaluation report hash is invalid")
		return verification
	}
	if stored.SuiteID != suiteID || stored.RunID != runID || stored.CasesChecked != len(input.Cases) {
		verification.Issues = append(verification.Issues, "evaluation report identity is inconsistent")
	}
	recalculated, calculateErr := Calculate(input)
	if calculateErr != nil {
		verification.Issues = append(verification.Issues, "evaluation input cannot be recalculated")
		return verification
	}
	inputCanonical, _ := json.Marshal(input)
	inputDigest := sha256.Sum256(inputCanonical)
	if stored.InputSHA256 != hex.EncodeToString(inputDigest[:]) {
		verification.Issues = append(verification.Issues, "evaluation input hash is invalid")
	}
	if stored.InputBlob == nil || stored.InputBlob.SHA256 != sha256Hex(inputData) ||
		stored.InputBlob.Bytes != int64(len(inputData)) {
		verification.Issues = append(verification.Issues, "evaluation input blob reference is invalid")
	} else if err := verifyBlob(store, *stored.InputBlob); err != nil {
		verification.Issues = append(verification.Issues, "evaluation input blob content is invalid")
	}
	wantedEvent := evaluationRunEventID(input, stored)
	var runRecord *ledger.Record
	allRecords := map[string]indexedRecord{}
	orderedRecords := []ledger.Record{}
	if err := store.VisitRecords(func(record ledger.Record) error {
		if _, duplicate := allRecords[record.Event.EventID]; duplicate {
			return fmt.Errorf("duplicate event id %s", record.Event.EventID)
		}
		orderedRecords = append(orderedRecords, record)
		allRecords[record.Event.EventID] = indexedRecord{Record: record, Index: len(orderedRecords)}
		if record.Event.EventID == wantedEvent {
			copy := record
			runRecord = &copy
		}
		return nil
	}); err != nil {
		verification.Issues = append(verification.Issues, "read evaluation evidence: "+err.Error())
		return verification
	}
	replayed := replayEvaluationEvidence(store, input, recalculated, options.PortableRoot,
		options.OracleRegistry, allRecords, orderedRecords)
	if !sameReplayedReport(stored, replayed) {
		verification.Issues = append(verification.Issues, "evaluation report does not match complete evidence replay")
	}
	if runRecord == nil || !validEvaluationRunEvent(store, *runRecord, input, stored,
		reportData, allRecords) {
		verification.Issues = append(verification.Issues, "evaluation run event is missing or invalid")
	}
	verification.CasesChecked = stored.CasesChecked
	verification.ReferencesChecked = stored.EvidenceReferencesChecked
	verification.Issues = uniqueSorted(verification.Issues)
	return verification
}

func sameDerivedReport(stored, calculated EvaluationReport) bool {
	return stored.SchemaVersion == calculated.SchemaVersion &&
		stored.EvaluationVersion == calculated.EvaluationVersion &&
		stored.SuiteID == calculated.SuiteID && stored.RunID == calculated.RunID &&
		stored.InputSHA256 == calculated.InputSHA256 && stored.SystemVersion == calculated.SystemVersion &&
		stored.QualityProfile == calculated.QualityProfile && stored.Authority == calculated.Authority &&
		stored.CorpusID == calculated.CorpusID &&
		stored.CasesChecked == calculated.CasesChecked && reflect.DeepEqual(stored.Capture, calculated.Capture) &&
		reflect.DeepEqual(stored.FalseMemory, calculated.FalseMemory) &&
		reflect.DeepEqual(stored.Corrections, calculated.Corrections) &&
		reflect.DeepEqual(stored.CompactionDrift, calculated.CompactionDrift) &&
		reflect.DeepEqual(stored.Retrieval, calculated.Retrieval) &&
		reflect.DeepEqual(stored.Outcomes, calculated.Outcomes) &&
		reflect.DeepEqual(stored.Gates, calculated.Gates) && stored.Privacy == calculated.Privacy
}

func sameReplayedReport(stored, replayed EvaluationReport) bool {
	return sameDerivedReport(stored, replayed) &&
		stored.EvidenceReferencesChecked == replayed.EvidenceReferencesChecked &&
		stored.AttestedReferences == replayed.AttestedReferences &&
		stored.ReleaseReady == replayed.ReleaseReady &&
		reflect.DeepEqual(stored.Issues, replayed.Issues)
}

func replayEvaluationEvidence(store *ledger.Store, input EvaluationInput, report EvaluationReport,
	portableRoot, oracleRegistry string, allRecords map[string]indexedRecord,
	orderedRecords []ledger.Record) EvaluationReport {
	report.Issues = append(report.Issues, verifyContinuousPopulation(
		store, input, portableRoot, oracleRegistry, false)...)
	corpusArtifacts := map[string]CorpusArtifact{}
	var corpusManifest *CorpusManifest
	if input.CorpusID != "" {
		verification := VerifyCorpus(store, input.CorpusID)
		if len(verification.Issues) != 0 {
			for _, issue := range verification.Issues {
				report.Issues = append(report.Issues, "corpus: "+issue)
			}
		} else if manifest, err := LoadCorpusManifest(store, input.CorpusID); err != nil {
			report.Issues = append(report.Issues, "corpus manifest unavailable")
		} else {
			corpusManifest = &manifest
			for _, artifact := range manifest.Artifacts {
				corpusArtifacts[artifact.ArtifactID] = artifact
			}
		}
	}
	portableRevisions, portableIssues := loadEvaluationPortableRevisions(store, input, portableRoot)
	report.Issues = append(report.Issues, portableIssues...)
	if hasCategory(input, CategoryRetrievalCost) || hasCategory(input, CategoryRepeatedCorrection) ||
		hasCategory(input, CategoryPairedOutcome) {
		for _, issue := range retrieval.Verify(store).Issues {
			report.Issues = append(report.Issues, "retrieval receipts: "+issue)
		}
	}
	for _, evaluationCase := range input.Cases {
		caseRecords := map[string]ledger.Record{}
		caseArtifacts := map[string]CorpusArtifact{}
		for _, reference := range evaluationCase.Evidence {
			switch reference.Kind {
			case "ledger_event":
				indexed, exists := allRecords[reference.ID]
				if !exists {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": ledger evidence missing: "+reference.ID)
					continue
				}
				if indexed.Record.RecordHash != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": ledger evidence hash mismatch: "+reference.ID)
					continue
				}
				caseRecords[reference.ID] = indexed.Record
				report.EvidenceReferencesChecked++
			case "corpus_artifact":
				artifact, exists := corpusArtifacts[reference.ID]
				if !exists || artifact.Blob.SHA256 != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": corpus artifact missing or changed: "+reference.ID)
					continue
				}
				caseArtifacts[reference.ID] = artifact
				report.EvidenceReferencesChecked++
			case "portable_revision":
				revision, exists := portableRevisions[reference.ID]
				if !exists || revision.TextSHA256 != reference.SHA256 {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": portable revision missing or changed: "+reference.ID)
					continue
				}
				if evaluationCase.Memory != nil && revision.MemoryID != evaluationCase.Memory.MemoryID {
					report.Issues = append(report.Issues, "case "+evaluationCase.CaseID+": portable revision memory_id mismatch")
					continue
				}
				report.EvidenceReferencesChecked++
			}
		}
		issues, attestations := validateCaseEvidence(store, evaluationCase, caseRecords,
			caseArtifacts, corpusManifest, allRecords, orderedRecords)
		report.Issues = append(report.Issues, issues...)
		report.AttestedReferences += attestations
	}
	report.Issues = uniqueSorted(report.Issues)
	report.ReleaseReady = len(report.Issues) == 0 && gatesPassed(report.Gates)
	return report
}

func validEvaluationRunEvent(store *ledger.Store, record ledger.Record, input EvaluationInput,
	report EvaluationReport, reportData []byte, allRecords map[string]indexedRecord) bool {
	event := record.Event
	expectedSHA := sha256Hex(reportData)
	if report.InputBlob == nil || event.Kind != ledger.KindEvaluationRun ||
		event.Payload == nil || event.Payload.Blob == nil ||
		event.Payload.SHA256 != expectedSHA || event.Payload.Bytes != int64(len(reportData)) ||
		event.Payload.Blob.SHA256 != expectedSHA || event.Payload.Blob.Bytes != int64(len(reportData)) ||
		verifyBlob(store, *event.Payload.Blob) != nil || event.Source.DeviceID == "" || event.Source.OS == "" ||
		!event.ObservedAt.UTC().Equal(input.CreatedAt.UTC()) ||
		event.Completeness.Status != ledger.CompletenessComplete ||
		event.Privacy.Classification != "local_only" {
		return false
	}
	expectedSource := ledger.Source{
		Agent: ledger.AgentUnknown, Adapter: evaluationAdapterName,
		AdapterVersion: evaluationAdapterVersion, DeviceID: event.Source.DeviceID, OS: event.Source.OS,
		ThreadID: input.SuiteID, SessionID: input.RunID, SourceEventID: input.RunID,
		SourcePathHash: report.InputBlob.SHA256, SourceCursor: "run:" + input.SuiteID + "/" + input.RunID,
	}
	if event.Source != expectedSource {
		return false
	}
	parents := []string{}
	for _, evaluationCase := range input.Cases {
		for _, reference := range evaluationCase.Evidence {
			if reference.Kind == "ledger_event" {
				if _, exists := allRecords[reference.ID]; exists {
					parents = append(parents, reference.ID)
				}
			}
		}
	}
	parents = sortedUniqueStrings(parents)
	if len(parents) == 0 {
		return event.Causality == nil
	}
	return event.Causality != nil && sameStrings(event.Causality.ParentEventIDs, parents) &&
		event.Causality.CallID == "" && event.Causality.CompactionID == ""
}

func validateCaseEvidence(store *ledger.Store, evaluationCase EvaluationCase,
	records map[string]ledger.Record, artifacts map[string]CorpusArtifact,
	manifest *CorpusManifest, allRecords map[string]indexedRecord,
	orderedRecords []ledger.Record) ([]string, int) {
	issues := []string{}
	attestations := 0
	for _, record := range records {
		if record.Event.Kind != ledger.KindEvaluationAttestation {
			continue
		}
		if err := validateAttestationRecord(store, evaluationCase, record); err != nil {
			issues = append(issues, "case "+evaluationCase.CaseID+": "+err.Error())
			continue
		}
		attestations++
	}
	if requiresCaseAttestation(evaluationCase.Category) && attestations != 1 {
		issues = append(issues, "case "+evaluationCase.CaseID+": exactly one matching evaluation attestation is required")
	} else if attestations > 1 {
		issues = append(issues, "case "+evaluationCase.CaseID+": multiple matching evaluation attestations were referenced")
	}
	hasKind := func(kinds ...ledger.EventKind) bool {
		for _, record := range records {
			for _, kind := range kinds {
				if record.Event.Kind == kind {
					return true
				}
			}
		}
		return false
	}
	switch evaluationCase.Category {
	case CategoryCaptureCoverage:
		switch evaluationCase.Capture.Unit {
		case CaptureUnitEvidenceEvents:
			if !hasKind(ledger.KindSourceSnapshot, ledger.KindGap) {
				issues = append(issues, "case "+evaluationCase.CaseID+": capture measurement lacks source snapshot or gap evidence")
				break
			}
			complete, partial, missing := 0, 0, 0
			for _, record := range records {
				if record.Event.Kind != ledger.KindSourceSnapshot && record.Event.Kind != ledger.KindGap {
					continue
				}
				switch record.Event.Completeness.Status {
				case ledger.CompletenessComplete:
					complete++
				case ledger.CompletenessPartial, ledger.CompletenessUnknown:
					partial++
				case ledger.CompletenessMissing:
					missing++
				}
			}
			measurement := evaluationCase.Capture
			if complete != measurement.Complete || partial != measurement.Partial ||
				missing != measurement.Missing || complete+partial+missing != measurement.Expected {
				issues = append(issues, "case "+evaluationCase.CaseID+": capture counts do not match referenced evidence")
			}
		case CaptureUnitLegacyRollouts:
			indexBound := false
			for _, artifact := range artifacts {
				if artifact.Role == RoleLegacyIndex {
					indexBound = true
				}
			}
			if manifest == nil || !indexBound {
				issues = append(issues, "case "+evaluationCase.CaseID+": legacy rollout measurement lacks its verified corpus index")
				break
			}
			measurement := evaluationCase.Capture
			if measurement.Expected != manifest.Counts.RolloutReferences ||
				measurement.Complete != manifest.Counts.CapturedRollouts ||
				measurement.Partial != manifest.Counts.PartialRollouts ||
				measurement.Missing != manifest.Counts.MissingRollouts {
				issues = append(issues, "case "+evaluationCase.CaseID+": legacy rollout counts do not match the corpus manifest")
			}
		}
	case CategoryRepeatedCorrection:
		issues = append(issues, validateCorrectionAttemptEvidence(
			store, evaluationCase, records, allRecords, orderedRecords)...)
	case CategoryCompactionDrift:
		if !hasKind(ledger.KindCompaction) {
			issues = append(issues, "case "+evaluationCase.CaseID+": compaction measurement lacks compaction evidence")
		}
	case CategoryRetrievalCost:
		issues = append(issues, validateRetrievalEvidence(store, evaluationCase, records)...)
	case CategoryPairedOutcome:
		issues = append(issues, validatePairedAttemptEvidence(
			store, evaluationCase, records, allRecords, orderedRecords)...)
	}
	return issues, attestations
}

func requiresCaseAttestation(category CaseCategory) bool {
	return category == CategoryFalseMemory || category == CategoryCompactionDrift
}

func validateCorrectionAttemptEvidence(store *ledger.Store, evaluationCase EvaluationCase,
	caseRecords map[string]ledger.Record, allRecords map[string]indexedRecord,
	orderedRecords []ledger.Record) []string {
	measurement := evaluationCase.Correction
	initialRecord, referenced := caseRecords[measurement.InitialCorrectionAttemptID]
	initialIndexed, indexed := allRecords[measurement.InitialCorrectionAttemptID]
	if !referenced || !indexed || initialRecord.Event.Kind != ledger.KindTaskAttempt {
		return []string{"case " + evaluationCase.CaseID + ": initial correction attempt is unavailable"}
	}
	if verification := verifyTaskAttemptRecord(store, initialRecord, allRecords, orderedRecords); len(verification.Issues) != 0 {
		return []string{"case " + evaluationCase.CaseID + ": initial correction attempt failed replay: " +
			strings.Join(verification.Issues, "; ")}
	}
	initial, err := decodeTaskAttemptReceipt(store, initialRecord.Event)
	if err != nil || initial.Agent != evaluationCase.Agent ||
		initial.SemanticKeySHA256 != measurement.SemanticKeySHA256 ||
		initial.Measurement.UserCorrections == 0 {
		return []string{"case " + evaluationCase.CaseID + ": initial attempt does not establish the claimed correction"}
	}

	repeated := 0
	for _, attemptID := range measurement.AttemptIDs {
		record, referenced := caseRecords[attemptID]
		indexed, exists := allRecords[attemptID]
		if !referenced || !exists || record.Event.Kind != ledger.KindTaskAttempt ||
			indexed.Index <= initialIndexed.Index {
			return []string{"case " + evaluationCase.CaseID + ": correction follow-up is unavailable or not later than the initial correction"}
		}
		verification := verifyTaskAttemptRecord(store, record, allRecords, orderedRecords)
		if len(verification.Issues) != 0 {
			return []string{"case " + evaluationCase.CaseID + ": task attempt " + attemptID + " failed replay: " + strings.Join(verification.Issues, "; ")}
		}
		receipt, err := decodeTaskAttemptReceipt(store, record.Event)
		if err != nil || receipt.Condition != TaskConditionMemory || receipt.Agent != evaluationCase.Agent ||
			receipt.SemanticKeySHA256 != measurement.SemanticKeySHA256 {
			return []string{"case " + evaluationCase.CaseID + ": correction follow-up has the wrong condition, agent, or semantic key"}
		}
		if receipt.Measurement.UserCorrections > 0 {
			repeated++
		}
	}
	if measurement.EligibleFollowupOpportunities != len(measurement.AttemptIDs) ||
		measurement.RepeatedCorrections != repeated ||
		measurement.RepeatedCorrectionsAfterMemory != repeated {
		return []string{"case " + evaluationCase.CaseID + ": correction counts do not match replayed task attempts"}
	}
	return nil
}

func validatePairedAttemptEvidence(store *ledger.Store, evaluationCase EvaluationCase,
	caseRecords map[string]ledger.Record, allRecords map[string]indexedRecord,
	orderedRecords []ledger.Record) []string {
	measurement := evaluationCase.PairedOutcome
	load := func(id string) (TaskAttemptReceipt, error) {
		record, referenced := caseRecords[id]
		if !referenced || record.Event.Kind != ledger.KindTaskAttempt {
			return TaskAttemptReceipt{}, errors.New("paired outcome lacks exact task attempt " + id)
		}
		verification := verifyTaskAttemptRecord(store, record, allRecords, orderedRecords)
		if len(verification.Issues) != 0 {
			return TaskAttemptReceipt{}, errors.New("task attempt " + id + " failed replay: " + strings.Join(verification.Issues, "; "))
		}
		return decodeTaskAttemptReceipt(store, record.Event)
	}
	baseline, err := load(measurement.BaselineAttemptID)
	if err != nil {
		return []string{"case " + evaluationCase.CaseID + ": " + err.Error()}
	}
	treatment, err := load(measurement.TreatmentAttemptID)
	if err != nil {
		return []string{"case " + evaluationCase.CaseID + ": " + err.Error()}
	}
	if !comparableTaskAttempts(baseline, treatment, evaluationCase.Agent) {
		return []string{"case " + evaluationCase.CaseID + ": paired task attempts are not comparable"}
	}
	if baseline.Measurement != measurement.Baseline || treatment.Measurement != measurement.Treatment {
		return []string{"case " + evaluationCase.CaseID + ": paired measurements do not match replayed task attempts"}
	}
	return nil
}

func comparableTaskAttempts(baseline, treatment TaskAttemptReceipt, agent ledger.Agent) bool {
	return baseline.Condition == TaskConditionBaseline && treatment.Condition == TaskConditionMemory &&
		baseline.Agent == agent && treatment.Agent == agent &&
		baseline.TaskID == treatment.TaskID && baseline.TaskSpecSHA256 == treatment.TaskSpecSHA256 &&
		baseline.AcceptanceCriteriaSHA256 == treatment.AcceptanceCriteriaSHA256 &&
		baseline.ExecutionConfigSHA256 == treatment.ExecutionConfigSHA256 &&
		baseline.SemanticKeySHA256 == treatment.SemanticKeySHA256 &&
		baseline.Oracle.Kind == treatment.Oracle.Kind && baseline.Oracle.ID == treatment.Oracle.ID &&
		baseline.Oracle.Version == treatment.Oracle.Version
}

func validateRetrievalEvidence(store *ledger.Store, evaluationCase EvaluationCase,
	records map[string]ledger.Record) []string {
	issues := []string{}
	measurement := evaluationCase.Retrieval
	record, exists := records[measurement.RetrievalID]
	if !exists || record.Event.Kind != ledger.KindRetrieval {
		return []string{"case " + evaluationCase.CaseID + ": retrieval measurement lacks its exact receipt event"}
	}
	payload, err := eventPayload(store, record.Event)
	if err != nil {
		return []string{"case " + evaluationCase.CaseID + ": retrieval receipt payload is invalid"}
	}
	var receipt retrieval.Receipt
	if err := json.Unmarshal(payload, &receipt); err != nil || receipt.ReceiptID != measurement.RetrievalID {
		return []string{"case " + evaluationCase.CaseID + ": retrieval receipt cannot be decoded"}
	}
	if receipt.Result.SelectedEstimatedTokens != measurement.EstimatedTokens ||
		receipt.Result.SelectedBytes != measurement.UTF8Bytes ||
		len(receipt.Result.Selected) != measurement.SelectedItems {
		issues = append(issues, "case "+evaluationCase.CaseID+": retrieval cost does not match the receipt")
	}
	adopted := false
	outcomes := map[OutcomeLabel]struct{}{}
	for _, candidate := range records {
		if candidate.Event.Kind != ledger.KindAdoption {
			continue
		}
		data, payloadErr := eventPayload(store, candidate.Event)
		if payloadErr != nil {
			issues = append(issues, "case "+evaluationCase.CaseID+": adoption receipt payload is invalid")
			continue
		}
		var adoption retrieval.AdoptionReceipt
		if json.Unmarshal(data, &adoption) != nil || adoption.RetrievalReceiptID != measurement.RetrievalID {
			continue
		}
		for _, item := range adoption.Items {
			if item.Adoption != retrieval.AdoptionAdopted {
				continue
			}
			adopted = true
			outcomes[OutcomeLabel(item.Outcome)] = struct{}{}
		}
	}
	if adopted != measurement.Adopted {
		issues = append(issues, "case "+evaluationCase.CaseID+": adoption status does not match referenced receipts")
	}
	if summarizedOutcome(outcomes) != measurement.Outcome {
		issues = append(issues, "case "+evaluationCase.CaseID+": outcome does not match referenced adoption receipts")
	}
	return issues
}

func summarizedOutcome(outcomes map[OutcomeLabel]struct{}) OutcomeLabel {
	for _, outcome := range []OutcomeLabel{OutcomeHarmful, OutcomeHelpful, OutcomeNeutral} {
		if _, exists := outcomes[outcome]; exists {
			return outcome
		}
	}
	return OutcomeUnknown
}

func eventPayload(store *ledger.Store, event ledger.Event) ([]byte, error) {
	if event.Payload == nil {
		return nil, errors.New("event has no payload")
	}
	if event.Payload.Content != nil {
		data := []byte(*event.Payload.Content)
		if sha256Hex(data) != event.Payload.SHA256 || int64(len(data)) != event.Payload.Bytes {
			return nil, errors.New("inline payload hash mismatch")
		}
		return data, nil
	}
	if event.Payload.Blob == nil {
		return nil, errors.New("event payload has no content")
	}
	if err := verifyBlob(store, *event.Payload.Blob); err != nil {
		return nil, err
	}
	file, err := store.OpenBlob(*event.Payload.Blob)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func loadEvaluationPortableRevisions(store *ledger.Store, input EvaluationInput,
	portableRoot string) (map[string]portable.Revision, []string) {
	if input.QualityProfile != QualityProfileContinuousLearning {
		return loadPortableRevisions(store.Root(), portableRoot,
			hasEvidenceKind(input, "portable_revision"))
	}
	if input.Population == nil || input.Population.PortableStateBlob == nil {
		return map[string]portable.Revision{}, []string{"continuous portable state snapshot is missing"}
	}
	revisions, _, err := loadPortablePopulationSnapshot(store, *input.Population.PortableStateBlob)
	if err != nil {
		return map[string]portable.Revision{}, []string{"continuous portable state snapshot: " + err.Error()}
	}
	return revisions, nil
}

func loadPortableRevisions(evidenceRoot, portableRoot string, required bool) (map[string]portable.Revision, []string) {
	result := map[string]portable.Revision{}
	if !required {
		return result, nil
	}
	if strings.TrimSpace(portableRoot) == "" {
		return result, []string{"portable repository is required by evaluation evidence"}
	}
	if err := portable.EnsureSeparateRoots(evidenceRoot, portableRoot); err != nil {
		return result, []string{"portable and evidence roots are not safely separate"}
	}
	report := portable.VerifyRepository(portableRoot)
	if len(report.Issues) != 0 {
		return result, []string{"portable repository verification failed"}
	}
	err := filepath.WalkDir(filepath.Join(portableRoot, "memories"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		revision, err := portable.ParseRevision(data)
		if err != nil {
			return err
		}
		result[revision.RevisionID] = revision
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return map[string]portable.Revision{}, []string{"portable revision index could not be built"}
	}
	return result, nil
}

func writeRunFiles(destination string, inputData, reportData []byte) error {
	if existingInput, inputErr := os.ReadFile(filepath.Join(destination, "input.json")); inputErr == nil {
		existingReport, reportErr := os.ReadFile(filepath.Join(destination, "report.json"))
		if reportErr == nil && bytes.Equal(existingInput, inputData) && bytes.Equal(existingReport, reportData) {
			return nil
		}
		return errors.New("evaluation run id already exists with different content")
	} else if !errors.Is(inputErr, os.ErrNotExist) {
		return inputErr
	}
	base := filepath.Dir(destination)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create evaluation run directory: %w", err)
	}
	temporary, err := os.MkdirTemp(base, ".run-tmp-")
	if err != nil {
		return fmt.Errorf("create evaluation run temp directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	for name, data := range map[string][]byte{"input.json": inputData, "report.json": reportData} {
		path := filepath.Join(temporary, name)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("commit evaluation run: %w", err)
	}
	return nil
}

func evaluationRunPath(root, suiteID, runID string) string {
	return filepath.Join(root, "derived", "evaluations", "runs", suiteID, runID)
}

func evaluationRunEventID(input EvaluationInput, report EvaluationReport) string {
	return adapterjsonl.DeterministicID("evaluation-run", input.SuiteID, input.RunID,
		report.InputSHA256, report.ReportSHA256)
}

func hasCategory(input EvaluationInput, category CaseCategory) bool {
	for _, evaluationCase := range input.Cases {
		if evaluationCase.Category == category {
			return true
		}
	}
	return false
}

func hasEvidenceKind(input EvaluationInput, kind string) bool {
	for _, evaluationCase := range input.Cases {
		if hasReferenceKind(evaluationCase.Evidence, kind) {
			return true
		}
	}
	return false
}

func lockedContinuousLedgerIssues(input EvaluationInput, ordered []ledger.Record) []string {
	if input.QualityProfile != QualityProfileContinuousLearning || input.Population == nil {
		return nil
	}
	prefix := input.Population.LedgerRecordCount
	if prefix < 1 || prefix > len(ordered) {
		return []string{"continuous-learning population is stale relative to the locked ledger snapshot"}
	}
	if ordered[prefix-1].RecordHash != input.Population.LedgerLastRecordHash {
		return []string{"continuous-learning population prefix hash does not match the locked ledger snapshot"}
	}
	for _, record := range ordered[prefix:] {
		if record.Event.Kind != ledger.KindEvaluationRun {
			return []string{"continuous-learning population is stale relative to the locked ledger snapshot"}
		}
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
