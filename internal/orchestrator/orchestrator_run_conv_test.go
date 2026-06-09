package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/tokenutil"
)

// ---------------------------------------------------------------------------
// orchestrator_run_conv.go — buildConversationContext.
//
// Reads session history and formats it as a transcript block for the
// system prompt. Critical path on every RunAgent call when the chat
// has prior messages. Subtle behaviour worth pinning:
//
//   1. The last user message is STRIPPED — bridge code appends it
//      just before RunAgent, and the formatter receives the same
//      message that's about to be sent. Re-including it would duplicate
//      it in the system prompt + user turn.
//   2. Token budget honored newest-first; oldest messages drop on overflow.
//   3. Mid-message truncation: when remaining budget > 200 chars, a
//      partial copy with "...(truncated)" suffix is included.
//   4. Mid-message: budget ≤ 200 chars → drop entirely, don't include
//      a useless ~100-char fragment.
//   5. Header / trailer fences let downstream prompts recognize the
//      block; pinning the literal strings catches a refactor that
//      changed the marker syntax.
// ---------------------------------------------------------------------------

// newConvOrchestrator wires a real conversation.Store rooted in a
// tempdir so each test gets isolated session JSONL files. The
// orchestrator is the only field we need set for buildConversationContext.
func newConvOrchestrator(t *testing.T) (*Orchestrator, *conversation.Store) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	store := conversation.NewStore(dir, logger)
	t.Cleanup(func() { store.Close() })
	return &Orchestrator{convStore: store, logger: logger}, store
}

