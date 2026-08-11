package retrieval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

func Verify(store *ledger.Store) VerificationReport {
	report := VerificationReport{
		SchemaVersion:    VerificationSchemaVersion,
		OpenRetrievalIDs: []string{},
		Issues:           []string{},
		Privacy:          PrivacyLocalOnly,
	}
	if store == nil {
		report.Issues = append(report.Issues, "local evidence store is required")
		return report
	}
	retrievals := map[string]Receipt{}
	injections := map[string]InjectionReceipt{}
	adoptions := map[string]AdoptionReceipt{}
	eventIndexes := map[string]int{}
	events := map[string]ledger.Event{}
	index := 0
	visitErr := store.VisitRecords(func(record ledger.Record) error {
		index++
		event := record.Event
		if _, duplicate := eventIndexes[event.EventID]; duplicate {
			report.Issues = append(report.Issues, fmt.Sprintf("event id %s appears more than once", event.EventID))
			return nil
		}
		eventIndexes[event.EventID] = index
		events[event.EventID] = event
		switch event.Kind {
		case ledger.KindRetrieval:
			report.RetrievalsChecked++
			var receipt Receipt
			if err := decodeReceiptEvent(event, &receipt); err != nil {
				report.Issues = append(report.Issues, fmt.Sprintf("retrieval %s: %v", event.EventID, err))
				return nil
			}
			if issues := validateReceipt(event, receipt); len(issues) != 0 {
				for _, issue := range issues {
					report.Issues = append(report.Issues, fmt.Sprintf("retrieval %s: %s", event.EventID, issue))
				}
				return nil
			}
			retrievals[receipt.ReceiptID] = receipt
		case ledger.KindInjection:
			report.InjectionsChecked++
			var receipt InjectionReceipt
			if err := decodeReceiptEvent(event, &receipt); err != nil {
				report.Issues = append(report.Issues, fmt.Sprintf("injection %s: %v", event.EventID, err))
				return nil
			}
			if issues := validateInjectionReceipt(event, receipt); len(issues) != 0 {
				for _, issue := range issues {
					report.Issues = append(report.Issues, fmt.Sprintf("injection %s: %s", event.EventID, issue))
				}
				return nil
			}
			injections[receipt.InjectionID] = receipt
		case ledger.KindAdoption:
			report.AdoptionsChecked++
			var receipt AdoptionReceipt
			if err := decodeReceiptEvent(event, &receipt); err != nil {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s: %v", event.EventID, err))
				return nil
			}
			if issues := validateAdoptionReceipt(event, receipt); len(issues) != 0 {
				for _, issue := range issues {
					report.Issues = append(report.Issues, fmt.Sprintf("adoption %s: %s", event.EventID, issue))
				}
				return nil
			}
			adoptions[receipt.AdoptionID] = receipt
		}
		return nil
	})
	if visitErr != nil {
		report.Issues = append(report.Issues, fmt.Sprintf("evidence ledger verification failed: %v", visitErr))
		return finalizeVerification(report)
	}

	injectedByRetrieval := map[string]map[string]struct{}{}
	for _, injection := range injections {
		retrieval, exists := retrievals[injection.RetrievalReceiptID]
		if !exists {
			report.Issues = append(report.Issues, fmt.Sprintf("injection %s references an unavailable retrieval", injection.InjectionID))
			continue
		}
		if eventIndexes[injection.InjectionID] <= eventIndexes[retrieval.ReceiptID] {
			report.Issues = append(report.Issues, fmt.Sprintf("injection %s does not follow its retrieval", injection.InjectionID))
		}
		if injection.Channel != retrieval.Request.Context.Channel ||
			!sourceMatchesContext(events[injection.InjectionID], retrieval.Request.Context) {
			report.Issues = append(report.Issues, fmt.Sprintf("injection %s has a context that differs from its retrieval", injection.InjectionID))
		}
		if !causalityMatches(events[injection.InjectionID], []string{retrieval.ReceiptID}) {
			report.Issues = append(report.Issues, fmt.Sprintf("injection %s has invalid causal parents", injection.InjectionID))
		}
		allowed := referencesFromMatches(retrieval.Result.Selected)
		for _, memory := range injection.Memories {
			if _, ok := allowed[referenceKey(memory)]; !ok {
				report.Issues = append(report.Issues, fmt.Sprintf("injection %s contains a memory absent from its retrieval", injection.InjectionID))
			}
		}
		expectedContent, expectedMemories := renderBoundedContext(retrieval.Result, retrieval.Request)
		if injection.Content != expectedContent || !sameReferences(injection.Memories, expectedMemories) {
			report.Issues = append(report.Issues, fmt.Sprintf("injection %s differs from deterministic retrieval output", injection.InjectionID))
		}
		if injectedByRetrieval[injection.RetrievalReceiptID] == nil {
			injectedByRetrieval[injection.RetrievalReceiptID] = map[string]struct{}{}
		}
		for _, memory := range injection.Memories {
			injectedByRetrieval[injection.RetrievalReceiptID][referenceKey(memory)] = struct{}{}
		}
	}

	observedByRetrieval := map[string]map[string]struct{}{}
	for _, adoption := range adoptions {
		retrieval, exists := retrievals[adoption.RetrievalReceiptID]
		if !exists {
			report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references an unavailable retrieval", adoption.AdoptionID))
			continue
		}
		if eventIndexes[adoption.AdoptionID] <= eventIndexes[retrieval.ReceiptID] {
			report.Issues = append(report.Issues, fmt.Sprintf("adoption %s does not follow its retrieval", adoption.AdoptionID))
		}
		if !sourceMatchesContext(events[adoption.AdoptionID], retrieval.Request.Context) {
			report.Issues = append(report.Issues, fmt.Sprintf("adoption %s has a context that differs from its retrieval", adoption.AdoptionID))
		}
		allowed := referencesFromMatches(retrieval.Result.Selected)
		if adoption.InjectionID != "" {
			injection, ok := injections[adoption.InjectionID]
			if !ok || injection.RetrievalReceiptID != adoption.RetrievalReceiptID {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references an unavailable or unrelated injection", adoption.AdoptionID))
				continue
			}
			allowed = referenceSet(injection.Memories)
			if eventIndexes[adoption.AdoptionID] <= eventIndexes[injection.InjectionID] {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s does not follow its injection", adoption.AdoptionID))
			}
		}
		for _, item := range adoption.Items {
			key := referenceKey(item.MemoryReference)
			if _, ok := allowed[key]; !ok {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s contains a memory absent from its delivery", adoption.AdoptionID))
			}
			if observedByRetrieval[adoption.RetrievalReceiptID] == nil {
				observedByRetrieval[adoption.RetrievalReceiptID] = map[string]struct{}{}
			}
			observedByRetrieval[adoption.RetrievalReceiptID][key] = struct{}{}
		}
		for _, evidenceID := range adoption.OutcomeEvidenceEventIDs {
			evidenceEvent, ok := events[evidenceID]
			if !ok {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references unavailable outcome evidence", adoption.AdoptionID))
			} else if eventIndexes[evidenceID] <= eventIndexes[retrieval.ReceiptID] ||
				(adoption.InjectionID != "" && eventIndexes[evidenceID] <= eventIndexes[adoption.InjectionID]) {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references outcome evidence that does not follow memory delivery", adoption.AdoptionID))
			} else if eventIndexes[evidenceID] >= eventIndexes[adoption.AdoptionID] {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references outcome evidence that does not precede it", adoption.AdoptionID))
			} else if !sourceMatchesContext(evidenceEvent, retrieval.Request.Context) {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s references outcome evidence from another task context", adoption.AdoptionID))
			} else if err := verifyOutcomeEvidence(store, evidenceEvent); err != nil {
				report.Issues = append(report.Issues, fmt.Sprintf("adoption %s has invalid outcome evidence: %v", adoption.AdoptionID, err))
			} else {
				causalParent := adoption.InjectionID
				if causalParent == "" {
					causalParent = adoption.RetrievalReceiptID
				}
				if !eventDescendsFrom(events, eventIndexes, evidenceID, causalParent) {
					report.Issues = append(report.Issues, fmt.Sprintf("adoption %s has outcome evidence without memory-delivery causality", adoption.AdoptionID))
				}
			}
		}
		expectedParents := append([]string{adoption.RetrievalReceiptID}, adoption.OutcomeEvidenceEventIDs...)
		if adoption.InjectionID != "" {
			expectedParents = append(expectedParents, adoption.InjectionID)
		}
		if !causalityMatches(events[adoption.AdoptionID], sortedUnique(expectedParents)) {
			report.Issues = append(report.Issues, fmt.Sprintf("adoption %s has invalid causal parents", adoption.AdoptionID))
		}
	}

	for retrievalID, retrieval := range retrievals {
		expected := injectedByRetrieval[retrievalID]
		if len(expected) == 0 {
			expected = referencesFromMatches(retrieval.Result.Selected)
		}
		observed := observedByRetrieval[retrievalID]
		open := false
		for key := range expected {
			if _, exists := observed[key]; !exists {
				open = true
				break
			}
		}
		if open {
			report.OpenRetrievalIDs = append(report.OpenRetrievalIDs, retrievalID)
		}
	}
	return finalizeVerification(report)
}

