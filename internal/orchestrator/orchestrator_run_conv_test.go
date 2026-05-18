package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
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
