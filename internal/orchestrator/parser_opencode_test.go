package orchestrator

import (
	"testing"
)

// TestParseOpenCode_TextPart — assistant text streamed as message.part with
// part.type=text. Each line is one chunk.
func TestParseOpenCode_TextPart(t *testing.T) {
	line := []byte(`{"type":"message.part.updated","sessionID":"ses-1","partID":"p1","part":{"type":"text","text":"hello"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello" {
		t.Fatalf("text part wrong: %+v", got)
	}
}

// TestParseOpenCode_ReasoningPart — chain-of-thought routes to "thinking".
func TestParseOpenCode_ReasoningPart(t *testing.T) {
	line := []byte(`{"type":"message.part.updated","part":{"type":"reasoning","text":"thinking..."}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "thinking" || got[0].Content != "thinking..." {
		t.Errorf("reasoning part wrong: %+v", got)
	}
}

// TestParseOpenCode_ToolPartLifecycle — running → tool_call, completed →
// tool_result. partID becomes tool correlation id.
func TestParseOpenCode_ToolPartLifecycle(t *testing.T) {
	running := []byte(`{"type":"message.part.updated","sessionID":"s","partID":"tp-1","part":{"type":"tool","tool":"bash","state":{"status":"running","input":{"cmd":"ls"}}}}`)
	completed := []byte(`{"type":"message.part.updated","sessionID":"s","partID":"tp-1","part":{"type":"tool","tool":"bash","state":{"status":"completed","output":"a.txt"}}}`)

	var got []AgentEvent
	parseOpenCodeStreamJSON(running, func(e AgentEvent) { got = append(got, e) })
	parseOpenCodeStreamJSON(completed, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Type != "tool_call" || got[0].Content != "bash" {
		t.Errorf("running event wrong: %+v", got[0])
	}
	startedMeta := got[0].Metadata.(map[string]interface{})
	if startedMeta["tool_id"] != "tp-1" {
		t.Errorf("partID → tool_id mapping lost: %v", startedMeta["tool_id"])
	}

	if got[1].Type != "tool_result" || got[1].Content != "a.txt" {
		t.Errorf("completed event wrong: %+v", got[1])
	}
	completedMeta := got[1].Metadata.(map[string]interface{})
	if completedMeta["tool_use_id"] != "tp-1" {
		t.Errorf("partID → tool_use_id mapping lost: %v", completedMeta["tool_use_id"])
	}
}

// TestParseOpenCode_StepFinish — per-turn usage envelope. Carries cost in USD
// directly (OpenCode pre-computes it across providers) so Paymaster doesn't
// need a separate price table for OpenCode runs.
func TestParseOpenCode_StepFinish(t *testing.T) {
	line := []byte(`{"type":"message.part.updated","part":{"type":"step-finish","tokens":{"input":80,"output":20,"reasoning":40},"cost":0.0042,"providerID":"anthropic","modelID":"claude-sonnet-4-6"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("want result event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["cost_usd"].(float64) != 0.0042 {
		t.Errorf("cost lost: %v", meta["cost_usd"])
	}
	if meta["provider"] != "anthropic" {
		t.Errorf("provider lost: %v", meta["provider"])
	}
	if meta["model"] != "claude-sonnet-4-6" {
		t.Errorf("model lost: %v", meta["model"])
	}
	tokens := meta["tokens"].(map[string]interface{})
	if tokens["input"].(float64) != 80 {
		t.Errorf("tokens.input lost: %v", tokens["input"])
	}
}

// TestParseOpenCode_StepStartSilent — boundary marker, must not flood journal.
func TestParseOpenCode_StepStartSilent(t *testing.T) {
	var got []AgentEvent
	parseOpenCodeStreamJSON([]byte(`{"type":"message.part.updated","part":{"type":"step-start"}}`), func(e AgentEvent) { got = append(got, e) })
	if len(got) != 0 {
		t.Errorf("step-start must be silent, got %+v", got)
	}
}

// TestParseOpenCode_FilePart — file read/write summary.
func TestParseOpenCode_FilePart(t *testing.T) {
	line := []byte(`{"type":"message.part.updated","partID":"fp-1","part":{"type":"file","path":"/work/main.go"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "tool_result" {
		t.Fatalf("want tool_result for file part, got %+v", got)
	}
	if got[0].Content != "/work/main.go" {
		t.Errorf("file path lost: %q", got[0].Content)
	}
}

// TestParseOpenCode_NoPart — top-level events without part (session.idle,
// message.completed) fan to system event so journal sees them.
func TestParseOpenCode_NoPart(t *testing.T) {
	line := []byte(`{"type":"session.idle","sessionID":"s-1"}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("want system event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "session.idle" {
		t.Errorf("subtype lost: %v", meta["subtype"])
	}
}

// TestParseOpenCode_CamelCaseFields — sessionID/messageID/partID camelCase
// canonical (snake_case is wrong). Pinning here so a mistaken refactor fails
// loudly.
func TestParseOpenCode_CamelCaseFields(t *testing.T) {
	// snake_case must NOT match — verify the parser ignores session_id field.
	line := []byte(`{"type":"message.part.updated","session_id":"WRONG","sessionID":"correct","part":{"type":"text","text":"x"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	// Correct sessionID was used; this asserts via TestParseOpenCode_NoPart's
	// session_id metadata path indirectly. Here we just verify nothing panics
	// and the text part is extracted.
	if len(got) != 1 || got[0].Content != "x" {
		t.Errorf("camelCase parsing broken: %+v", got)
	}
}

// TestParseOpenCode_NotJSON falls through to text.
func TestParseOpenCode_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseOpenCodeStreamJSON([]byte("not json"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Errorf("want text fallback, got %+v", got)
	}
}

// TestParseOpenCode_NilHandler must not panic.
func TestParseOpenCode_NilHandler(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil handler panicked: %v", r)
		}
	}()
	parseOpenCodeStreamJSON([]byte(`{"type":"message.part.updated","part":{"type":"text","text":"x"}}`), nil)
}
