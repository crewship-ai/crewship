package orchestrator

// Conversation history / context assembly helpers extracted from
// orchestrator_run.go.

import (
	"context"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

const (
	// conversationSummaryBudgetPct is the slice of the conversation char
	// budget reserved for the compaction summary block when older turns
	// overflow. The remainder funds the verbatim recent window. Kept small
	// so a summary never crowds out the recent transcript the agent needs
	// most.
	conversationSummaryBudgetPct = 15

	// minConversationSummaryChars floors the summary budget. Below this a
	// summary can't carry enough signal to be worth an aux-LLM round-trip,
	// so we skip compaction and fall back to plain truncation.
	minConversationSummaryChars = 200
)

// conversationSummaryInstruction is the directive handed to the aux model
// when compacting the overflow (oldest) slice of a long conversation. It
// asks for decisions, facts, constraints, and still-open threads — the
// signal that the verbatim recent window can no longer carry — and nothing
// else, so the summary stays dense.
const conversationSummaryInstruction = `You are compacting the earlier part of a conversation between a user and an AI agent so it survives once those turns scroll out of the context window.

Write a concise summary that preserves:
- decisions made and conclusions reached
- facts, identifiers, and constraints established
- tasks completed (so they are not re-attempted)
- still-open threads or pending follow-ups

Omit pleasantries and tool-call noise. Output only the summary prose, no preamble.

EARLIER CONVERSATION TO SUMMARIZE:
`

// buildConversationContext reads messages from the session JSONL and formats them
// as a conversation transcript for the system prompt. Uses a token budget to
// dynamically size the window — short exchanges get more turns, long tool-heavy
// turns get fewer but always include the most recent messages.
//
// When older turns overflow the budget AND an aux-LLM summarizer is wired
// (SetConversationSummarizer), the overflow slice is compacted into an
// [EARLIER CONVERSATION — SUMMARY] block prepended to the verbatim recent
// window, instead of being dropped outright. With no summarizer wired, or if
// the summarize call fails, the function falls back to plain newest-first
// truncation — byte-for-byte the historical behavior.
//
// Cache note: the returned block is mid-conversation content, never part of
// the cached system-prompt prefix, so injecting a freshly-generated summary
// here does not perturb the prompt-cache invariant.
func (o *Orchestrator) buildConversationContext(ctx context.Context, sessionID string, tokenBudget int) string {
	messages, err := o.convStore.Read(ctx, sessionID, 0, 0)
	if err != nil || len(messages) == 0 {
		return ""
	}

	// Skip the current user message (just appended by bridge before RunAgent call).
	if len(messages) > 0 && messages[len(messages)-1].Role == conversation.RoleUser {
		messages = messages[:len(messages)-1]
	}
	if len(messages) == 0 {
		return ""
	}

	charBudget := tokenutil.CharsForTokens(tokenBudget)

	// Default path: fill the recent window with the full budget. overflow
	// holds the older messages that didn't fit (chronological order).
	selected, overflow := selectRecentMessages(messages, charBudget)

	// Compaction path: if older turns overflowed and a summarizer is wired,
	// reserve a slice of the budget for a summary block and re-select the
	// recent window against the reduced budget so the two fit together. If
	// the summarize call produces nothing usable, we keep the full-budget
	// `selected` from above and drop the overflow — the historical behavior.
	var summaryBlock string
	if len(overflow) > 0 && o.getConvSummarizer() != nil {
		summaryBudget := charBudget * conversationSummaryBudgetPct / 100
		if summaryBudget >= minConversationSummaryChars {
			recent2, overflow2 := selectRecentMessages(messages, charBudget-summaryBudget)
			if s := o.summarizeOverflow(ctx, overflow2, summaryBudget); s != "" {
				summaryBlock = s
				selected = recent2
			}
		}
	}

	if len(selected) == 0 && summaryBlock == "" {
		return ""
	}

	var b strings.Builder

	if summaryBlock != "" {
		b.WriteString("[EARLIER CONVERSATION — SUMMARY of older messages no longer shown in full]\n")
		b.WriteString(summaryBlock)
		if !strings.HasSuffix(summaryBlock, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("[END EARLIER CONVERSATION]\n")
	}

	b.WriteString("[CONVERSATION HISTORY - previous messages in this session]\n")
	for _, msg := range selected {
		fmt.Fprintf(&b, "[%s]: %s\n", msg.Role, msg.Content)
		if msg.ToolSummary != "" {
			fmt.Fprintf(&b, "  %s\n", msg.ToolSummary)
		}
	}
	b.WriteString("[END CONVERSATION HISTORY]\n")
	b.WriteString("The user's new message follows. Continue the conversation naturally, referencing previous context when relevant.")
	return b.String()
}

// selectRecentMessages walks backward from the newest message accumulating
// until charBudget is exhausted, then returns the kept messages in
// chronological order plus the older messages that didn't fit (overflow,
// also chronological). It preserves the historical boundary rules: the first
// message that doesn't fully fit is included truncated when >200 chars of
// budget remain, otherwise dropped entirely.
func selectRecentMessages(messages []conversation.Message, charBudget int) (recent, overflow []conversation.Message) {
	var selected []conversation.Message
	totalChars := 0
	// cut is the exclusive upper bound of the overflow slice: messages[:cut]
	// are the older turns that did not make the recent window.
	cut := 0
	i := len(messages) - 1
	for ; i >= 0; i-- {
		msg := messages[i]
		msgLen := len(msg.Content) + len(msg.ToolSummary)
		if totalChars+msgLen > charBudget {
			// Try to fit a truncated version of this message.
			remaining := charBudget - totalChars
			if remaining > 200 {
				truncated := msg
				if len(truncated.Content) > remaining {
					truncated.Content = truncated.Content[:remaining-20] + "...(truncated)"
					truncated.ToolSummary = ""
				}
				selected = append(selected, truncated)
				// This message is (partially) included; the overflow is
				// everything strictly older than it.
				cut = i
			} else {
				// Dropped entirely; it joins the overflow.
				cut = i + 1
			}
			break
		}
		selected = append(selected, msg)
		totalChars += msgLen
	}
	if i < 0 {
		// Whole history fit within budget — no overflow.
		cut = 0
	}

	// Reverse selected to chronological order.
	for a, c := 0, len(selected)-1; a < c; a, c = a+1, c-1 {
		selected[a], selected[c] = selected[c], selected[a]
	}
	return selected, messages[:cut]
}

// summarizeOverflow renders the overflow messages into a transcript and asks
// the wired aux-LLM summarizer to compact them. Returns "" (caller falls back
// to truncation) when no summarizer is wired, the overflow is empty, or the
// call errors / yields blank output. The result is clamped to budget chars so
// a verbose model can't blow past the reserved slice.
func (o *Orchestrator) summarizeOverflow(ctx context.Context, overflow []conversation.Message, budget int) string {
	summarizer := o.getConvSummarizer()
	if summarizer == nil || len(overflow) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(conversationSummaryInstruction)
	for _, msg := range overflow {
		fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, msg.Content)
		if msg.ToolSummary != "" {
			fmt.Fprintf(&sb, "  %s\n", msg.ToolSummary)
		}
	}

	out, err := summarizer.Summarize(ctx, sb.String())
	if err != nil {
		// Best-effort: a wedged or misconfigured aux model must never fail a
		// run — fall back to truncation.
		o.logger.Debug("conversation compaction summarize failed; falling back to truncation",
			"session_messages", len(overflow), "error", err)
		return ""
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if len(out) > budget {
		out = out[:budget]
	}
	return out
}
