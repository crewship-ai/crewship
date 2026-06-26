package orchestrator

import (
	"testing"
)

// TestParseClaude_SystemInitWithPlugins pins that v2.1.111+ plugins +
// plugin_errors fields round-trip into journal metadata. Pre-fix struct
// dropped them; operators wouldn't see plugin load failures.
func TestParseClaude_SystemInitWithPlugins(t *testing.T) {
	line := []byte(`{"type":"system","subtype":"init","model":"claude-sonnet-4-6","plugins":[{"name":"linear","version":"1.0"}],"plugin_errors":[{"name":"broken","error":"manifest missing"}]}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("system init wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["model"] != "claude-sonnet-4-6" {
		t.Errorf("model lost: %v", meta["model"])
	}
	if meta["plugins"] == nil {
		t.Errorf("plugins field dropped — operators won't see what loaded")
	}
	if meta["plugin_errors"] == nil {
		t.Errorf("plugin_errors field dropped — operators won't see load failures")
	}
}

// TestParseClaude_ApiRetryEvent pins v2.1.x system/api_retry envelope. Pre-fix
// parser dropped these to default branch (silent fallthrough); Crow's Nest
// had no visibility into Anthropic backoff.
func TestParseClaude_ApiRetryEvent(t *testing.T) {
	line := []byte(`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":5,"retry_delay_ms":1500,"error_status":529,"error":"overloaded"}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("api_retry event wrong: %+v", got)
	}
	if got[0].Content != "api_retry" {
		t.Errorf("subtype lost: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if attempt, ok := meta["attempt"].(int); !ok || attempt != 2 {
		t.Errorf("attempt lost: %v (type %T)", meta["attempt"], meta["attempt"])
	}
	if maxR, ok := meta["max_retries"].(int); !ok || maxR != 5 {
		t.Errorf("max_retries lost: %v", meta["max_retries"])
	}
	if delay, ok := meta["retry_delay_ms"].(float64); !ok || delay != 1500 {
		t.Errorf("retry_delay_ms lost: %v", meta["retry_delay_ms"])
	}
	if status, ok := meta["error_status"].(int); !ok || status != 529 {
		t.Errorf("error_status lost: %v", meta["error_status"])
	}
	if meta["error"] != "overloaded" {
		t.Errorf("error message lost: %v", meta["error"])
	}
}

// TestParseClaude_StreamEventTextDelta pins token-level streaming.
func TestParseClaude_StreamEventTextDelta(t *testing.T) {
	line := []byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello "}}}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello " {
		t.Errorf("text_delta wrong: %+v", got)
	}
}

// TestParseClaude_StreamEventThinkingDelta — separate event type (chain of
// thought), routes to "thinking" with streaming flag.
func TestParseClaude_StreamEventThinkingDelta(t *testing.T) {
	line := []byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"considering..."}}}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "thinking" || got[0].Content != "considering..." {
		t.Errorf("thinking_delta wrong: %+v", got)
	}
}

// TestParseClaude_AssistantToolUse — content blocks for tool calls (text was
// already streamed via deltas, so assistant text should be skipped here).
func TestParseClaude_AssistantToolUse(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"already-streamed"},{"type":"tool_use","id":"tu-1","name":"read","input":{"path":"main.go"}}]}}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	// Should emit ONLY the tool_use, not the text (text was already delta-streamed).
	if len(got) != 1 || got[0].Type != "tool_call" || got[0].Content != "read" {
		t.Fatalf("expected only tool_call event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_id"] != "tu-1" {
		t.Errorf("tool_id lost: %v", meta["tool_id"])
	}
}

// TestParseClaude_ResultEnvelope — terminal usage + cost.
func TestParseClaude_ResultEnvelope(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","duration_ms":1234,"duration_api_ms":987,"total_cost_usd":0.0042,"num_turns":3,"is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("result wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if cost, ok := meta["total_cost_usd"].(float64); !ok || cost != 0.0042 {
		t.Errorf("cost lost: %v", meta["total_cost_usd"])
	}
	usage, ok := meta["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage dropped: %v", meta["usage"])
	}
	if usage["input_tokens"].(float64) != 100 {
		t.Errorf("input_tokens lost: %v", usage["input_tokens"])
	}
}

// TestParseClaude_NotJSON falls through to text fallback.
func TestParseClaude_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseClaudeCodeStreamJSON([]byte("not json line"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Errorf("want text fallback, got %+v", got)
	}
}

// TestParseClaude_NilHandler must not panic.
func TestParseClaude_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseClaudeCodeStreamJSON([]byte(`{"type":"system","subtype":"init"}`), nil)
}

// TestParseClaude_SubagentParentToolUseID — the adapter tags every line from a
// nested subagent (parent_tool_use_id present) so the UI can scope subagent
// thinking / tool activity under its parent instead of flattening it into the
// main stream.
func TestParseClaude_SubagentParentToolUseID(t *testing.T) {
	// subagent thinking delta
	line := []byte(`{"type":"stream_event","parent_tool_use_id":"toolu_parent","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"sub thinking"}}}`)
	var got []AgentEvent
	parseClaudeCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })
	if len(got) != 1 || got[0].Type != "thinking" {
		t.Fatalf("expected thinking event, got %+v", got)
	}
	meta, _ := got[0].Metadata.(map[string]interface{})
	if meta == nil || meta["parent_tool_use_id"] != "toolu_parent" || meta["subagent"] != true {
		t.Errorf("expected subagent metadata on thinking, got %+v", got[0].Metadata)
	}

	// subagent tool call
	line2 := []byte(`{"type":"assistant","parent_tool_use_id":"toolu_parent","message":{"content":[{"type":"tool_use","id":"tu-2","name":"Bash","input":{"command":"ls"}}]}}`)
	var got2 []AgentEvent
	parseClaudeCodeStreamJSON(line2, func(e AgentEvent) { got2 = append(got2, e) })
	if len(got2) != 1 || got2[0].Type != "tool_call" {
		t.Fatalf("expected tool_call event, got %+v", got2)
	}
	meta2, _ := got2[0].Metadata.(map[string]interface{})
	if meta2["parent_tool_use_id"] != "toolu_parent" || meta2["subagent"] != true {
		t.Errorf("expected subagent metadata on tool_call, got %+v", got2[0].Metadata)
	}

	// top-level (no parent) event must NOT carry subagent metadata
	line3 := []byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}`)
	var got3 []AgentEvent
	parseClaudeCodeStreamJSON(line3, func(e AgentEvent) { got3 = append(got3, e) })
	if len(got3) != 1 {
		t.Fatalf("expected 1 top-level event, got %d", len(got3))
	}
	if m, _ := got3[0].Metadata.(map[string]interface{}); m != nil && m["subagent"] == true {
		t.Errorf("top-level event should not be tagged subagent, got %+v", got3[0].Metadata)
	}
}
