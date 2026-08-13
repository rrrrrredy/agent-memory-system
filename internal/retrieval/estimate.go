package retrieval

// EstimateTokens exposes the deterministic conservative estimator used by
// retrieval budgets. It is not a provider tokenizer or an exact token count.
func EstimateTokens(value string) int {
	return estimateTokens(value)
}
