package orchestrator

import (
	"testing"
)

// TestParseCodex_SessionStart pins the bootstrap event. Codex Rust port emits
// session.started (with .start as a documented alias) when --json is on.
func TestParseCodex_SessionStart(t *testing.T) {
	line := []byte(`{"type":"session.started","model":"gpt-5","id":"sess-1"}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("want system event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["model"] != "gpt-5" {
		t.Errorf("model lost: %v", meta["model"])
	}
}

// TestParseCodex_TextDelta covers streaming token delta events. Codex emits
// agent.message.delta (or "delta" alias) with the text in either Delta or
// Text field; parser favours Delta.
func TestParseCodex_TextDelta(t *testing.T) {
	for _, typ := range []string{"agent.message.delta", "delta", "message.delta"} {
		line := []byte(`{"type":"` + typ + `","delta":"hello"}`)
		var got []AgentEvent
		parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

		if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello" {
			t.Errorf("type=%s want text 'hello', got %+v", typ, got)
		}
	}
}

// TestParseCodex_FullMessage covers the non-streaming path where the model
// returns one full assistant message instead of deltas.
func TestParseCodex_FullMessage(t *testing.T) {
	line := []byte(`{"type":"agent.message","text":"the answer is 42"}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "the answer is 42" {
		t.Errorf("want full message text, got %+v", got)
	}
}

// TestParseCodex_ToolCall pins the tool invocation shape. Codex names the
// arguments field "arguments" (OpenAI convention) — the parser must lift it
// into metadata["input"] so the chat UI can render it.
func TestParseCodex_ToolCall(t *testing.T) {
	line := []byte(`{"type":"tool.call","name":"read_file","id":"tc-1","arguments":{"path":"main.go"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_call" || got[0].Content != "read_file" {
		t.Fatalf("want tool_call read_file, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_id"] != "tc-1" {
		t.Errorf("tool_id lost: %v", meta["tool_id"])
	}
	input, ok := meta["input"].(map[string]interface{})
	if !ok {
		t.Fatalf("input not a map: %T", meta["input"])
	}
	if input["path"] != "main.go" {
		t.Errorf("input.path lost: %v", input["path"])
	}
}

// TestParseCodex_Result pins the terminal envelope. Usage shape is OpenAI's
// chat completion convention: input_tokens / output_tokens / total_tokens.
func TestParseCodex_Result(t *testing.T) {
	line := []byte(`{"type":"session.ended","usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("want result event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	usage, ok := meta["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage not a map: %T", meta["usage"])
	}
	if usage["input_tokens"].(float64) != 100 {
		t.Errorf("input_tokens lost: %v", usage["input_tokens"])
	}
}

// TestParseCodex_Error covers recoverable errors.
func TestParseCodex_Error(t *testing.T) {
	line := []byte(`{"type":"error","error":"rate limit exceeded"}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" || got[0].Content != "rate limit exceeded" {
		t.Errorf("error event wrong: %+v", got)
	}
}

// TestParseCodex_NilHandler must not panic.
func TestParseCodex_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseCodexStreamJSON([]byte(`{"type":"agent.message","text":"x"}`), nil)
}

// TestParseCodex_NotJSON falls through to text so non-JSON debug noise still
// surfaces.
func TestParseCodex_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseCodexStreamJSON([]byte("not json"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Errorf("want text fallback, got %+v", got)
	}
}
