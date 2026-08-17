package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rrrrrredy/agent-memory-system/internal/candidates"
	"github.com/rrrrrredy/agent-memory-system/internal/ledger"
	"github.com/rrrrrredy/agent-memory-system/internal/portable"
	"github.com/rrrrrredy/agent-memory-system/internal/review"
)

var ErrPortableRepositoryRejected = errors.New("portable memory repository is not eligible for retrieval")

var ignoredTerms = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"by": {}, "for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {},
	"on": {}, "or": {}, "that": {}, "the": {}, "this": {}, "to": {}, "with": {},
	"一个": {}, "以及": {}, "可以": {}, "应该": {}, "这个": {}, "需要": {},
}

type rankedRevision struct {
	revision           portable.Revision
	score              int
	matchedTerms       []string
	scopeSpecificity   int
	selectionRationale []string
}

func Search(store *ledger.Store, portableRoot string, request Request) (Result, error) {
	if store == nil {
		return Result{}, errors.New("local evidence store is required")
	}
	normalized, err := normalizeRequest(request)
	if err != nil {
		return Result{}, err
	}
	absoluteRoot, err := filepath.Abs(portableRoot)
	if err != nil || strings.TrimSpace(portableRoot) == "" {
		return Result{}, errors.New("portable memory repository root is required")
	}
	now := time.Now().UTC()
	randomID, err := ledger.NewEventID(now)
	if err != nil {
		return Result{}, err
	}
	receiptID := "retrieval-" + randomID
	result := Result{
		SchemaVersion:    ResultSchemaVersion,
		ReceiptID:        receiptID,
		Status:           "completed",
		SelectorSHA256:   selectorDigest(normalized),
		RepositoryIssues: []string{},
		Selected:         []Match{},
		TokenBudget:      normalized.TokenBudget,
		ByteBudget:       normalized.ByteBudget,
		ContextGaps:      contextGaps(normalized.Context),
		Privacy:          PrivacyLocalOnly,
	}

	if err := portable.EnsureSeparateRoots(store.Root(), absoluteRoot); err != nil {
		result.Status = "blocked"
		issue := "storage_roots_unresolved"
		if errors.Is(err, portable.ErrStorageRootsOverlap) {
			issue = "storage_roots_not_separate"
		}
		result.RepositoryIssues = []string{issue}
	} else {
		revisions, report := portable.LoadActiveRevisions(absoluteRoot)
		result.ActiveMemories = report.ActiveMemories
		if len(report.Issues) != 0 {
			result.Status = "blocked"
			for _, issue := range report.Issues {
				result.RepositoryIssues = append(result.RepositoryIssues, issue.Code)
			}
			result.RepositoryIssues = sortedUnique(result.RepositoryIssues)
		} else {
			result.RepositoryStateSHA256 = repositoryStateDigest(revisions)
			rankRevisions(revisions, normalized, &result)
		}
	}

	requestBytes, err := json.Marshal(normalized)
	if err != nil {
		return Result{}, fmt.Errorf("encode retrieval request: %w", err)
	}
	receipt := Receipt{
		SchemaVersion:      ReceiptSchemaVersion,
		ReceiptID:          receiptID,
		RecordedAt:         now,
		PortableRoot:       absoluteRoot,
		PortableRootSHA256: digestString(absoluteRoot),
		Request:            normalized,
		RequestSHA256:      digestBytes(requestBytes),
		Result:             result,
		Privacy:            PrivacyLocalOnly,
	}
	if err := appendReceiptEvent(store, receipt, normalized.Context); err != nil {
		return Result{}, err
	}
	if result.Status == "blocked" {
		return result, ErrPortableRepositoryRejected
	}
	return result, nil
}

