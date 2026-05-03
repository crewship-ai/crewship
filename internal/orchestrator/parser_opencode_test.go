package orchestrator

import (
	"testing"
)

// TestParseOpenCode_TextEvent — top-level type is the part type itself, NOT
// nested under message.part.updated. This is the key invariant that the
// pre-rewrite parser got wrong.
func TestParseOpenCode_TextEvent(t *testing.T) {
	line := []byte(`{"type":"text","sessionID":"ses-1","part":{"id":"p1","text":"hello"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "hello" {
		t.Fatalf("text event wrong: %+v (parser may still be looking for message.part.updated envelope)", got)
	}
}

// TestParseOpenCode_TextEvent_MissedByOldEnvelopeAssumption regression-pins
// that we don't accept the pre-rewrite envelope shape — that shape never
// existed in real opencode output, and accepting it would mask a future
// schema regression where someone genuinely changes the discriminator.
func TestParseOpenCode_TextEvent_RejectsOldEnvelope(t *testing.T) {
	// The OLD wrong shape: type=message.part.updated, nested part with type=text.
	// Should fall through to the system event branch (forward-compat) since
	// real opencode never emits this discriminator.
	line := []byte(`{"type":"message.part.updated","part":{"type":"text","text":"hello"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	// Old-shape goes to default branch — system event with subtype.
	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("old envelope must NOT be accepted as text event: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "message.part.updated" {
		t.Errorf("forward-compat preservation lost: %v", meta["subtype"])
	}
}

// TestParseOpenCode_Reasoning — chain-of-thought routes to "thinking".
func TestParseOpenCode_Reasoning(t *testing.T) {
	line := []byte(`{"type":"reasoning","sessionID":"ses-1","part":{"id":"p1","text":"weighing options..."}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "thinking" || got[0].Content != "weighing options..." {
		t.Errorf("reasoning event wrong: %+v", got)
	}
}

// TestParseOpenCode_ToolUseLifecycle — tool_use type (NOT "tool"); state.status
// drives running → tool_call vs completed → tool_result; part.id (NOT envelope
// partID) is the correlation key.
func TestParseOpenCode_ToolUseLifecycle(t *testing.T) {
	running := []byte(`{"type":"tool_use","sessionID":"s","part":{"id":"tu-1","tool":"bash","state":{"status":"running","input":{"cmd":"ls"}}}}`)
	completed := []byte(`{"type":"tool_use","sessionID":"s","part":{"id":"tu-1","tool":"bash","state":{"status":"completed","output":"file.txt"}}}`)

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
	if startedMeta["tool_id"] != "tu-1" {
		t.Errorf("part.id → tool_id mapping lost: %v", startedMeta["tool_id"])
	}
	if got[1].Type != "tool_result" || got[1].Content != "file.txt" {
		t.Errorf("completed event wrong: %+v", got[1])
	}
}

// TestParseOpenCode_StepFinish — usage + cost. snake_case "step_finish" (NOT
// hyphenated "step-finish" the pre-rewrite parser assumed).
func TestParseOpenCode_StepFinish(t *testing.T) {
	line := []byte(`{"type":"step_finish","sessionID":"s","part":{"tokens":{"input":80,"output":20},"cost":0.0042,"providerID":"anthropic","modelID":"claude-sonnet-4-6"}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("step_finish wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["cost_usd"].(float64) != 0.0042 {
		t.Errorf("cost lost: %v", meta["cost_usd"])
	}
	if meta["provider"] != "anthropic" {
		t.Errorf("providerID → provider mapping lost: %v", meta["provider"])
	}
}

// TestParseOpenCode_StepStartSilent — boundary marker, must not flood journal.
func TestParseOpenCode_StepStartSilent(t *testing.T) {
	var got []AgentEvent
	parseOpenCodeStreamJSON([]byte(`{"type":"step_start","part":{}}`), func(e AgentEvent) { got = append(got, e) })
	if len(got) != 0 {
		t.Errorf("step_start must be silent, got %+v", got)
	}
}

// TestParseOpenCode_Error — fatal error has `error` string at envelope level
// (NOT inside part).
func TestParseOpenCode_Error(t *testing.T) {
	line := []byte(`{"type":"error","sessionID":"s","error":"missing API key"}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "error" || got[0].Content != "missing API key" {
		t.Errorf("error event wrong: %+v", got)
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
	parseOpenCodeStreamJSON([]byte(`{"type":"text","part":{"text":"x"}}`), nil)
}

// TestParseOpenCode_UnknownType — forward-compat: unknown top-level type
// surfaces as system event so future opencode releases don't silently drop.
func TestParseOpenCode_UnknownType(t *testing.T) {
	line := []byte(`{"type":"future_event_kind","sessionID":"s"}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Errorf("unknown type should surface as system event: %+v", got)
	}
}
