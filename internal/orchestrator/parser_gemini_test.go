package orchestrator

import (
	"testing"
)

// TestParseGemini_Init pins the bootstrap event.
func TestParseGemini_Init(t *testing.T) {
	line := []byte(`{"type":"init","model":"gemini-2.5-pro","session_id":"g-1","timestamp":"2026-05-03T15:00:00Z"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("want system event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["model"] != "gemini-2.5-pro" {
		t.Errorf("model lost: %v", meta["model"])
	}
}

// TestParseGemini_MessageDelta — PR #10883 added `delta` for streaming. If the
// parser doesn't read it, streamed text disappears silently. This test pins
// that streamed deltas reach the chat as text events.
func TestParseGemini_MessageDelta(t *testing.T) {
	line := []byte(`{"type":"message","role":"assistant","delta":"hel"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hel" {
		t.Errorf("delta dropped — streaming would silently fail. got %+v", got)
	}
}

// TestParseGemini_MessageContent — non-streaming path: `content` field carries
// the full message body.
func TestParseGemini_MessageContent(t *testing.T) {
	line := []byte(`{"type":"message","role":"assistant","content":"hello"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello" {
		t.Errorf("content path wrong: %+v", got)
	}
}

// TestParseGemini_ToolUseFieldNames pins the canonical PR #10883 field names:
// tool_name (NOT name), tool_id (NOT id), parameters (NOT input). If gemini-
// cli changes any of these, the chat UI will display tool_call events with
// blank names — this test catches it.
func TestParseGemini_ToolUseFieldNames(t *testing.T) {
	line := []byte(`{"type":"tool_use","tool_name":"web_search","tool_id":"tu-1","parameters":{"query":"crewship"}}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_call" {
		t.Fatalf("want tool_call event, got %+v", got)
	}
	if got[0].Content != "web_search" {
		t.Errorf("tool_name lost (parser may be reading 'name' instead of 'tool_name'): %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_id"] != "tu-1" {
		t.Errorf("tool_id lost (parser may be reading 'id'): %v", meta["tool_id"])
	}
	input := meta["input"].(map[string]interface{})
	if input["query"] != "crewship" {
		t.Errorf("parameters→input mapping lost: %v", meta["input"])
	}
}

// TestParseGemini_ToolResult — pins tool_id (NOT tool_use_id) and the new
// status field.
func TestParseGemini_ToolResult(t *testing.T) {
	line := []byte(`{"type":"tool_result","tool_id":"tu-1","status":"success","output":"5 results"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_result" || got[0].Content != "5 results" {
		t.Fatalf("tool_result wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_use_id"] != "tu-1" {
		t.Errorf("tool_id → tool_use_id remap lost: %v", meta["tool_use_id"])
	}
	if meta["status"] != "success" {
		t.Errorf("status field lost: %v", meta["status"])
	}
}

// TestParseGemini_Result pins terminal envelope with stats.
func TestParseGemini_Result(t *testing.T) {
	line := []byte(`{"type":"result","status":"success","response":"42","stats":{"total_tokens":150,"input_tokens":100,"output_tokens":50,"duration_ms":1234,"tool_calls":2}}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" || got[0].Content != "42" {
		t.Fatalf("want result, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	stats := meta["stats"].(map[string]interface{})
	for _, k := range []string{"total_tokens", "input_tokens", "output_tokens", "duration_ms", "tool_calls"} {
		if _, ok := stats[k]; !ok {
			t.Errorf("stats.%s missing — Paymaster may undercount", k)
		}
	}
	if meta["is_error"].(bool) {
		t.Errorf("is_error should be false for status=success")
	}
}

// TestParseGemini_ResultErrorStatus — when status is "error", flag the result.
func TestParseGemini_ResultErrorStatus(t *testing.T) {
	line := []byte(`{"type":"result","status":"error","stats":{}}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	meta := got[0].Metadata.(map[string]interface{})
	if !meta["is_error"].(bool) {
		t.Errorf("is_error must be true when status=error")
	}
}

func TestParseGemini_Error(t *testing.T) {
	line := []byte(`{"type":"error","error":"quota exhausted"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" || got[0].Content != "quota exhausted" {
		t.Errorf("error event wrong: %+v", got)
	}
}

// TestParseGemini_ErrorSeverityWarningDemoted pins the PR #26262 contract.
// Pre-fix parser checked msg.Subtype but the JSON field is `severity` —
// the demote was dead code, every warning surfaced as red error. Real
// upstream emits severity:"warning" for AgentExecutionBlocked etc.
func TestParseGemini_ErrorSeverityWarningDemoted(t *testing.T) {
	line := []byte(`{"type":"error","severity":"warning","error":"AgentExecutionBlocked: filter triggered"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Type != "system" {
		t.Errorf("severity:warning must demote to system event (was: %s) — chat UI would render as red error and orchestrator would mark run failed", got[0].Type)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["severity"] != "warning" {
		t.Errorf("severity field lost: %v", meta["severity"])
	}
}

// TestParseGemini_ErrorSeverityErrorStaysError — explicit severity="error"
// (or no severity field) keeps the fatal classification.
func TestParseGemini_ErrorSeverityErrorStaysError(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(`{"type":"error","severity":"error","error":"hard fail"}`),
		[]byte(`{"type":"error","error":"no severity field at all"}`),
	} {
		var got []AgentEvent
		parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })
		if len(got) != 1 || got[0].Type != "error" {
			t.Errorf("input %s: want fatal error, got %+v", line, got)
		}
	}
}

func TestParseGemini_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseGeminiStreamJSON([]byte(`{"type":"message","content":"x"}`), nil)
}
