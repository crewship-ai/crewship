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
// is_error / session_id all need to round-trip into metadata so Paymaster
// can read them.
func TestParseCursor_Result(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","duration_ms":1234,"duration_api_ms":987,"is_error":false,"result":"done","session_id":"s-1"}`)
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
