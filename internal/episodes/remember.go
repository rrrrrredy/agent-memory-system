package episodes

import "strings"

// IsExplicitRemember recognizes requests to retain an instruction. Statement
// admission and candidate extraction share this rule so later turns are not lost.
func IsExplicitRemember(text string) bool {
	return isExplicitRememberNormalized(normalizeText(text))
}

func isExplicitRememberNormalized(normalized string) bool {
	// Corrections can also request memory; the caller keeps their original kind.
	for _, prefix := range []string{"as i said ", "i said ", "again ", "我再次强调", "重新强调", "再次"} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = strings.TrimSpace(strings.TrimPrefix(normalized, prefix))
			break
		}
	}
	for _, prefix := range []string{"please ", "kindly "} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	// Require an opening request, an English word boundary, and a body. Mere
	// mentions such as "I remember" or "Do you remember" are not instructions.
	for _, prefix := range []string{
		"remember", "just remember", "always remember",
		"can you remember", "can you please remember",
		"could you remember", "could you please remember",
		"would you remember", "would you please remember",
		"will you remember", "will you please remember",
		"i want you to remember", "i would like you to remember", "i d like you to remember",
		"you must remember", "you should remember", "you need to remember",
		"from now on",
	} {
		if strings.HasPrefix(normalized, prefix+" ") {
			return true
		}
	}
	compact := strings.ReplaceAll(normalized, " ", "")
	for _, prefix := range []string{
		"记住", "请记住", "请你记住", "帮我记住", "请帮我记住", "你要记住",
		"以后都", "从现在开始",
	} {
		if strings.HasPrefix(compact, prefix) && len(compact) > len(prefix) {
			return true
		}
	}
	return false
}
