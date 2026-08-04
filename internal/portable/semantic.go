package portable

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
)

type semanticPolarity string

const (
	polarityAffirmative semanticPolarity = "affirmative"
	polarityProhibitive semanticPolarity = "prohibitive"
)

type semanticHead struct {
	memoryID   string
	revisionID string
	polarity   semanticPolarity
}

func validateSemanticHeads(report *VerificationReport, heads map[string]Revision) {
	groups := map[string][]semanticHead{}
	for memoryID, revision := range heads {
		if revision.Status != StatusActive {
			continue
		}
		key, polarity := semanticIdentity(revision)
		groups[key] = append(groups[key], semanticHead{
			memoryID: memoryID, revisionID: revision.RevisionID, polarity: polarity,
		})
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(left, right int) bool { return group[left].memoryID < group[right].memoryID })
		memoryIDs := make([]string, 0, len(group))
		revisionIDs := make([]string, 0, len(group))
		hasAffirmative, hasProhibitive := false, false
		for _, item := range group {
			memoryIDs = append(memoryIDs, item.memoryID)
			revisionIDs = append(revisionIDs, item.revisionID)
			hasAffirmative = hasAffirmative || item.polarity == polarityAffirmative
			hasProhibitive = hasProhibitive || item.polarity == polarityProhibitive
		}
		code := "duplicate_semantic_memory"
		message := "active portable memories share the same semantic identity and require explicit deduplication"
		if hasAffirmative && hasProhibitive {
			code = "semantic_conflict"
			message = "active portable memories have opposing semantics and are quarantined"
		}
		addIssue(report, VerificationIssue{
			Code: code, RelatedMemoryIDs: memoryIDs, RelatedRevisionIDs: revisionIDs, Message: message,
		})
	}
}

func semanticIdentity(revision Revision) (string, semanticPolarity) {
	normalized := normalizeSemanticText(revision.Text)
	core := semanticCore(normalized)
	hasher := sha256.New()
	for _, value := range []string{
		"portable-semantic-key/v1alpha1", string(revision.ScopeKind), revision.ScopeValue, core,
	} {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(value))
	}
	return hex.EncodeToString(hasher.Sum(nil)), detectSemanticPolarity(normalized)
}

func detectSemanticPolarity(normalized string) semanticPolarity {
	padded := " " + normalized + " "
	for _, phrase := range []string{
		" must not ", " do not ", " don t ", " never ", " without ", " prohibited ", " forbid ",
	} {
		if strings.Contains(padded, phrase) {
			return polarityProhibitive
		}
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, phrase := range []string{"不要", "不得", "不能", "禁止", "不可", "不允许"} {
		if strings.Contains(compact, phrase) {
			return polarityProhibitive
		}
	}
	return polarityAffirmative
}

func semanticCore(normalized string) string {
	value := normalized
	for _, phrase := range []string{
		"请记住", "记住", "以后都", "从现在开始", "我再次强调", "重新强调", "再次",
		"我说", "刚才", "前面", "仍然", "必须", "不要", "不得", "不能", "禁止", "不可", "不允许",
	} {
		value = strings.ReplaceAll(value, phrase, "")
	}
	ignored := map[string]bool{
		"a": true, "again": true, "already": true, "always": true, "an": true,
		"as": true, "be": true, "do": true, "don": true, "forbid": true,
		"from": true, "i": true, "keep": true, "must": true, "never": true,
		"not": true, "only": true, "please": true, "preserve": true, "prohibited": true,
		"remember": true, "required": true, "said": true, "shall": true, "should": true,
		"still": true, "t": true, "that": true, "to": true, "without": true,
		"you": true,
	}
	words := strings.Fields(value)
	kept := make([]string, 0, len(words))
	for _, word := range words {
		if !ignored[word] {
			kept = append(kept, word)
		}
	}
	core := strings.Join(kept, " ")
	if core == "" {
		return normalized
	}
	return core
}

func normalizeSemanticText(text string) string {
	var builder strings.Builder
	space := false
	for _, char := range strings.ToLower(text) {
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
