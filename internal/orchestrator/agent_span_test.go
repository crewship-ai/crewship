package orchestrator

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveSpanKind(t *testing.T) {
	cases := map[string]string{
		"Bash":                                 "bash",
		"Write":                                "write",
		"Edit":                                 "edit",
		"MultiEdit":                            "edit",
		"Read":                                 "read",
		"Grep":                                 "read",
		"Glob":                                 "read",
		"WebFetch":                             "http",
		"WebSearch":                            "http",
		"mcp__crewship-routines__save_routine": "mcp_tool",
		"SomethingUnknown":                     "tool",
	}
	for tool, want := range cases {
		if got := DeriveSpanKind(tool); got != want {
			t.Errorf("DeriveSpanKind(%q) = %q, want %q", tool, got, want)
		}
	}
}

// makeToolCall / makeToolResult build the AgentEvents the Claude adapter emits
// so the recorder test exercises the exact metadata shape the live parser
// produces (see parseClaudeCodeStreamJSON / emitToolResultBlock).
func makeToolCall(toolID, name string, input map[string]any, ts time.Time) AgentEvent {
	return AgentEvent{
		Type:    "tool_call",
		Content: name,
		Metadata: map[string]interface{}{
			"tool_name": name,
			"tool_id":   toolID,
			"input":     input,
		},
		Timestamp: ts,
	}
}

func makeToolResult(toolUseID string, isError bool, ts time.Time) AgentEvent {
	meta := map[string]interface{}{"tool_use_id": toolUseID}
	if isError {
		meta["is_error"] = true
	}
	return AgentEvent{Type: "tool_result", Content: "ok", Metadata: meta, Timestamp: ts}
}

func TestAgentSpanRecorder_MapsToolsToSpans(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })

	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// A Bash call.
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": "ls -la"}, t0))
	rec.Observe(makeToolResult("t1", false, t0.Add(150*time.Millisecond)))

	// A Write call (artifact_path attribute).
	rec.Observe(makeToolCall("t2", "Write", map[string]any{"file_path": "/output/report.md", "content": "x"}, t0.Add(time.Second)))
	rec.Observe(makeToolResult("t2", false, t0.Add(time.Second+50*time.Millisecond)))

	// An MCP tool call that errors.
	rec.Observe(makeToolCall("t3", "mcp__crewship-routines__save_routine", map[string]any{"slug": "demo"}, t0.Add(2*time.Second)))
	rec.Observe(makeToolResult("t3", true, t0.Add(2*time.Second+10*time.Millisecond)))

	if len(got) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(got))
	}

	// Ordering + identity.
	for i, s := range got {
		if s.RunID != "run_1" || s.StepID != "step_a" {
			t.Errorf("span %d: run/step = %q/%q", i, s.RunID, s.StepID)
		}
		if s.Seq != i {
			t.Errorf("span %d: seq = %d, want %d", i, s.Seq, i)
		}
	}

	// Bash span.
	if got[0].Kind != "bash" || got[0].Name != "Bash" || got[0].Detail != "ls -la" {
		t.Errorf("bash span = %+v", got[0])
	}
	if got[0].DurationMs != 150 {
		t.Errorf("bash duration = %d, want 150", got[0].DurationMs)
	}
	if got[0].Status != "ok" {
		t.Errorf("bash status = %q, want ok", got[0].Status)
	}

	// Write span carries artifact_path.
	if got[1].Kind != "write" {
		t.Errorf("write kind = %q", got[1].Kind)
	}
	if got[1].Attributes["artifact_path"] != "/output/report.md" {
		t.Errorf("write artifact_path = %q", got[1].Attributes["artifact_path"])
	}

	// MCP span: kind=mcp_tool, short name, error status.
	if got[2].Kind != "mcp_tool" || got[2].Name != "save_routine" {
		t.Errorf("mcp span kind/name = %q/%q", got[2].Kind, got[2].Name)
	}
	if got[2].Status != "error" {
		t.Errorf("mcp span status = %q, want error", got[2].Status)
	}
	if got[2].Attributes["tool"] != "mcp__crewship-routines__save_routine" {
		t.Errorf("mcp span tool attr = %q", got[2].Attributes["tool"])
	}
}

func TestAgentSpanRecorder_CapturesModelFromSystemInit(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	rec.Observe(AgentEvent{Type: "system", Content: "init", Metadata: map[string]interface{}{"model": "claude-opus-4-8"}, Timestamp: t0})
	rec.Observe(makeToolCall("t1", "Bash", map[string]any{"command": "echo hi"}, t0))
	rec.Observe(makeToolResult("t1", false, t0.Add(time.Millisecond)))

	if len(got) != 1 {
		t.Fatalf("expected 1 span, got %d", len(got))
	}
	if got[0].Attributes["model"] != "claude-opus-4-8" {
		t.Errorf("model attr = %q, want claude-opus-4-8", got[0].Attributes["model"])
	}
}

func TestAgentSpanRecorder_PerStepCapAndDetailTruncation(t *testing.T) {
	var got []RunAgentSpan
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { got = append(got, s) })
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	// Detail truncation: a command longer than the cap.
	longCmd := strings.Repeat("a", RunAgentSpanDetailMaxBytes+500)
	rec.Observe(makeToolCall("big", "Bash", map[string]any{"command": longCmd}, t0))
	rec.Observe(makeToolResult("big", false, t0.Add(time.Millisecond)))
	if len(got[0].Detail) > RunAgentSpanDetailMaxBytes+len("...(truncated)") {
		t.Errorf("detail not truncated: len=%d", len(got[0].Detail))
	}
	if !strings.HasSuffix(got[0].Detail, "...(truncated)") {
		t.Errorf("detail missing truncation marker: %q", got[0].Detail[len(got[0].Detail)-20:])
	}
	if rec.Truncated() != 1 {
		t.Errorf("Truncated() = %d, want 1", rec.Truncated())
	}

	// Per-step cap: feed far more than the cap; only cap spans are sunk.
	for i := 0; i < RunAgentSpanMaxPerStep+50; i++ {
		id := "x" + strings.Repeat("y", i%5) + time.Duration(i).String()
		rec.Observe(makeToolCall(id, "Read", map[string]any{"file_path": "/f"}, t0))
		rec.Observe(makeToolResult(id, false, t0.Add(time.Millisecond)))
	}
	if len(got) != RunAgentSpanMaxPerStep {
		t.Errorf("sunk %d spans, want cap %d", len(got), RunAgentSpanMaxPerStep)
	}
	if rec.Dropped() == 0 {
		t.Errorf("expected Dropped() > 0 after exceeding cap")
	}
}

func TestAgentSpanRecorder_NoToolCallsNoSpans(t *testing.T) {
	called := false
	rec := NewAgentSpanRecorder("run_1", "step_a", func(s RunAgentSpan) { called = true })
	t0 := time.Now()
	// Only text/thinking/result events — no tool_use pairs.
	rec.Observe(AgentEvent{Type: "text", Content: "hello", Timestamp: t0})
	rec.Observe(AgentEvent{Type: "thinking", Content: "...", Timestamp: t0})
	rec.Observe(AgentEvent{Type: "result", Content: "done", Metadata: map[string]interface{}{"total_cost_usd": 0.01}, Timestamp: t0})
	// An unmatched tool_result (no preceding tool_call) must not produce a span.
	rec.Observe(makeToolResult("orphan", false, t0))
	if called {
		t.Errorf("sink invoked with no completed tool_use pairs")
	}
}
