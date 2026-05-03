package orchestrator

import (
	"testing"
)

// TestParseOpenCode_FullResponse pins the single-object envelope from
// `opencode run --format json`. Unlike the streaming CLIs, opencode emits one
// JSON blob at the end with the full response + metadata.
func TestParseOpenCode_FullResponse(t *testing.T) {
	line := []byte(`{"response":"42","model":"claude-sonnet-4-6","provider":"anthropic","duration_ms":1234,"session_id":"o-1","usage":{"input_tokens":80,"output_tokens":20}}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	// Expect 2 events: the text body + a final result envelope.
	if len(got) != 2 {
		t.Fatalf("want 2 events (text + result), got %d: %+v", len(got), got)
	}
	if got[0].Type != "text" || got[0].Content != "42" {
		t.Errorf("first event should be text response, got %+v", got[0])
	}
	if got[1].Type != "result" {
		t.Errorf("second event should be result, got %+v", got[1])
	}

	meta := got[1].Metadata.(map[string]interface{})
	if meta["model"] != "claude-sonnet-4-6" {
		t.Errorf("model lost: %v", meta["model"])
	}
	if meta["provider"] != "anthropic" {
		t.Errorf("provider lost: %v", meta["provider"])
	}
	if meta["duration_ms"].(float64) != 1234 {
		t.Errorf("duration_ms lost: %v", meta["duration_ms"])
	}
	usage, ok := meta["usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("usage not a map: %T", meta["usage"])
	}
	if usage["input_tokens"].(float64) != 80 {
		t.Errorf("usage.input_tokens lost: %v", usage["input_tokens"])
	}
}

// TestParseOpenCode_TextFieldFallback covers builds that put the response in
// .text or .output instead of .response.
func TestParseOpenCode_TextFieldFallback(t *testing.T) {
	cases := []struct {
		payload string
		want    string
	}{
		{`{"text":"alpha"}`, "alpha"},
		{`{"output":"beta"}`, "beta"},
	}
	for _, tc := range cases {
		var got []AgentEvent
		parseOpenCodeStreamJSON([]byte(tc.payload), func(e AgentEvent) { got = append(got, e) })

		if len(got) < 1 || got[0].Type != "text" || got[0].Content != tc.want {
			t.Errorf("payload %s want text %q, got %+v", tc.payload, tc.want, got)
		}
	}
}

// TestParseOpenCode_Error pins how an upstream error round-trips.
func TestParseOpenCode_Error(t *testing.T) {
	line := []byte(`{"error":"missing API key"}`)
	var got []AgentEvent
	parseOpenCodeStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	// Expect: error event + a result envelope flagged is_error.
	hasError := false
	hasResultErr := false
	for _, e := range got {
		if e.Type == "error" && e.Content == "missing API key" {
			hasError = true
		}
		if e.Type == "result" {
			meta := e.Metadata.(map[string]interface{})
			if isErr, _ := meta["is_error"].(bool); isErr {
				hasResultErr = true
			}
		}
	}
	if !hasError {
		t.Error("expected error event")
	}
	if !hasResultErr {
		t.Error("expected result with is_error=true")
	}
}

// TestParseOpenCode_NotJSON falls through to a text event so debug noise still
// surfaces.
func TestParseOpenCode_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseOpenCodeStreamJSON([]byte("hello plaintext"), func(e AgentEvent) { got = append(got, e) })

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
	parseOpenCodeStreamJSON([]byte(`{"response":"x"}`), nil)
}
