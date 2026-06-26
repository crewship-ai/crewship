package conversation

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
)

// TestPartAccumulator_CoalescesAndSegments is the core of the parts model.
// It must:
//   - coalesce consecutive text deltas into ONE text part,
//   - coalesce consecutive thinking deltas into ONE thinking part,
//   - emit tool_call / tool_result as their own parts, carrying tool_name/tool_id
//     lifted from the normalized AgentEvent metadata,
//   - START A FRESH text part after tool activity, so text that follows a tool
//     never visually "appends" to the text bubble above the tools.
//
// The accumulator works on the adapter-neutral normalized event vocabulary
// (text|thinking|tool_call|tool_result|image) emitted by every CLI adapter —
// NOT on Claude stream-json — so Codex/Gemini/OpenCode get the same behaviour
// for free.
func TestPartAccumulator_CoalescesAndSegments(t *testing.T) {
	acc := NewPartAccumulator()

	acc.Add("text", "Hello ", nil)
	acc.Add("text", "world", nil)
	acc.Add("thinking", "let me ", map[string]any{"streaming": true})
	acc.Add("thinking", "think", map[string]any{"streaming": true})
	acc.Add("tool_call", "Read", map[string]any{"tool_name": "Read", "tool_id": "t1"})
	acc.Add("tool_result", "file contents", map[string]any{"tool_id": "t1"})
	acc.Add("text", "Done", nil)

	parts := acc.Parts()

	if len(parts) != 5 {
		t.Fatalf("expected 5 parts, got %d: %+v", len(parts), parts)
	}

	if parts[0].Type != "text" || parts[0].Content != "Hello world" {
		t.Errorf("part[0]: want text 'Hello world', got %s %q", parts[0].Type, parts[0].Content)
	}
	if parts[1].Type != "thinking" || parts[1].Content != "let me think" {
		t.Errorf("part[1]: want thinking 'let me think', got %s %q", parts[1].Type, parts[1].Content)
	}
	if parts[2].Type != "tool_call" || parts[2].ToolName != "Read" || parts[2].ToolID != "t1" {
		t.Errorf("part[2]: want tool_call Read/t1, got %s name=%q id=%q", parts[2].Type, parts[2].ToolName, parts[2].ToolID)
	}
	if parts[3].Type != "tool_result" || parts[3].ToolID != "t1" || parts[3].Content != "file contents" {
		t.Errorf("part[3]: want tool_result/t1, got %s id=%q content=%q", parts[3].Type, parts[3].ToolID, parts[3].Content)
	}
	// The crucial segmentation assertion: text after a tool is a NEW part,
	// never merged back into parts[0].
	if parts[4].Type != "text" || parts[4].Content != "Done" {
		t.Errorf("part[4]: want fresh text 'Done', got %s %q", parts[4].Type, parts[4].Content)
	}
}

// TestPartAccumulator_IgnoresNonContentEvents makes sure status/system/result/
// error control events never become persisted content parts — they are
// transport/telemetry, not conversation content.
func TestPartAccumulator_IgnoresNonContentEvents(t *testing.T) {
	acc := NewPartAccumulator()
	acc.Add("status", "Starting agent...", nil)
	acc.Add("system", "init", nil)
	acc.Add("result", "", map[string]any{"total_cost_usd": 0.01})

	if parts := acc.Parts(); len(parts) != 0 {
		t.Fatalf("expected 0 content parts, got %d: %+v", len(parts), parts)
	}
}

// TestMessagePartsRoundTrip proves the ordered parts survive a JSONL
// Append→Read round-trip unchanged, so a reload renders exactly what streamed.
func TestMessagePartsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New("error", "json", nil)
	store := NewStore(dir, logger)
	defer store.Close()

	msg := Message{
		ID:        "msg_parts_1",
		Role:      RoleAssistant,
		Content:   "Hello world",
		Timestamp: time.Now().UTC(),
		Parts: []Part{
			{Type: "text", Content: "Hello world"},
			{Type: "tool_call", Content: "Read", ToolName: "Read", ToolID: "t1"},
			{Type: "tool_result", Content: "file contents", ToolID: "t1"},
		},
	}
	if err := store.Append(ctx, "session-parts", msg); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := store.Read(ctx, "session-parts", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if len(got[0].Parts) != 3 {
		t.Fatalf("expected 3 parts after round-trip, got %d: %+v", len(got[0].Parts), got[0].Parts)
	}
	if got[0].Parts[1].ToolName != "Read" || got[0].Parts[1].ToolID != "t1" {
		t.Errorf("tool_call part not preserved: %+v", got[0].Parts[1])
	}
}

// TestNormalizedPartsBackwardCompat ensures a legacy JSONL message written
// before the parts model (Content only, no Parts) still renders: NormalizedParts
// synthesizes a single text part from Content so old conversations are not
// suddenly blank.
func TestNormalizedPartsBackwardCompat(t *testing.T) {
	legacy := Message{
		ID:      "legacy_1",
		Role:    RoleAssistant,
		Content: "answer from before the parts model",
	}
	parts := legacy.NormalizedParts()
	if len(parts) != 1 {
		t.Fatalf("expected 1 synthesized part, got %d", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Content != "answer from before the parts model" {
		t.Errorf("synthesized part wrong: %+v", parts[0])
	}

	// A message that already has parts returns them verbatim.
	modern := Message{
		Parts: []Part{{Type: "text", Content: "x"}, {Type: "tool_call", Content: "Read"}},
	}
	if got := modern.NormalizedParts(); len(got) != 2 {
		t.Fatalf("expected 2 parts returned verbatim, got %d", len(got))
	}
}
