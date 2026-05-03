package orchestrator

import (
	"testing"
)

// TestParseGemini_Init covers the session bootstrap event documented in
// geminicli.com/docs/cli/headless/.
func TestParseGemini_Init(t *testing.T) {
	line := []byte(`{"type":"init","model":"gemini-2.5-pro","session_id":"g-1"}`)
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

// TestParseGemini_Message covers assistant text deltas.
func TestParseGemini_Message(t *testing.T) {
	// Schema favours .text but accepts .content as fallback.
	for _, payload := range []string{
		`{"type":"message","text":"hello"}`,
		`{"type":"message","content":"hello"}`,
	} {
		var got []AgentEvent
		parseGeminiStreamJSON([]byte(payload), func(e AgentEvent) { got = append(got, e) })

		if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello" {
			t.Errorf("payload %s want text 'hello', got %+v", payload, got)
		}
	}
}

// TestParseGemini_ToolUse pins the tool invocation shape — name/id/input.
func TestParseGemini_ToolUse(t *testing.T) {
	line := []byte(`{"type":"tool_use","name":"web_search","id":"tu-1","input":{"query":"crewship"}}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_call" || got[0].Content != "web_search" {
		t.Fatalf("want tool_call web_search, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	input, ok := meta["input"].(map[string]interface{})
	if !ok || input["query"] != "crewship" {
		t.Errorf("input.query lost: %v", meta["input"])
	}
}

// TestParseGemini_ToolResult pins how tool responses surface.
func TestParseGemini_ToolResult(t *testing.T) {
	line := []byte(`{"type":"tool_result","tool_use_id":"tu-1","output":"123 results"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_result" || got[0].Content != "123 results" {
		t.Errorf("tool_result wrong: %+v", got)
	}
}

// TestParseGemini_Result pins the terminal envelope (response + stats).
// stats schema follows Vertex/AI-Studio convention: totalTokens etc.
func TestParseGemini_Result(t *testing.T) {
	line := []byte(`{"type":"result","response":"42","stats":{"totalTokens":150,"inputTokens":100,"outputTokens":50}}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" || got[0].Content != "42" {
		t.Fatalf("want result event with response, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	stats, ok := meta["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats not a map: %T", meta["stats"])
	}
	if stats["totalTokens"].(float64) != 150 {
		t.Errorf("totalTokens lost: %v", stats["totalTokens"])
	}
}

// TestParseGemini_Error covers the documented error event.
func TestParseGemini_Error(t *testing.T) {
	line := []byte(`{"type":"error","error":"quota exhausted"}`)
	var got []AgentEvent
	parseGeminiStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" || got[0].Content != "quota exhausted" {
		t.Errorf("error event wrong: %+v", got)
	}
}

// TestParseGemini_NilHandler must not panic.
func TestParseGemini_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseGeminiStreamJSON([]byte(`{"type":"message","text":"x"}`), nil)
}
