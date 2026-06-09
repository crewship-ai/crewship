package orchestrator

// Conversation history / context assembly helpers extracted from
// orchestrator_run.go.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

const (
	// conversationSummaryBudgetPct is the slice of the conversation char
	// budget reserved for the compaction summary block when older turns
	// overflow. The remainder funds the verbatim recent window.
	conversationSummaryBudgetPct = 15

	// minConversationSummaryChars floors the summary budget. Below this a
	// summary can't carry enough signal to be worth an aux-LLM round-trip,
	// so we skip compaction and fall back to plain truncation.
	minConversationSummaryChars = 200
)

// conversationSummarizeTimeout bounds the synchronous aux-LLM call on the
// prompt-assembly hot path. A slow (but not erroring) summarizer must never
// stall a turn — on timeout the call errors and we fall back to plain
// truncation. A var (not const) so tests can shrink it.
var conversationSummarizeTimeout = 12 * time.Second

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

// conversationTemporalAnchorInstruction is prepended to the aux-LLM prompt
// (before conversationSummaryInstruction) when a "today" date is resolvable.
// Without it the summarizer can echo imperative phrasing ("email the report")
// that a resumed agent reads as a still-open instruction and re-runs already
// completed work. It carries today's UTC date (2006-01-02) so the model can
// anchor finished actions as dated past-tense facts. The %s is the date.
//
// It lives only in the throwaway aux prompt — never in the cached
// system-prompt prefix — so the prompt-cache invariant is unaffected.
const conversationTemporalAnchorInstruction = `TEMPORAL ANCHORING — today's date is %s (UTC).
When you summarize, rewrite completed or imperative actions as dated past-tense facts (e.g. "Sent the report on %s"), not as open instructions. Prefer a date recoverable from the transcript itself; if none is present, anchor to "around %s". Never restate a finished action as a still-open instruction.

`

// compactionStats reports what buildConversationContextWithStats did with the
// overflow (older) slice, for observability / journal emission. The zero
// value means nothing overflowed — no compaction decision was made.
type compactionStats struct {
	// OverflowMessages is the count of older messages that did not fit the
	// verbatim recent window (the ones summarized, or dropped on fallback).
	OverflowMessages int
	// Summarized is true when the overflow was compacted into a summary
	// block; false when it was dropped by truncation (no summarizer wired,
	// or the summarize call failed / timed out / returned blank).
	Summarized bool
	// SummaryBytes is the size of the rendered summary body (0 when not summarized).
	SummaryBytes int
}

// buildConversationContext is the string-only convenience wrapper over
// buildConversationContextWithStats. Most callers (and tests) only need the
// assembled transcript; the orchestration layer uses the stats variant so it
// can emit a conversation.compacted journal event with run scope.
func (o *Orchestrator) buildConversationContext(ctx context.Context, sessionID string, tokenBudget int) string {
	s, _ := o.buildConversationContextWithStats(ctx, sessionID, tokenBudget)
	return s
}

