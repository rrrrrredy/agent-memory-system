package evaluation

import (
	"github.com/rrrrrredy/agent-memory-system/internal/episodes"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
)

func buildCompactionPopulation(store *ledger.Store, prefixCount int, lastRecordHash string,
	ordered []ledger.Record, groundTruth map[string]CompactionGroundTruthSubject,
	groundTruthRecord *indexedRecord) (
	[]EvaluationCase, episodes.BuildResult, bool, []string,
) {
	issues := []string{}
	attempts, attemptErr := episodes.ListVerifiedGenerationAttempts(store)
	if attemptErr != nil {
		issues = append(issues, "append-only compaction detector attempt history is unavailable: "+attemptErr.Error())
		return nil, episodes.BuildResult{}, false, uniqueSorted(issues)
	}
	for _, attempted := range attempts {
		if groundTruthRecord != nil && attempted.LedgerIndex < groundTruthRecord.Index {
			for recordIndex, record := range ordered {
				if recordIndex+1 > attempted.Attempt.SourceRecords {
					break
				}
				if _, subject := groundTruth[record.Event.EventID]; subject {
					issues = append(issues, "compaction ground truth was sealed after detector generation was attempted over the frozen subjects")
					break
				}
			}
		}
	}
	audits, auditErr := episodes.ListVerifiedGenerationAudits(store)
	if auditErr != nil {
		issues = append(issues, "append-only compaction detector history is unavailable: "+auditErr.Error())
		return nil, episodes.BuildResult{}, false, uniqueSorted(issues)
	}
	var current *episodes.VerifiedGenerationAudit
	for index := range audits {
		audited := &audits[index]
		if groundTruthRecord != nil && audited.LedgerIndex < groundTruthRecord.Index {
			for recordIndex, record := range ordered {
				if recordIndex+1 > audited.Audit.SourceRecords {
					break
				}
				if _, subject := groundTruth[record.Event.EventID]; subject {
					issues = append(issues, "compaction ground truth was sealed after detector output already covered the frozen subjects")
					break
				}
			}
		}
		if audited.LedgerIndex == prefixCount && audited.Record.RecordHash == lastRecordHash {
			current = audited
		}
	}
	if current == nil {
		issues = append(issues, "latest ledger prefix has no append-only episode generation audit")
		return nil, episodes.BuildResult{}, false, uniqueSorted(issues)
	}
	derived, episodeSet, err := episodes.LoadVerifiedGeneration(store,
		current.Audit.SourceRecords, current.Audit.SourceLastRecordHash)
	if err != nil {
		issues = append(issues, "independent compaction detector unavailable: "+err.Error())
		return nil, derived, false, uniqueSorted(issues)
	}
	if groundTruthRecord == nil || groundTruthRecord.Index > derived.SourceRecords {
		issues = append(issues, "compaction detector generation is not bound after the sealed ground truth")
		return nil, derived, false, uniqueSorted(issues)
	}
	byEventID := map[string]episodes.CompactionCheckpoint{}
	for _, episode := range episodeSet {
		for _, checkpoint := range episode.Compactions {
			for _, eventID := range checkpoint.EventIDs {
				if _, duplicate := byEventID[eventID]; duplicate {
					issues = append(issues, "compaction detector repeats source event: "+eventID)
					continue
				}
				byEventID[eventID] = checkpoint
			}
		}
	}
	cases := []EvaluationCase{}
	for _, record := range ordered {
		truth, truthExists := groundTruth[record.Event.EventID]
		if !truthExists {
			continue
		}
		if record.Event.Kind != ledger.KindCompaction {
			issues = append(issues, "sealed compaction ground truth references a non-compaction event: "+record.Event.EventID)
			continue
		}
		caseID := populationCaseID(CategoryCompactionDrift, record.Event.EventID)
		checkpoint, detected := byEventID[record.Event.EventID]
		if !detected {
			issues = append(issues, "compaction event has no independent detector checkpoint: "+record.Event.EventID)
			continue
		}
		measurement := &CompactionMeasurement{CheckpointID: checkpoint.CheckpointID,
			Expected: truth.Expected, Observed: detectorDriftLabel(checkpoint.Status)}
		evidence := []EvidenceReference{ledgerEvidence(record), ledgerEvidence(groundTruthRecord.Record)}
		cases = append(cases, EvaluationCase{CaseID: caseID, Category: CategoryCompactionDrift,
			Agent: record.Event.Source.Agent, Evidence: evidence, Compaction: measurement})
	}
	return cases, derived, true, uniqueSorted(issues)
}

func detectorDriftLabel(status episodes.ContinuityStatus) DriftLabel {
	switch status {
	case episodes.ContinuityPreserved:
		return DriftPreserved
	case episodes.ContinuityDriftEvidence:
		return DriftDetected
	default:
		return DriftInsufficientEvidence
	}
}
