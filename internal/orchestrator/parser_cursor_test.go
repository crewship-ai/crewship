package orchestrator

import (
	"testing"
)

// TestParseCursor_System pins the Cursor stream-json `system/init` shape
// documented at cursor.com/docs/cli/reference/output-format. If Cursor renames
// the type discriminator, our system-event panel will stop populating model /
// session info — this test catches that on the next dependency bump.
func TestParseCursor_System(t *testing.T) {
	line := []byte(`{"type":"system","subtype":"init","apiKeySource":"env","cwd":"/work","session_id":"s-1","model":"claude-sonnet-4-6","permissionMode":"default"}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Type != "system" {
		t.Errorf("want system, got %s", got[0].Type)
	}
	meta, ok := got[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("metadata not a map: %T", got[0].Metadata)
	}
	if meta["model"] != "claude-sonnet-4-6" {
		t.Errorf("model field lost: %v", meta["model"])
	}
	if meta["session_id"] != "s-1" {
		t.Errorf("session_id field lost: %v", meta["session_id"])
	}
}

// TestParseCursor_Assistant verifies that text content blocks are emitted as
// "text" events. Cursor wraps assistant content in message.content[] with
// {type:text,text:...} blocks, identical to Claude Code.
func TestParseCursor_Assistant(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]},"session_id":"s-1"}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello world" {
		t.Errorf("want text 'hello world', got %+v", got)
	}
}

// TestParseCursor_AssistantPartialDeltas covers the --stream-partial-output
// case where multiple assistant events arrive with incremental text — each
// should fan out as its own "text" event.
func TestParseCursor_AssistantPartialDeltas(t *testing.T) {
	deltas := [][]byte{
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hel"}]}}`),
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"lo "}]}}`),
		[]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"world"}]}}`),
	}
	var got []AgentEvent
	for _, d := range deltas {
		parseCursorStreamJSON(d, func(e AgentEvent) { got = append(got, e) })
	}
	if len(got) != 3 {
		t.Fatalf("want 3 deltas, got %d", len(got))
	}
	if got[0].Content+got[1].Content+got[2].Content != "hello world" {
		t.Errorf("delta concatenation wrong: %q + %q + %q", got[0].Content, got[1].Content, got[2].Content)
	}
}