func normalizeRequest(request Request) (Request, error) {
	if request.SchemaVersion == "" {
		request.SchemaVersion = RequestSchemaVersion
	}
	if request.SchemaVersion != RequestSchemaVersion {
		return Request{}, fmt.Errorf("unsupported retrieval request schema %q", request.SchemaVersion)
	}
	hasQuery := strings.TrimSpace(request.Query) != ""
	hasMemoryID := strings.TrimSpace(request.MemoryID) != ""
	if hasQuery == hasMemoryID {
		return Request{}, errors.New("retrieval request requires exactly one query or memory id")
	}
	if hasQuery {
		if len([]byte(request.Query)) > MaximumQueryBytes {
			return Request{}, errors.New("retrieval query exceeds the maximum byte size")
		}
		if len(lexicalTerms(request.Query)) == 0 {
			return Request{}, errors.New("retrieval query has no searchable terms")
		}
	} else if !validPrefixedHash(request.MemoryID, "memory-") {
		return Request{}, errors.New("retrieval request has an invalid memory id")
	}
	if !validAgent(request.Context.Agent) {
		return Request{}, errors.New("retrieval context has an invalid agent")
	}
	if request.Context.Channel == "" {
		request.Context.Channel = ChannelCLI
	}
	if !validChannel(request.Context.Channel) {
		return Request{}, errors.New("retrieval context has an invalid delivery channel")
	}
	if request.Limit == 0 {
		request.Limit = DefaultLimit
	}
	if request.Limit < 1 || request.Limit > MaximumLimit {
		return Request{}, fmt.Errorf("retrieval limit must be between 1 and %d", MaximumLimit)
	}
	if request.TokenBudget == 0 {
		request.TokenBudget = DefaultTokenBudget
	}
	if request.TokenBudget < 1 || request.TokenBudget > MaximumTokenBudget {
		return Request{}, fmt.Errorf("retrieval token budget must be between 1 and %d", MaximumTokenBudget)
	}
	if request.ByteBudget == 0 {
		request.ByteBudget = DefaultByteBudget
	}
	if request.ByteBudget < 1 || request.ByteBudget > MaximumByteBudget {
		return Request{}, fmt.Errorf("retrieval byte budget must be between 1 and %d", MaximumByteBudget)
	}
	return request, nil
}

func rankRevisions(revisions []portable.Revision, request Request, result *Result) {
	applicable := make([]portable.Revision, 0, len(revisions))
	for _, revision := range revisions {
		if scopeApplies(revision, request.Context) {
			applicable = append(applicable, revision)
		} else {
			result.Exclusions.ScopeMismatch++
		}
	}
	result.ApplicableMemories = len(applicable)
	if request.MemoryID != "" {
		rankExactMemory(applicable, request, result)
		return
	}
	queryTerms := lexicalTerms(request.Query)
	queryFrequency := frequencies(queryTerms)
	documentFrequencies := map[string]int{}
	documentTerms := make(map[string]map[string]int, len(applicable))
	for _, revision := range applicable {
		frequency := frequencies(lexicalTerms(revision.Text))
		documentTerms[revision.RevisionID] = frequency
		for term := range queryFrequency {
			if frequency[term] > 0 {
				documentFrequencies[term]++
			}
		}
	}

	ranked := make([]rankedRevision, 0, len(applicable))
	for _, revision := range applicable {
		frequency := documentTerms[revision.RevisionID]
		matched := make([]string, 0, len(queryFrequency))
		score := 0
		for term, queryCount := range queryFrequency {
			tf := frequency[term]
			if tf == 0 {
				continue
			}
			matched = append(matched, term)
			if tf > 3 {
				tf = 3
			}
			if queryCount > 2 {
				queryCount = 2
			}
			idf := 1000 + (len(applicable)-documentFrequencies[term])*1000/(documentFrequencies[term]+1)
			score += idf * tf * queryCount
		}
		if len(matched) == 0 {
			result.Exclusions.NoLexicalMatch++
			continue
		}
		sort.Strings(matched)
		rationale := []string{"lexical_match", "scope_" + string(revision.ScopeKind)}
		if normalizedQuery := normalizePhrase(request.Query); normalizedQuery != "" &&
			strings.Contains(normalizePhrase(revision.Text), normalizedQuery) {
			score += 8000
			rationale = append(rationale, "exact_phrase")
		}
		specificity := scopeSpecificity(revision.ScopeKind)
		score += specificity * 250
		switch revision.Kind {
		case candidates.KindCorrection:
			score += 300
		case candidates.KindConstraint:
			score += 200
		case candidates.KindDirective:
			score += 100
		}
		for _, basis := range revision.EvidenceBasis {
			switch basis {
			case review.BasisUserCorrection, review.BasisOutcomeEvidence:
				score += 50
			}
		}
		ranked = append(ranked, rankedRevision{
			revision:           revision,
			score:              score,
			matchedTerms:       matched,
			scopeSpecificity:   specificity,
			selectionRationale: rationale,
		})
	}
	sort.Slice(ranked, func(left, right int) bool {
		a, b := ranked[left], ranked[right]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.scopeSpecificity != b.scopeSpecificity {
			return a.scopeSpecificity > b.scopeSpecificity
		}
		return a.revision.MemoryID < b.revision.MemoryID
	})

	for _, rankedRevision := range ranked {
		if len(result.Selected) >= request.Limit {
			result.Exclusions.ResultLimit++
			continue
		}
		bytes := len([]byte(rankedRevision.revision.Text))
		tokens := estimateTokens(rankedRevision.revision.Text)
		if result.SelectedBytes+bytes > request.ByteBudget {
			result.Exclusions.ByteBudget++
			continue
		}
		if result.SelectedEstimatedTokens+tokens > request.TokenBudget {
			result.Exclusions.TokenBudget++
			continue
		}
		result.Selected = append(result.Selected, Match{
			MemoryReference: MemoryReference{
				MemoryID:   rankedRevision.revision.MemoryID,
				RevisionID: rankedRevision.revision.RevisionID,
			},
			Kind:               rankedRevision.revision.Kind,
			ScopeKind:          rankedRevision.revision.ScopeKind,
			ScopeValue:         rankedRevision.revision.ScopeValue,
			EvidenceBasis:      append([]review.Basis(nil), rankedRevision.revision.EvidenceBasis...),
			Text:               rankedRevision.revision.Text,
			TextSHA256:         rankedRevision.revision.TextSHA256,
			Score:              rankedRevision.score,
			MatchedTerms:       rankedRevision.matchedTerms,
			EstimatedTokens:    tokens,
			Bytes:              bytes,
			ScopeSpecificity:   rankedRevision.scopeSpecificity,
			SelectionRationale: rankedRevision.selectionRationale,
		})
		result.SelectedBytes += bytes
		result.SelectedEstimatedTokens += tokens
	}
}

