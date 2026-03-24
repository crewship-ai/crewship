// Package tokenutil provides heuristic token estimation for context budgeting.
// Uses ~4 chars/token approximation — conservative enough for Claude Code's 200K context.
package tokenutil

const (
	// MaxSystemPromptTokens is the conservative budget for the system prompt
	// within Claude Code's 200K context window. Leaves room for user message,
	// tool definitions, and Claude Code overhead.
	MaxSystemPromptTokens = 32000

	// ConversationBudgetPct is the percentage of remaining budget allocated to
	// conversation history (after base system prompt is accounted for).
	ConversationBudgetPct = 60

	// MemoryBudgetPct is the percentage of remaining budget allocated to
	// agent memory context.
	MemoryBudgetPct = 40
)

// EstimateTokens returns an approximate token count for a string.
// Uses ~4 chars/token heuristic. Returns at least 1 for non-empty strings.
func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	t := len(s) / 4
	if t < 1 {
		return 1
	}
	return t
}

// CharsForTokens converts a token budget to an approximate character budget.
func CharsForTokens(tokens int) int {
	return tokens * 4
}