func decodeReceiptEvent(event ledger.Event, target any) error {
	if event.Payload == nil || event.Payload.Content == nil ||
		event.Payload.Encoding != "json" || event.Payload.MediaType != "application/json" ||
		event.Completeness.Status != ledger.CompletenessComplete {
		return fmt.Errorf("receipt is not complete inline JSON")
	}
	if event.Source.Adapter != "agentmem-retrieval" || event.Source.AdapterVersion != AdapterVersion {
		return fmt.Errorf("receipt has an invalid source adapter")
	}
	return decodeStrictJSON([]byte(*event.Payload.Content), target)
}

func validateReceipt(event ledger.Event, receipt Receipt) []string {
	var issues []string
	if receipt.SchemaVersion != ReceiptSchemaVersion || receipt.ReceiptID != event.EventID ||
		receipt.Result.ReceiptID != event.EventID || receipt.Privacy != PrivacyLocalOnly {
		issues = append(issues, "receipt envelope is invalid")
	}
	if !receipt.RecordedAt.Equal(event.RecordedAt) || !event.ObservedAt.Equal(event.RecordedAt) {
		issues = append(issues, "receipt timestamps do not match the evidence event")
	}
	if !sourceMatchesContext(event, receipt.Request.Context) || !causalityMatches(event, nil) {
		issues = append(issues, "retrieval event context or causality is invalid")
	}
	if receipt.PortableRoot == "" || receipt.PortableRootSHA256 != digestString(receipt.PortableRoot) ||
		event.Source.SourcePathHash != receipt.PortableRootSHA256 {
		issues = append(issues, "portable repository identity is invalid")
	}
	normalized, err := normalizeRequest(receipt.Request)
	if err != nil || !reflect.DeepEqual(normalized, receipt.Request) {
		issues = append(issues, "normalized retrieval request is invalid")
	}
	requestBytes, _ := json.Marshal(receipt.Request)
	if receipt.RequestSHA256 != digestBytes(requestBytes) {
		issues = append(issues, "retrieval request hash is invalid")
	}
	result := receipt.Result
	if result.SchemaVersion != ResultSchemaVersion || result.Privacy != PrivacyLocalOnly ||
		result.SelectorSHA256 != selectorDigest(receipt.Request) ||
		result.TokenBudget != receipt.Request.TokenBudget || result.ByteBudget != receipt.Request.ByteBudget ||
		result.Selected == nil || result.RepositoryIssues == nil || result.ContextGaps == nil ||
		!isSortedUnique(result.RepositoryIssues) ||
		!reflect.DeepEqual(result.ContextGaps, contextGaps(receipt.Request.Context)) {
		issues = append(issues, "retrieval result envelope is invalid")
	}
	if result.ActiveMemories < 0 || result.ApplicableMemories < 0 ||
		result.ApplicableMemories > result.ActiveMemories || !validExclusionCounts(result.Exclusions) {
		issues = append(issues, "retrieval result counts are invalid")
	}
	if result.Status == "completed" {
		if len(result.RepositoryIssues) != 0 || !validDigest(result.RepositoryStateSHA256) {
			issues = append(issues, "completed retrieval has invalid repository state")
		}
		if result.Exclusions.ScopeMismatch+result.ApplicableMemories != result.ActiveMemories {
			issues = append(issues, "completed retrieval has invalid scope accounting")
		}
	} else if result.Status == "blocked" {
		if len(result.RepositoryIssues) == 0 || result.RepositoryStateSHA256 != "" || len(result.Selected) != 0 {
			issues = append(issues, "blocked retrieval has invalid repository state")
		}
	} else {
		issues = append(issues, "retrieval result has an invalid status")
	}
	seen := map[string]struct{}{}
	tokens, bytes := 0, 0
	for index, match := range result.Selected {
		key := referenceKey(match.MemoryReference)
		if !validMemoryReference(match.MemoryReference) || match.TextSHA256 != digestString(match.Text) ||
			strings.TrimSpace(match.Text) == "" || match.Bytes != len([]byte(match.Text)) ||
			match.EstimatedTokens != estimateTokens(match.Text) || !validMatchKind(match.Kind) ||
			!validEvidenceBasis(match.EvidenceBasis) ||
			match.MatchedTerms == nil || match.SelectionRationale == nil ||
			!scopeValueApplies(match.ScopeKind, match.ScopeValue, receipt.Request.Context) ||
			match.ScopeSpecificity != scopeSpecificity(match.ScopeKind) ||
			!validMatchSelection(match, receipt.Request) {
			issues = append(issues, "retrieval result contains an invalid memory match")
		}
		if index > 0 && !matchPrecedes(result.Selected[index-1], match) {
			issues = append(issues, "retrieval result ordering is invalid")
		}
		if _, duplicate := seen[key]; duplicate {
			issues = append(issues, "retrieval result repeats a memory match")
		}
		seen[key] = struct{}{}
		tokens += match.EstimatedTokens
		bytes += match.Bytes
	}
	if tokens != result.SelectedEstimatedTokens || bytes != result.SelectedBytes ||
		tokens > result.TokenBudget || bytes > result.ByteBudget || len(result.Selected) > receipt.Request.Limit {
		issues = append(issues, "retrieval result budget accounting is invalid")
	}
	if result.Status == "completed" && receipt.Request.Query != "" &&
		result.ApplicableMemories != result.Exclusions.NoLexicalMatch+len(result.Selected)+
			result.Exclusions.ResultLimit+result.Exclusions.TokenBudget+result.Exclusions.ByteBudget {
		issues = append(issues, "retrieval result exclusion accounting is invalid")
	}
	return sortedUnique(issues)
}

