package orchestrator

import (
	"testing"
)

// TestParseDroid_SystemInit pins the bootstrap event documented at
// docs.factory.ai/cli/droid-exec/overview.
func TestParseDroid_SystemInit(t *testing.T) {
	line := []byte(`{"type":"system","subtype":"init","cwd":"/work","session_id":"d-1","model":"claude-sonnet-4-6","tools":["bash","read"]}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("system init wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["model"] != "claude-sonnet-4-6" {
		t.Errorf("model lost: %v", meta["model"])
	}
	if meta["cwd"] != "/work" {
		t.Errorf("cwd lost: %v", meta["cwd"])
	}
}

// TestParseDroid_Message — assistant text event.
func TestParseDroid_Message(t *testing.T) {
	line := []byte(`{"type":"message","role":"assistant","id":"msg-1","text":"hello world","session_id":"d-1"}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello world" {
		t.Errorf("message event wrong: %+v", got)
	}
}

// TestParseDroid_ToolCallCamelCase — pins toolName + parameters camelCase
// fields. The pre-fix parser read name + input and produced empty tool_call
// events for every Droid tool invocation.
func TestParseDroid_ToolCallCamelCase(t *testing.T) {
	line := []byte(`{"type":"tool_call","id":"tc-1","messageId":"msg-1","toolId":"t-1","toolName":"shell","parameters":{"cmd":"ls"},"session_id":"d-1"}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_call" {
		t.Fatalf("tool_call wrong: %+v", got)
	}
	if got[0].Content != "shell" {
		t.Errorf("toolName lost — parser may be reading 'name' instead of 'toolName': %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_id"] != "t-1" {
		t.Errorf("toolId lost: %v", meta["tool_id"])
	}
	input := meta["input"].(map[string]interface{})
	if input["cmd"] != "ls" {
		t.Errorf("parameters → input mapping lost: %v", meta["input"])
	}
}

// TestParseDroid_ToolResultValue — pins value field (NOT output), isError
// camelCase. Pre-fix parser silently dropped tool output by reading item.Output.
func TestParseDroid_ToolResultValue(t *testing.T) {
	line := []byte(`{"type":"tool_result","id":"tr-1","messageId":"msg-1","toolId":"t-1","isError":false,"value":"file.txt","session_id":"d-1"}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_result" {
		t.Fatalf("tool_result wrong: %+v", got)
	}
	if got[0].Content != "file.txt" {
		t.Errorf("value field lost — pre-fix parser used 'output' which produced empty results: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["tool_use_id"] != "t-1" {
		t.Errorf("toolId → tool_use_id mapping lost: %v", meta["tool_use_id"])
	}
}

// TestParseDroid_Completion — camelCase finalText/numTurns/durationMs.
func TestParseDroid_Completion(t *testing.T) {
	line := []byte(`{"type":"completion","finalText":"all done","numTurns":3,"durationMs":4500.5,"session_id":"d-1"}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("completion wrong: %+v", got)
	}
	if got[0].Content != "all done" {
		t.Errorf("finalText lost: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if turns, ok := meta["num_turns"].(int); !ok || turns != 3 {
		t.Errorf("numTurns lost: %v (type %T)", meta["num_turns"], meta["num_turns"])
	}
	if dur, ok := meta["duration_ms"].(float64); !ok || dur != 4500.5 {
		t.Errorf("durationMs lost: %v", meta["duration_ms"])
	}
}

// TestParseDroid_ResultSnakeCase — different convention from completion.
func TestParseDroid_ResultSnakeCase(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,"num_turns":2,"result":"finished","session_id":"d-1"}`)
	var got []AgentEvent
	parseDroidStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("result wrong: %+v", got)
	}
	if got[0].Content != "finished" {
		t.Errorf("result text lost: %q", got[0].Content)
	}
}

// TestParseDroid_NotJSON falls through to text.
func TestParseDroid_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseDroidStreamJSON([]byte("not json"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Errorf("want text fallback, got %+v", got)
	}
}

// TestParseDroid_NilHandler must not panic.
func TestParseDroid_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseDroidStreamJSON([]byte(`{"type":"message","text":"x"}`), nil)
}