func rankExactMemory(revisions []portable.Revision, request Request, result *Result) {
	for _, revision := range revisions {
		if revision.MemoryID != request.MemoryID {
			continue
		}
		bytes := len([]byte(revision.Text))
		tokens := estimateTokens(revision.Text)
		if bytes > request.ByteBudget {
			result.Exclusions.ByteBudget++
			return
		}
		if tokens > request.TokenBudget {
			result.Exclusions.TokenBudget++
			return
		}
		result.Selected = append(result.Selected, Match{
			MemoryReference:    MemoryReference{MemoryID: revision.MemoryID, RevisionID: revision.RevisionID},
			Kind:               revision.Kind,
			ScopeKind:          revision.ScopeKind,
			ScopeValue:         revision.ScopeValue,
			EvidenceBasis:      append([]review.Basis(nil), revision.EvidenceBasis...),
			Text:               revision.Text,
			TextSHA256:         revision.TextSHA256,
			Score:              1,
			MatchedTerms:       []string{},
			EstimatedTokens:    tokens,
			Bytes:              bytes,
			ScopeSpecificity:   scopeSpecificity(revision.ScopeKind),
			SelectionRationale: []string{"exact_memory_id", "scope_" + string(revision.ScopeKind)},
		})
		result.SelectedBytes = bytes
		result.SelectedEstimatedTokens = tokens
		return
	}
}

func appendReceiptEvent(store *ledger.Store, receipt Receipt, context Context) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode retrieval receipt: %w", err)
	}
	payload := ledger.InlinePayload("json", "application/json", string(data))
	event := ledger.Event{
		SchemaVersion: ledger.SchemaVersion,
		EventID:       receipt.ReceiptID,
		Kind:          ledger.KindRetrieval,
		ObservedAt:    receipt.RecordedAt,
		RecordedAt:    receipt.RecordedAt,
		Source: ledger.Source{
			Agent:          context.Agent,
			Adapter:        "agentmem-retrieval",
			AdapterVersion: AdapterVersion,
			DeviceID:       store.DeviceID(),
			ThreadID:       threadIDForContext(context),
			SessionID:      context.SessionID,
			SourcePathHash: receipt.PortableRootSHA256,
		},
		Payload:      &payload,
		Completeness: ledger.Completeness{Status: ledger.CompletenessComplete},
		Privacy:      ledger.Privacy{Classification: PrivacyLocalOnly},
	}
	if err := appendEvidenceWithRetry(store, event); err != nil {
		return fmt.Errorf("append retrieval receipt: %w", err)
	}
	return nil
}

func threadIDForContext(context Context) string {
	if strings.TrimSpace(context.ThreadID) == "" {
		return "standalone-retrieval"
	}
	return context.ThreadID
}

func appendEvidenceWithRetry(store *ledger.Store, event ledger.Event) error {
	delay := 5 * time.Millisecond
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		_, err := store.Append(event)
		if err == nil {
			return nil
		}
		last = err
		if !errors.Is(err, ledger.ErrWriterLocked) || attempt == 5 {
			break
		}
		time.Sleep(delay)
		delay *= 2
	}
	return last
}