// TestParseCursor_ToolCall covers tool_call started/completed lifecycle.
func TestParseCursor_ToolCall(t *testing.T) {
	started := []byte(`{"type":"tool_call","subtype":"started","tool_call":{"name":"read_file","args":{"path":"main.go"}}}`)
	completed := []byte(`{"type":"tool_call","subtype":"completed","tool_call":{"name":"read_file","output":"file contents"}}`)

	var got []AgentEvent
	parseCursorStreamJSON(started, func(e AgentEvent) { got = append(got, e) })
	parseCursorStreamJSON(completed, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	if got[0].Type != "tool_call" || got[0].Content != "started" {
		t.Errorf("started event wrong: %+v", got[0])
	}
	if got[1].Type != "tool_result" || got[1].Content != "completed" {
		t.Errorf("completed event wrong: %+v", got[1])
	}
}

// TestParseCursor_Result pins the terminal result envelope. duration_ms /
// is_error / session_id / request_id all need to round-trip into metadata.
// request_id specifically is what Cursor support asks for when debugging — if
// we drop it, users cannot file actionable tickets.
func TestParseCursor_Result(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","duration_ms":1234,"duration_api_ms":987,"is_error":false,"result":"done","session_id":"s-1","request_id":"req-abc"}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("want 1 result event, got %+v", got)
	}
	if got[0].Content != "done" {
		t.Errorf("result content lost: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["duration_ms"].(float64) != 1234 {
		t.Errorf("duration_ms lost: %v", meta["duration_ms"])
	}
	if meta["is_error"].(bool) != false {
		t.Errorf("is_error lost: %v", meta["is_error"])
	}
	if meta["request_id"] != "req-abc" {
		t.Errorf("request_id lost — users cannot file Cursor support tickets without it: %v", meta["request_id"])
	}
}

// TestParseCursor_ToolCallLiftsCallID — call_id is lifted to tool_use_id so
// cross-CLI correlation in Crow's Nest can use one key everywhere. Pre-fix
// behaviour kept call_id only inside the raw tool_call blob (preserved by the
// JSON unmarshal but never lifted to the canonical metadata key).
func TestParseCursor_ToolCallLiftsCallID(t *testing.T) {
	line := []byte(`{"type":"tool_call","subtype":"started","call_id":"tc-123","tool_call":{"readToolCall":{"path":"main.go"}}}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(got), got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_use_id"] != "tc-123" {
		t.Errorf("call_id not lifted to tool_use_id (cross-CLI canonical key): %v", meta["tool_use_id"])
	}
	if meta["call_id"] != "tc-123" {
		t.Errorf("call_id key not preserved for back-compat: %v", meta["call_id"])
	}
}

// TestParseCursor_ResultWithUsage — Feb 2026 forum #146980 added usage block
// to result events; Paymaster reads it.
func TestParseCursor_ResultWithUsage(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","duration_ms":1234,"is_error":false,"result":"done","usage":{"input_tokens":100,"output_tokens":50}}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("result event wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	usage, ok := meta["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage block lost — Paymaster will undercount: %v", meta["usage"])
	}
	if usage["input_tokens"].(float64) != 100 {
		t.Errorf("usage.input_tokens lost: %v", usage["input_tokens"])
	}
}

// TestParseCursor_MCPToolCall — stub for the regression-paused mcpToolCall
// event type (forum #158988). Currently unreachable in production output but
// the case is in place so the moment Cursor restores it nothing else needs
// changing.
func TestParseCursor_MCPToolCall(t *testing.T) {
	line := []byte(`{"type":"mcpToolCall","providerIdentifier":"linear","toolName":"create_issue","arguments":{"title":"bug"}}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_call" || got[0].Content != "create_issue" {
		t.Fatalf("mcpToolCall event wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["transport"] != "mcp" {
		t.Errorf("mcp transport tag lost")
	}
	if meta["provider_identifier"] != "linear" {
		t.Errorf("provider_identifier lost: %v", meta["provider_identifier"])
	}
}

// TestParseCursor_AssistantWithModelCallID verifies that streaming assistant
// deltas carry model_call_id + timestamp_ms metadata so the chat-bridge can
// dedup duplicates after a connection reconnect (Cursor forum #157593).
func TestParseCursor_AssistantWithModelCallID(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]},"model_call_id":"mc-1","timestamp_ms":1700000000000}`)
	var got []AgentEvent
	parseCursorStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hi" {
		t.Fatalf("want text 'hi', got %+v", got)
	}
	meta, ok := got[0].Metadata.(map[string]interface{})
	if !ok {
		t.Fatalf("metadata not attached to streaming delta — chat-bridge cannot dedup")
	}
	if meta["model_call_id"] != "mc-1" {
		t.Errorf("model_call_id lost: %v", meta["model_call_id"])
	}
	if meta["timestamp_ms"].(float64) != 1700000000000 {
		t.Errorf("timestamp_ms lost: %v", meta["timestamp_ms"])
	}
}

// TestParseCursor_NotJSON ensures a non-JSON line falls through to a text
// event so debug output (when Cursor adds non-JSON warnings) still surfaces.
func TestParseCursor_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseCursorStreamJSON([]byte("WARNING: rate limit"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Fatalf("want text fallback, got %+v", got)
	}
}

// TestParseCursor_NilHandler must not panic — streamOutput passes nil when
// the agent run is metrics-only.
func TestParseCursor_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseCursorStreamJSON([]byte(`{"type":"system"}`), nil)
}