// buildConversationContextWithStats reads messages from the session JSONL and
// formats them as a conversation transcript for the system prompt, returning
// what it did with any overflow (see compactionStats).
//
// It sizes a token budget and fills it newest-first; when older turns overflow
// AND an aux-LLM summarizer is wired (SetConversationSummarizer), the overflow
// slice is compacted into an [EARLIER CONVERSATION — SUMMARY] block prepended
// to the verbatim recent window, instead of being dropped. With no summarizer
// wired, or if the summarize call fails / times out, it falls back to plain
// newest-first truncation — byte-for-byte the historical behavior.
//
// Cache note: the returned block is mid-conversation content, never part of
// the cached system-prompt prefix, so a freshly-generated summary here does
// not perturb the prompt-cache invariant.
func (o *Orchestrator) buildConversationContextWithStats(ctx context.Context, sessionID string, tokenBudget int) (string, compactionStats) {
	var stats compactionStats

	messages, err := o.convStore.Read(ctx, sessionID, 0, 0)
	if err != nil || len(messages) == 0 {
		return "", stats
	}

	// Skip the current user message (just appended by bridge before RunAgent call).
	if len(messages) > 0 && messages[len(messages)-1].Role == conversation.RoleUser {
		messages = messages[:len(messages)-1]
	}
	if len(messages) == 0 {
		return "", stats
	}

	charBudget := tokenutil.CharsForTokens(tokenBudget)

	// Default path: fill the recent window with the full budget. overflow
	// holds the older messages that didn't fit (chronological order).
	selected, overflow := selectRecentMessages(messages, charBudget)
	stats.OverflowMessages = len(overflow)

	// Compaction path: if older turns overflowed and a summarizer is wired,
	// reserve a slice of the budget for a summary block and re-select the
	// recent window against the reduced budget so the two fit together. On a
	// summarize failure we keep the full-budget `selected` and drop the
	// overflow — the historical behavior.
	var summaryBlock string
	if len(overflow) > 0 && o.getConvSummarizer() != nil {
		summaryBudget := charBudget * conversationSummaryBudgetPct / 100
		if summaryBudget >= minConversationSummaryChars {
			recent2, overflow2 := selectRecentMessages(messages, charBudget-summaryBudget)
			if s := o.summarizeOverflow(ctx, overflow2, summaryBudget); s != "" {
				summaryBlock = s
				selected = recent2
				stats.OverflowMessages = len(overflow2)
			}
		}
	}
	if summaryBlock != "" {
		stats.Summarized = true
		stats.SummaryBytes = len(summaryBlock)
	}

	if len(selected) == 0 && summaryBlock == "" {
		return "", stats
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
	return b.String(), stats
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
			// Tool-context preservation: truncating this boundary message
			// zeroes its ToolSummary (below), leaving the recent window with
			// a half-sentence that references a tool whose result was
			// dropped. When the message actually carries a ToolSummary AND
			// at least one other message already fits, prefer dropping it
			// whole into the overflow (where it can still be summarized)
			// rather than emitting a tool-less fragment. We only do this when
			// the window is non-empty — never produce an empty window; the
			// >200 last-resort truncation still fires for a lone oversized
			// boundary message.
			wouldLoseToolSummary := msg.ToolSummary != "" && len(msg.Content) > remaining
			if remaining > 200 && !(wouldLoseToolSummary && len(selected) > 0) {
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
				// Dropped entirely; it joins the overflow (whole, so the
				// overflow slice never starts mid-message and the
				// ToolSummary survives for compaction).
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
// call errors / times out / yields blank output. The result is clamped to
// budget chars so a verbose model can't blow past the reserved slice. The call
// is bounded by conversationSummarizeTimeout so a slow aux model can't stall
// the turn.
func (o *Orchestrator) summarizeOverflow(ctx context.Context, overflow []conversation.Message, budget int) string {
	summarizer := o.getConvSummarizer()
	if summarizer == nil || len(overflow) == 0 {
		return ""
	}

	// Session-purity guard: summarizeOverflow is a pure function of its
	// overflow arg and the overflow must come from a single session. If a
	// future caching change ever mixed messages from different sessions into
	// one overflow slice, compacting them would leak one session's history
	// into another's summary. Detect a mixed ChatID and safe-degrade to
	// truncation (return "") rather than risk contamination.
	if id, ok := singleChatID(overflow); !ok {
		o.logger.Debug("conversation compaction skipped: overflow spans multiple sessions",
			"session_messages", len(overflow), "first_session_id", id)
		return ""
	}

	var sb strings.Builder
	// Temporal anchoring (best-effort): when a "today" date is resolvable,
	// prepend the anchor directive so the summary renders finished work as
	// dated past-tense facts. An empty date (zero clock) omits the directive,
	// keeping the prompt byte-identical to the pre-anchor baseline.
	if today := o.todayString(); today != "" {
		fmt.Fprintf(&sb, conversationTemporalAnchorInstruction, today, today, today)
	}
	sb.WriteString(conversationSummaryInstruction)
	for _, msg := range overflow {
		fmt.Fprintf(&sb, "[%s]: %s\n", msg.Role, msg.Content)
		if msg.ToolSummary != "" {
			fmt.Fprintf(&sb, "  %s\n", msg.ToolSummary)
		}
	}

	cctx, cancel := context.WithTimeout(ctx, conversationSummarizeTimeout)
	defer cancel()

	out, err := summarizer.Summarize(cctx, sb.String())
	if err != nil {
		// Best-effort: a wedged, slow, or misconfigured aux model must never
		// fail a run — fall back to truncation.
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

// todayString renders today's date as UTC 2006-01-02 for the temporal-anchor
// directive, or "" when no usable date is resolvable (zero clock) — the
// signal to omit the directive entirely (best-effort). The format idiom
// mirrors internal/orchestrator/memory.go's "today" rendering.
func (o *Orchestrator) todayString() string {
	now := o.nowUTC()
	if now.IsZero() {
		return ""
	}
	return now.Format("2006-01-02")
}

// singleChatID reports whether every message in msgs shares one ChatID,
// returning that id. An empty slice trivially passes (the callers guard
// len==0 separately). A mixed slice returns ok=false with the first id seen,
// for the skip-and-log path.
func singleChatID(msgs []conversation.Message) (id string, ok bool) {
	if len(msgs) == 0 {
		return "", true
	}
	id = msgs[0].ChatID
	for _, m := range msgs[1:] {
		if m.ChatID != id {
			return id, false
		}
	}
	return id, true
}

// emitCompactionEvent records a conversation.compacted journal entry when a
// turn's prior history overflowed the budget — whether the overflow was
// summarized or dropped by truncation. No-op when nothing overflowed. Nil-safe
// via getJournal(). Gives operators a CLI-queryable ("crewship journal") audit
// of what fell out of the context window.
func (o *Orchestrator) emitCompactionEvent(ctx context.Context, req AgentRunRequest, stats compactionStats) {
	if stats.OverflowMessages == 0 {
		return
	}
	summary := fmt.Sprintf("compacted %d older message(s) into a %d-byte summary",
		stats.OverflowMessages, stats.SummaryBytes)
	if !stats.Summarized {
		summary = fmt.Sprintf("dropped %d older message(s) by truncation (no summarizer available)",
			stats.OverflowMessages)
	}
	_, _ = o.getJournal().Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		Type:        "conversation.compacted",
		Severity:    "info",
		ActorType:   "system",
		ActorID:     req.AgentID,
		Summary:     summary,
		Payload: map[string]any{
			"session_id":        req.ChatID,
			"overflow_messages": stats.OverflowMessages,
			"summarized":        stats.Summarized,
			"summary_bytes":     stats.SummaryBytes,
		},
	})
}