func scopeApplies(revision portable.Revision, context Context) bool {
	return scopeValueApplies(revision.ScopeKind, revision.ScopeValue, context)
}

func scopeValueApplies(kind review.ScopeKind, value string, context Context) bool {
	switch kind {
	case review.ScopeGlobal:
		return value == "*"
	case review.ScopeAgent:
		return value == string(context.Agent)
	case review.ScopeRepository:
		return context.Repository != "" && value == context.Repository
	case review.ScopeProject:
		return context.Project != "" && value == context.Project
	case review.ScopeTask:
		return context.Task != "" && value == context.Task
	default:
		return false
	}
}

func scopeSpecificity(kind review.ScopeKind) int {
	switch kind {
	case review.ScopeTask:
		return 5
	case review.ScopeProject:
		return 4
	case review.ScopeRepository:
		return 3
	case review.ScopeAgent:
		return 2
	case review.ScopeGlobal:
		return 1
	default:
		return 0
	}
}

func repositoryStateDigest(revisions []portable.Revision) string {
	identities := make([]string, 0, len(revisions)+1)
	identities = append(identities, portable.RepositorySchemaVersion)
	for _, revision := range revisions {
		identities = append(identities, revision.MemoryID+"\x00"+revision.RevisionID)
	}
	return digestString(strings.Join(identities, "\n"))
}

func selectorDigest(request Request) string {
	if request.MemoryID != "" {
		return digestString("memory_id\x00" + request.MemoryID)
	}
	return digestString("query\x00" + request.Query)
}

func lexicalTerms(value string) []string {
	var terms []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		term := string(word)
		if !ignoredTerm(term) {
			terms = append(terms, term)
		}
		word = word[:0]
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		if len(cjk) == 1 {
			term := string(cjk)
			if !ignoredTerm(term) {
				terms = append(terms, term)
			}
		} else {
			for index := 0; index+1 < len(cjk); index++ {
				term := string(cjk[index : index+2])
				if !ignoredTerm(term) {
					terms = append(terms, term)
				}
			}
		}
		cjk = cjk[:0]
	}
	for _, char := range []rune(strings.ToLower(value)) {
		if isCJK(char) {
			flushWord()
			cjk = append(cjk, char)
			continue
		}
		flushCJK()
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			word = append(word, char)
		} else {
			flushWord()
		}
	}
	flushWord()
	flushCJK()
	return terms
}

func isCJK(char rune) bool {
	return unicode.In(char, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

func ignoredTerm(term string) bool {
	_, ignored := ignoredTerms[term]
	return ignored
}

func frequencies(terms []string) map[string]int {
	result := make(map[string]int, len(terms))
	for _, term := range terms {
		result[term]++
	}
	return result
}

func normalizePhrase(value string) string {
	var builder strings.Builder
	space := false
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			space = false
		} else if !space && builder.Len() > 0 {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func estimateTokens(value string) int {
	count := 0
	asciiRun := 0
	flushASCII := func() {
		if asciiRun > 0 {
			count += (asciiRun + 3) / 4
			asciiRun = 0
		}
	}
	for _, char := range value {
		if char <= unicode.MaxASCII && (unicode.IsLetter(char) || unicode.IsDigit(char)) {
			asciiRun++
			continue
		}
		flushASCII()
		if unicode.IsSpace(char) {
			continue
		}
		if char > unicode.MaxASCII {
			count += 2
		} else {
			count++
		}
	}
	flushASCII()
	if count == 0 && value != "" {
		return 1
	}
	return count
}

func contextGaps(context Context) []string {
	gaps := []string{}
	if strings.TrimSpace(context.ThreadID) == "" {
		gaps = append(gaps, "thread_id_unavailable")
	}
	if strings.TrimSpace(context.SessionID) == "" {
		gaps = append(gaps, "session_id_unavailable")
	}
	return gaps
}

func validAgent(agent ledger.Agent) bool {
	return agent == ledger.AgentCodex || agent == ledger.AgentClaudeCode ||
		agent == ledger.AgentOpenCode || agent == ledger.AgentDeepSeekHarness ||
		agent == ledger.AgentUnknown
}

func validChannel(channel DeliveryChannel) bool {
	switch channel {
	case ChannelCLI, ChannelMCP, ChannelCodexHook, ChannelClaudeHook,
		ChannelOpenCodePlugin, ChannelHarness:
		return true
	default:
		return false
	}
}

func digestString(value string) string {
	return digestBytes([]byte(value))
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sortedUnique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value != "" && (len(result) == 0 || result[len(result)-1] != value) {
			result = append(result, value)
		}
	}
	return result
}