func validExclusionCounts(counts ExclusionCounts) bool {
	return counts.ScopeMismatch >= 0 && counts.NoLexicalMatch >= 0 &&
		counts.ResultLimit >= 0 && counts.TokenBudget >= 0 && counts.ByteBudget >= 0
}

func validMatchKind(kind candidates.CandidateKind) bool {
	return kind == candidates.KindConstraint || kind == candidates.KindCorrection ||
		kind == candidates.KindDirective
}

func validEvidenceBasis(values []review.Basis) bool {
	if len(values) == 0 {
		return false
	}
	for index, value := range values {
		switch value {
		case review.BasisExplicitRemember, review.BasisUserCorrection,
			review.BasisStableRepetition, review.BasisOutcomeEvidence,
			review.BasisExplicitUserConfirmation:
		default:
			return false
		}
		if index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validMatchSelection(match Match, request Request) bool {
	if match.Score <= 0 {
		return false
	}
	scopeReason := "scope_" + string(match.ScopeKind)
	if request.MemoryID != "" {
		return match.MemoryID == request.MemoryID && match.Score == 1 &&
			len(match.MatchedTerms) == 0 && reflect.DeepEqual(
			match.SelectionRationale, []string{"exact_memory_id", scopeReason})
	}
	if !isSortedUnique(match.MatchedTerms) {
		return false
	}
	queryTerms := frequencies(lexicalTerms(request.Query))
	memoryTerms := frequencies(lexicalTerms(match.Text))
	for _, term := range match.MatchedTerms {
		if queryTerms[term] == 0 || memoryTerms[term] == 0 {
			return false
		}
	}
	expectedRationale := []string{"lexical_match", scopeReason}
	if normalizedQuery := normalizePhrase(request.Query); normalizedQuery != "" &&
		strings.Contains(normalizePhrase(match.Text), normalizedQuery) {
		expectedRationale = append(expectedRationale, "exact_phrase")
	}
	return reflect.DeepEqual(match.SelectionRationale, expectedRationale)
}

func matchPrecedes(left, right Match) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.ScopeSpecificity != right.ScopeSpecificity {
		return left.ScopeSpecificity > right.ScopeSpecificity
	}
	return left.MemoryID < right.MemoryID
}

func validateInjectionReceipt(event ledger.Event, receipt InjectionReceipt) []string {
	var issues []string
	if receipt.SchemaVersion != InjectionReceiptSchemaVersion || receipt.InjectionID != event.EventID ||
		!strings.HasPrefix(receipt.RetrievalReceiptID, "retrieval-") ||
		receipt.Privacy != PrivacyLocalOnly || !validChannel(receipt.Channel) {
		issues = append(issues, "injection receipt envelope is invalid")
	}
	if !receipt.RecordedAt.Equal(event.RecordedAt) || !event.ObservedAt.Equal(event.RecordedAt) {
		issues = append(issues, "injection timestamps do not match the evidence event")
	}
	if receipt.Content == "" || receipt.ContentSHA256 != digestString(receipt.Content) ||
		receipt.ContentBytes != len([]byte(receipt.Content)) ||
		receipt.EstimatedTokens != estimateTokens(receipt.Content) {
		issues = append(issues, "injected content integrity is invalid")
	}
	seen := map[string]struct{}{}
	for _, memory := range receipt.Memories {
		key := referenceKey(memory)
		if !validMemoryReference(memory) {
			issues = append(issues, "injection contains an invalid memory reference")
		}
		if _, duplicate := seen[key]; duplicate {
			issues = append(issues, "injection repeats a memory reference")
		}
		seen[key] = struct{}{}
	}
	if len(receipt.Memories) == 0 || receipt.Memories == nil {
		issues = append(issues, "injection contains no memories")
	}
	return sortedUnique(issues)
}

func validateAdoptionReceipt(event ledger.Event, receipt AdoptionReceipt) []string {
	var issues []string
	if receipt.SchemaVersion != AdoptionReceiptSchemaVersion || receipt.AdoptionID != event.EventID ||
		receipt.Privacy != PrivacyLocalOnly {
		issues = append(issues, "adoption receipt envelope is invalid")
	}
	if !receipt.RecordedAt.Equal(event.RecordedAt) || !event.ObservedAt.Equal(event.RecordedAt) {
		issues = append(issues, "adoption timestamps do not match the evidence event")
	}
	request := AdoptionRequest{
		SchemaVersion:           AdoptionRequestSchemaVersion,
		Reporter:                receipt.Reporter,
		RetrievalReceiptID:      receipt.RetrievalReceiptID,
		InjectionID:             receipt.InjectionID,
		Items:                   receipt.Items,
		OutcomeEvidenceEventIDs: receipt.OutcomeEvidenceEventIDs,
	}
	if err := validateAdoptionRequest(request); err != nil {
		issues = append(issues, "adoption request projection is invalid")
	}
	requestBytes, _ := json.Marshal(request)
	if receipt.RequestSHA256 != digestBytes(requestBytes) {
		issues = append(issues, "adoption request hash is invalid")
	}
	return sortedUnique(issues)
}

func validDigest(value string) bool {
	return validPrefixedHash("digest-"+value, "digest-")
}

func sourceMatchesContext(event ledger.Event, context Context) bool {
	return event.Source.Agent == context.Agent &&
		event.Source.ThreadID == threadIDForContext(context) &&
		event.Source.SessionID == context.SessionID
}

func causalityMatches(event ledger.Event, expected []string) bool {
	if len(expected) == 0 {
		return event.Causality == nil
	}
	if event.Causality == nil {
		return false
	}
	actual := append([]string(nil), event.Causality.ParentEventIDs...)
	expected = append([]string(nil), expected...)
	sort.Strings(actual)
	sort.Strings(expected)
	return reflect.DeepEqual(actual, expected)
}

func finalizeVerification(report VerificationReport) VerificationReport {
	sort.Strings(report.OpenRetrievalIDs)
	report.OpenRetrievalIDs = sortedUnique(report.OpenRetrievalIDs)
	sort.Strings(report.Issues)
	report.Issues = sortedUnique(report.Issues)
	return report
}