func appendMsg(t *testing.T, store *conversation.Store, sessionID string, role conversation.Role, content, toolSummary string, ts time.Time) {
	t.Helper()
	if err := store.Append(context.Background(), sessionID, conversation.Message{
		ID:          "m-" + ts.Format(time.RFC3339Nano),
		Role:        role,
		Content:     content,
		ToolSummary: toolSummary,
		Timestamp:   ts,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func TestBuildConversationContext_NoSession_ReturnsEmpty(t *testing.T) {
	// Read returns ([], no-error) on a missing session file. Pin that
	// the function treats this as "no history" rather than emitting
	// just the header/trailer fences with nothing between them.
	o, _ := newConvOrchestrator(t)
	got := o.buildConversationContext(context.Background(), "ses-nope", 8000)
	if got != "" {
		t.Errorf("missing session = %q, want \"\" (no history → no fences)", got)
	}
}

func TestBuildConversationContext_StripsTrailingUserMessage(t *testing.T) {
	// Source: "Skip the current user message (just appended by bridge
	// before RunAgent call)". An assistant msg followed by a user msg
	// → the user is stripped, assistant rendered.
	o, store := newConvOrchestrator(t)
	const sid = "ses-strip"
	appendMsg(t, store, sid, conversation.RoleAssistant, "earlier reply", "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleUser, "current question — must be stripped", "", time.Unix(2, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	if !strings.Contains(got, "earlier reply") {
		t.Errorf("assistant msg missing from output: %q", got)
	}
	if strings.Contains(got, "current question — must be stripped") {
		t.Errorf("trailing user msg leaked into history: %q (should have been stripped)", got)
	}
}

func TestBuildConversationContext_OnlyTrailingUserMessage_ReturnsEmpty(t *testing.T) {
	// If the only message is the trailing user message, stripping it
	// leaves zero messages → return empty (NOT empty fences).
	o, store := newConvOrchestrator(t)
	const sid = "ses-only-user"
	appendMsg(t, store, sid, conversation.RoleUser, "first ever message", "", time.Unix(1, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	if got != "" {
		t.Errorf("solo-user-stripped = %q, want \"\"", got)
	}
}

func TestBuildConversationContext_NonUserTrailingNotStripped(t *testing.T) {
	// The strip only fires for trailing RoleUser. A trailing assistant
	// message (e.g. mid-tool-loop session) must stay.
	o, store := newConvOrchestrator(t)
	const sid = "ses-trailing-ast"
	appendMsg(t, store, sid, conversation.RoleUser, "previous user", "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "trailing assistant must stay", "", time.Unix(2, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	if !strings.Contains(got, "trailing assistant must stay") {
		t.Errorf("trailing assistant got stripped: %q", got)
	}
	if !strings.Contains(got, "previous user") {
		t.Errorf("previous-user missing from output: %q", got)
	}
}

func TestBuildConversationContext_HeaderTrailer_Pinned(t *testing.T) {
	// Literal fence markers — downstream prompt scrapers / human
	// reviewers grep for these. A refactor that changed casing or
	// added punctuation must update this test in step.
	o, store := newConvOrchestrator(t)
	const sid = "ses-fences"
	appendMsg(t, store, sid, conversation.RoleAssistant, "hi", "", time.Unix(1, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	if !strings.HasPrefix(got, "[CONVERSATION HISTORY - previous messages in this session]\n") {
		t.Errorf("missing exact opening header; got = %q", got)
	}
	if !strings.Contains(got, "[END CONVERSATION HISTORY]") {
		t.Errorf("missing closing fence: %q", got)
	}
	if !strings.HasSuffix(got, "The user's new message follows. Continue the conversation naturally, referencing previous context when relevant.") {
		t.Errorf("missing exact trailer continuation prompt: %q", got)
	}
}

func TestBuildConversationContext_RoleAndContentLineFormat(t *testing.T) {
	// Per-message line shape: "[<role>]: <content>\n" with optional
	// "  <tool_summary>\n" indented next line. Pinning so a regression
	// that swapped colons / removed brackets would surface here.
	o, store := newConvOrchestrator(t)
	const sid = "ses-line"
	appendMsg(t, store, sid, conversation.RoleAssistant, "answer body", "ran tests: 5 passed", time.Unix(1, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	if !strings.Contains(got, "[assistant]: answer body\n") {
		t.Errorf("missing role/content line shape; got %q", got)
	}
	if !strings.Contains(got, "  ran tests: 5 passed\n") {
		t.Errorf("missing 2-space-indented ToolSummary line; got %q", got)
	}
}

func TestBuildConversationContext_ChronologicalOrderInOutput(t *testing.T) {
	// Source iterates newest-first to apply budget, then reverses to
	// chronological. A regression that left the slice newest-first
	// would render the transcript backwards — confusing both for the
	// LLM and human reviewers.
	o, store := newConvOrchestrator(t)
	const sid = "ses-order"
	appendMsg(t, store, sid, conversation.RoleAssistant, "FIRST_REPLY", "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "SECOND_REPLY", "", time.Unix(2, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "THIRD_REPLY", "", time.Unix(3, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)
	firstIdx := strings.Index(got, "FIRST_REPLY")
	secondIdx := strings.Index(got, "SECOND_REPLY")
	thirdIdx := strings.Index(got, "THIRD_REPLY")
	if firstIdx == -1 || secondIdx == -1 || thirdIdx == -1 {
		t.Fatalf("not all messages present: first=%d second=%d third=%d body=%q", firstIdx, secondIdx, thirdIdx, got)
	}
	if !(firstIdx < secondIdx && secondIdx < thirdIdx) {
		t.Errorf("messages not in chronological order: first=%d second=%d third=%d", firstIdx, secondIdx, thirdIdx)
	}
}

func TestBuildConversationContext_BudgetDropsOldestFirst(t *testing.T) {
	// Token budget at 100 → 400 chars total. Seed 3 messages of ~200
	// chars each — only the most recent should fit. Pin newest-wins
	// (a regression to oldest-wins would silently truncate the
	// recently-relevant turns).
	o, store := newConvOrchestrator(t)
	const sid = "ses-budget"
	big := strings.Repeat("x", 200)
	appendMsg(t, store, sid, conversation.RoleAssistant, "OLD_"+big, "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "MID_"+big, "", time.Unix(2, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "NEW_"+big, "", time.Unix(3, 0))

	got := o.buildConversationContext(context.Background(), sid, 100) // 400 char budget
	if !strings.Contains(got, "NEW_") {
		t.Errorf("newest msg missing; got %q...", got[:min(200, len(got))])
	}
	if strings.Contains(got, "OLD_") {
		t.Errorf("oldest msg should have been dropped under tight budget; got %q...", got[:min(400, len(got))])
	}
}

func TestBuildConversationContext_MidMessageTruncation_AboveThreshold(t *testing.T) {
	// Source: `if remaining > 200 { truncate the next message with
	// "...(truncated)" suffix and break }`. Engineer a budget where the
	// next message can't fully fit but >200 chars remain → it should
	// appear truncated.
	o, store := newConvOrchestrator(t)
	const sid = "ses-trunc"
	// Two messages: one small (fits), one large (overflows but with
	// >200 chars of room left).
	appendMsg(t, store, sid, conversation.RoleAssistant, "small-msg", "", time.Unix(1, 0))
	bigMsg := strings.Repeat("y", 500)
	appendMsg(t, store, sid, conversation.RoleAssistant, "BIG_"+bigMsg, "", time.Unix(2, 0))

	// Token budget: 100 → 400 chars. Big msg is ~504 chars, won't fit.
	// Small msg is ~9 chars. After Big msg's 200-truncated copy +
	// ToolSummary (none), small msg may NOT make it. But: source
	// iterates newest-first, so big msg is processed first; it
	// doesn't fit → truncated → break. Small msg never seen.
	got := o.buildConversationContext(context.Background(), sid, 100)
	if !strings.Contains(got, "...(truncated)") {
		t.Errorf("expected \"...(truncated)\" marker in output; got %q", got)
	}
	if !strings.Contains(got, "BIG_") {
		t.Errorf("truncated message lost its identifying prefix; got %q", got)
	}
}

func TestBuildConversationContext_MidMessageTruncation_BelowThreshold_Skipped(t *testing.T) {
	// Inverse: when remaining ≤ 200 chars, the source drops the
	// would-be-truncated message entirely (no ...(truncated) marker).
	// A ~100-char fragment is useless to the LLM. Pin the drop.
	o, store := newConvOrchestrator(t)
	const sid = "ses-tight"
	// Newest-first is by APPEND order (`for i := len-1 ... ; i--`),
	// not by Timestamp — so the second Append below is what gets
	// processed first. Order so that the fitting one is "newest".
	appendMsg(t, store, sid, conversation.RoleAssistant, "OLDER_"+strings.Repeat("b", 300), "", time.Unix(0, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, strings.Repeat("a", 300), "", time.Unix(1, 0))

	// Budget 100 → 400 chars. Newest (300 chars) fits → 100 chars left.
	// Older message (306 chars) can't fit, remaining≤200 → must be
	// SKIPPED, not truncated.
	got := o.buildConversationContext(context.Background(), sid, 100)
	if strings.Contains(got, "...(truncated)") {
		t.Errorf("found \"...(truncated)\" but remaining budget was ≤200 — older message should have been skipped, not truncated; got %q",
			got[:min(500, len(got))])
	}
	if strings.Contains(got, "OLDER_") {
		t.Errorf("older message was rendered despite tight remaining budget; got %q", got[:min(500, len(got))])
	}
}

func TestBuildConversationContext_ZeroBudget_ReturnsEmpty(t *testing.T) {
	// Token budget == 0 → char budget == 0 → no message fits, no
	// truncation possible (remaining never exceeds 200). Selected slice
	// stays empty → function returns "". A regression that emitted
	// empty fences would clutter the system prompt with no signal.
	o, store := newConvOrchestrator(t)
	const sid = "ses-zero"
	appendMsg(t, store, sid, conversation.RoleAssistant, "anything", "", time.Unix(1, 0))

	got := o.buildConversationContext(context.Background(), sid, 0)
	if got != "" {
		t.Errorf("zero budget = %q, want \"\"", got)
	}
}

func TestBuildConversationContext_ToolSummaryCountedInBudget(t *testing.T) {
	// Source: `msgLen := len(msg.Content) + len(msg.ToolSummary)`.
	// Pin so a regression that ignored ToolSummary in budget math
	// would silently let the transcript exceed the requested cap.
	o, store := newConvOrchestrator(t)
	const sid = "ses-tool"
	// Newest-first is by APPEND order: the second Append below is
	// what gets processed first. Order so the "newest" msg has the
	// huge ToolSummary that consumes the entire budget.
	appendMsg(t, store, sid, conversation.RoleAssistant, "even smaller", "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "small", strings.Repeat("z", 500), time.Unix(2, 0))

	// Budget 100 → 400 chars. Latest msg total = 505 chars → doesn't
	// fit. Remaining=400, > 200 → truncated copy included → break.
	// Older "even smaller" never processed. If a regression dropped
	// ToolSummary from the budget math, the latest msg's Content
	// alone is 5 chars (fits), so totalChars would be 5 and "even
	// smaller" would also fit — making the assertion below fire.
	got := o.buildConversationContext(context.Background(), sid, 100)
	if strings.Contains(got, "even smaller") {
		t.Errorf("older msg should have been excluded once budget was consumed; got %q", got[:min(500, len(got))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Conversation compaction — buildConversationContext summarizes the overflow
// (oldest) slice into an [EARLIER CONVERSATION — SUMMARY] block when an aux
// summarizer is wired, instead of dropping it. The fallback (no summarizer,
// or summarize error) must stay byte-compatible with the truncation path the
// tests above pin.
// ---------------------------------------------------------------------------

// fakeConvSummarizer is a test double for ConversationSummarizer. It records
// call count and returns a canned result/error.
type fakeConvSummarizer struct {
	out   string
	err   error
	calls int
	// block makes Summarize wait for context cancellation (timeout) and
	// return ctx.Err(), to exercise the bounded-timeout fallback.
	block bool
}

func (f *fakeConvSummarizer) Summarize(ctx context.Context, _ string) (string, error) {
	f.calls++
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.out, f.err
}

// captureJournal captures emitted entries for assertions.
type captureJournal struct{ entries []JournalEntry }

func (f *captureJournal) Emit(_ context.Context, e JournalEntry) (string, error) {
	f.entries = append(f.entries, e)
	return "", nil
}

// seedOverflowSession appends three large messages that together exceed any
// reasonable budget, so the oldest overflow the recent window. Returns the
// session id.
func seedOverflowSession(t *testing.T, store *conversation.Store) string {
	t.Helper()
	const sid = "ses-overflow"
	big := strings.Repeat("x", 4000)
	appendMsg(t, store, sid, conversation.RoleAssistant, "OLDEST_"+big, "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "MIDDLE_"+big, "", time.Unix(2, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "NEWEST_"+big, "", time.Unix(3, 0))
	return sid
}

func TestBuildConversationContext_Compaction_SummarizesOverflow(t *testing.T) {
	o, store := newConvOrchestrator(t)
	sum := &fakeConvSummarizer{out: "COMPACTED_SUMMARY_OF_OLD_TURNS"}
	o.SetConversationSummarizer(sum)
	sid := seedOverflowSession(t, store)

	// tokenBudget 2000 → 8000 char budget → summaryBudget 1200 (≥200), so
	// compaction engages and the oldest message overflows.
	got := o.buildConversationContext(context.Background(), sid, 2000)

	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	if !strings.Contains(got, "[EARLIER CONVERSATION — SUMMARY") {
		t.Errorf("missing summary block header; got %q", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "COMPACTED_SUMMARY_OF_OLD_TURNS") {
		t.Errorf("summary text missing; got %q", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "[END EARLIER CONVERSATION]") {
		t.Errorf("missing summary closing fence; got %q", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "NEWEST_") {
		t.Errorf("recent message missing from verbatim window; got %q", got[:min(300, len(got))])
	}
	// Summary block must precede the verbatim history fence.
	si := strings.Index(got, "[EARLIER CONVERSATION")
	hi := strings.Index(got, "[CONVERSATION HISTORY")
	if !(si >= 0 && hi >= 0 && si < hi) {
		t.Errorf("summary block must precede history fence: si=%d hi=%d", si, hi)
	}
}

func TestBuildConversationContext_Compaction_ErrorFallsBackToTruncation(t *testing.T) {
	o, store := newConvOrchestrator(t)
	sum := &fakeConvSummarizer{err: errors.New("aux model down")}
	o.SetConversationSummarizer(sum)
	sid := seedOverflowSession(t, store)

	got := o.buildConversationContext(context.Background(), sid, 2000)

	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1 (attempted then failed)", sum.calls)
	}
	if strings.Contains(got, "[EARLIER CONVERSATION") {
		t.Errorf("summary block emitted despite summarize error; got %q", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "NEWEST_") {
		t.Errorf("recent message missing after fallback; got %q", got[:min(300, len(got))])
	}
}

func TestBuildConversationContext_Compaction_NilSummarizerNoBlock(t *testing.T) {
	// No summarizer wired → compaction off, no aux call, oldest dropped as
	// before. This is the default production-without-aux-model path.
	o, store := newConvOrchestrator(t)
	sid := seedOverflowSession(t, store)

	got := o.buildConversationContext(context.Background(), sid, 2000)

	if strings.Contains(got, "[EARLIER CONVERSATION") {
		t.Errorf("summary block emitted with no summarizer wired; got %q", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "NEWEST_") {
		t.Errorf("recent message missing; got %q", got[:min(300, len(got))])
	}
}

func TestBuildConversationContext_Compaction_UnderBudgetNoCall(t *testing.T) {
	// When the whole history fits, there is no overflow → the summarizer is
	// never invoked and no summary block appears.
	o, store := newConvOrchestrator(t)
	sum := &fakeConvSummarizer{out: "should-not-appear"}
	o.SetConversationSummarizer(sum)
	const sid = "ses-underbudget"
	appendMsg(t, store, sid, conversation.RoleAssistant, "short reply one", "", time.Unix(1, 0))
	appendMsg(t, store, sid, conversation.RoleAssistant, "short reply two", "", time.Unix(2, 0))

	got := o.buildConversationContext(context.Background(), sid, 8000)

	if sum.calls != 0 {
		t.Errorf("summarizer invoked for under-budget conversation: calls=%d", sum.calls)
	}
	if strings.Contains(got, "[EARLIER CONVERSATION") {
		t.Errorf("summary block emitted with no overflow; got %q", got)
	}
}

func TestBuildConversationContext_Compaction_TimeoutFallsBackToTruncation(t *testing.T) {
	// A slow (blocking, non-erroring) aux model must not stall the turn:
	// the bounded timeout fires, the call errors, and we fall back to
	// truncation. Shrink the timeout so the test is fast.
	old := conversationSummarizeTimeout
	conversationSummarizeTimeout = 30 * time.Millisecond
	defer func() { conversationSummarizeTimeout = old }()

	o, store := newConvOrchestrator(t)
	o.SetConversationSummarizer(&fakeConvSummarizer{block: true})
	sid := seedOverflowSession(t, store)

	got, stats := o.buildConversationContextWithStats(context.Background(), sid, 2000)

	if strings.Contains(got, "[EARLIER CONVERSATION") {
		t.Errorf("timed-out summary should not emit a block; got %q", got[:min(300, len(got))])
	}
	if stats.Summarized {
		t.Errorf("Summarized must be false on timeout; stats=%+v", stats)
	}
	if stats.OverflowMessages == 0 {
		t.Errorf("overflow should still be recorded on fallback; stats=%+v", stats)
	}
	if !strings.Contains(got, "NEWEST_") {
		t.Errorf("recent window should survive the fallback; got %q", got[:min(300, len(got))])
	}
}

func TestBuildConversationContext_Compaction_BlankOutputFallsBack(t *testing.T) {
	// A summarizer returning only whitespace is treated as no summary →
	// fall back to truncation, no block, Summarized=false.
	o, store := newConvOrchestrator(t)
	o.SetConversationSummarizer(&fakeConvSummarizer{out: "   \n\t  "})
	sid := seedOverflowSession(t, store)

	got, stats := o.buildConversationContextWithStats(context.Background(), sid, 2000)

	if strings.Contains(got, "[EARLIER CONVERSATION") {
		t.Errorf("blank summary should not emit a block; got %q", got[:min(300, len(got))])
	}
	if stats.Summarized {
		t.Errorf("Summarized must be false on blank output; stats=%+v", stats)
	}
}

func TestSummarizeOverflow_DefensiveReturns(t *testing.T) {
	// Direct unit coverage of the early-return guards the budget path never
	// reaches in practice (nil summarizer / empty overflow).
	o, _ := newConvOrchestrator(t) // no summarizer wired
	if got := o.summarizeOverflow(context.Background(),
		[]conversation.Message{{Role: conversation.RoleAssistant, Content: "x"}}, 500); got != "" {
		t.Errorf("nil summarizer → want \"\", got %q", got)
	}
	o.SetConversationSummarizer(&fakeConvSummarizer{out: "ignored"})
	if got := o.summarizeOverflow(context.Background(), nil, 500); got != "" {
		t.Errorf("empty overflow → want \"\", got %q", got)
	}
}

func TestSummarizeOverflow_RendersToolSummaryLine(t *testing.T) {
	// Overflow messages carrying a ToolSummary must render the indented
	// tool line into the summarizer prompt (covers that branch).
	o, _ := newConvOrchestrator(t)
	o.SetConversationSummarizer(&fakeConvSummarizer{out: "ok"})
	out := o.summarizeOverflow(context.Background(), []conversation.Message{
		{Role: conversation.RoleAssistant, Content: "did work", ToolSummary: "ran build: ok"},
	}, 500)
	if out != "ok" {
		t.Errorf("want summarizer output \"ok\", got %q", out)
	}
}

func TestBuildConversationContextWithStats_ReportsCompaction(t *testing.T) {
	o, store := newConvOrchestrator(t)
	o.SetConversationSummarizer(&fakeConvSummarizer{out: "COMPACTED"})
	sid := seedOverflowSession(t, store)

	_, stats := o.buildConversationContextWithStats(context.Background(), sid, 2000)

	if !stats.Summarized || stats.OverflowMessages == 0 || stats.SummaryBytes == 0 {
		t.Errorf("expected compaction stats, got %+v", stats)
	}
}

func TestBuildConversationContext_WrapperMatchesStatsVariant(t *testing.T) {
	// The string-only wrapper must return exactly the stats variant's first
	// return — guards against drift between the two entry points.
	o, store := newConvOrchestrator(t)
	o.SetConversationSummarizer(&fakeConvSummarizer{out: "COMPACTED"})
	sid := seedOverflowSession(t, store)

	a := o.buildConversationContext(context.Background(), sid, 2000)
	b, _ := o.buildConversationContextWithStats(context.Background(), sid, 2000)
	if a != b {
		t.Errorf("wrapper diverged from stats variant:\n wrapper=%q\n stats  =%q", a, b)
	}
}

func TestEmitCompactionEvent(t *testing.T) {
	o, _ := newConvOrchestrator(t)
	fj := &captureJournal{}
	o.SetJournal(fj)

	// No overflow → no emit.
	o.emitCompactionEvent(context.Background(), AgentRunRequest{ChatID: "c1"}, compactionStats{})
	if len(fj.entries) != 0 {
		t.Fatalf("no-overflow should not emit; got %d entries", len(fj.entries))
	}

	// Overflow + summarized → one scoped conversation.compacted event.
	o.emitCompactionEvent(context.Background(),
		AgentRunRequest{WorkspaceID: "w1", CrewID: "cr1", AgentID: "a1", ChatID: "c1"},
		compactionStats{OverflowMessages: 3, Summarized: true, SummaryBytes: 120})
	if len(fj.entries) != 1 {
		t.Fatalf("expected one emit, got %d", len(fj.entries))
	}
	e := fj.entries[0]
	if e.Type != "conversation.compacted" {
		t.Errorf("type = %q, want conversation.compacted", e.Type)
	}
	if e.WorkspaceID != "w1" || e.CrewID != "cr1" || e.AgentID != "a1" {
		t.Errorf("scope not propagated: %+v", e)
	}
	if e.Payload["overflow_messages"] != 3 || e.Payload["summarized"] != true || e.Payload["summary_bytes"] != 120 {
		t.Errorf("payload mismatch: %+v", e.Payload)
	}
	if e.Payload["session_id"] != "c1" {
		t.Errorf("session_id not in payload: %+v", e.Payload)
	}

	// Overflow but not summarized → still emits (truncation audit), summarized=false.
	fj.entries = nil
	o.emitCompactionEvent(context.Background(),
		AgentRunRequest{AgentID: "a1", ChatID: "c2"},
		compactionStats{OverflowMessages: 2, Summarized: false})
	if len(fj.entries) != 1 || fj.entries[0].Payload["summarized"] != false {
		t.Errorf("truncation fallback should emit with summarized=false; got %+v", fj.entries)
	}
}

func TestBuildConversationContext_Compaction_SummaryClampedToBudget(t *testing.T) {
	// A verbose model must not blow past the reserved summary slice. The 'Q'
	// marker appears nowhere in the fences/trailer, so counting it isolates
	// the summary body length.
	o, store := newConvOrchestrator(t)
	sum := &fakeConvSummarizer{out: strings.Repeat("Q", 100000)}
	o.SetConversationSummarizer(sum)
	sid := seedOverflowSession(t, store)

	got := o.buildConversationContext(context.Background(), sid, 2000)

	summaryBudget := tokenutil.CharsForTokens(2000) * conversationSummaryBudgetPct / 100
	qCount := strings.Count(got, "Q")
	if qCount == 0 {
		t.Fatalf("summary body missing entirely; got %q", got[:min(300, len(got))])
	}
	if qCount > summaryBudget {
		t.Errorf("summary not clamped: %d summary chars > budget %d", qCount, summaryBudget)
	}
}
