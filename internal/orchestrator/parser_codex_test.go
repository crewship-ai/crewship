package orchestrator

import (
	"testing"
)

// TestParseCodex_ThreadStarted pins the bootstrap event from the Rust port.
// Schema: {"type":"thread.started","thread_id":"<uuid>","model":"<id>"}
// (NOT session.started + session_id from the Agents-SDK style we initially
// guessed — that schema does not exist in @openai/codex 0.128.0).
func TestParseCodex_ThreadStarted(t *testing.T) {
	line := []byte(`{"type":"thread.started","thread_id":"thr-abc","model":"gpt-5"}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("want system event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["thread_id"] != "thr-abc" {
		t.Errorf("thread_id lost: %v", meta["thread_id"])
	}
	if meta["model"] != "gpt-5" {
		t.Errorf("model lost: %v", meta["model"])
	}
}

// TestParseCodex_TurnStartedSilent — turn.started has no payload and would
// flood the journal on long runs; parser drops it silently.
func TestParseCodex_TurnStartedSilent(t *testing.T) {
	var got []AgentEvent
	parseCodexStreamJSON([]byte(`{"type":"turn.started"}`), func(e AgentEvent) { got = append(got, e) })
	if len(got) != 0 {
		t.Errorf("turn.started must be silent, got %+v", got)
	}
}

// TestParseCodex_TurnCompletedUsage pins the usage envelope. Note the
// non-obvious field set: cached_input_tokens (cache hits) and
// reasoning_output_tokens (o1/o3 thinking) are billed separately from
// regular input/output tokens — Paymaster needs all four.
func TestParseCodex_TurnCompletedUsage(t *testing.T) {
	line := []byte(`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":50,"reasoning_output_tokens":120}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("want result event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	usage := meta["usage"].(map[string]interface{})
	for _, key := range []string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens"} {
		if _, ok := usage[key]; !ok {
			t.Errorf("usage.%s lost — Paymaster will undercount cost", key)
		}
	}
	if meta["is_error"].(bool) != false {
		t.Errorf("is_error should be false for successful turn")
	}
}

// TestParseCodex_TurnFailed — error envelope with usage (model still consumed
// tokens before failing, so usage must round-trip).
func TestParseCodex_TurnFailed(t *testing.T) {
	line := []byte(`{"type":"turn.failed","error":"context window exceeded","usage":{"input_tokens":20000}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "result" {
		t.Fatalf("want result event, got %+v", got)
	}
	if got[0].Content != "context window exceeded" {
		t.Errorf("error message lost: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if !meta["is_error"].(bool) {
		t.Errorf("is_error must be true for turn.failed")
	}
}

// TestParseCodex_AgentMessageStreaming — item.updated carries delta text;
// each delta becomes its own text event so the chat UI streams token-by-token.
func TestParseCodex_AgentMessageStreaming(t *testing.T) {
	deltas := [][]byte{
		[]byte(`{"type":"item.updated","item":{"type":"agent_message","id":"itm-1","delta":"Hello"}}`),
		[]byte(`{"type":"item.updated","item":{"type":"agent_message","id":"itm-1","delta":" world"}}`),
	}
	var got []AgentEvent
	for _, d := range deltas {
		parseCodexStreamJSON(d, func(e AgentEvent) { got = append(got, e) })
	}
	if len(got) != 2 {
		t.Fatalf("want 2 deltas, got %d: %+v", len(got), got)
	}
	if got[0].Content+got[1].Content != "Hello world" {
		t.Errorf("delta concat wrong: %q + %q", got[0].Content, got[1].Content)
	}
}

// TestParseCodex_AgentMessageCompleted — when no deltas precede it, the
// completed event carries full text and we emit it as a text event.
func TestParseCodex_AgentMessageCompleted(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"type":"agent_message","id":"itm-1","text":"final answer"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" || got[0].Content != "final answer" {
		t.Errorf("want completed text, got %+v", got)
	}
}

// TestParseCodex_Reasoning — o1/o3 chain-of-thought routes to "thinking".
func TestParseCodex_Reasoning(t *testing.T) {
	line := []byte(`{"type":"item.updated","item":{"type":"reasoning","id":"itm-2","delta":"considering options..."}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "thinking" || got[0].Content != "considering options..." {
		t.Errorf("want thinking event, got %+v", got)
	}
}

// TestParseCodex_CommandExecution — shell commands fan into tool_call
// (started) + tool_result (completed). Pins the canonical field name
// `aggregated_output` (NOT `output`) and the `exit_code` field, both of which
// the pre-fix parser silently dropped.
func TestParseCodex_CommandExecution(t *testing.T) {
	started := []byte(`{"type":"item.started","item":{"type":"command_execution","id":"cmd-1","command":"ls /tmp"}}`)
	completed := []byte(`{"type":"item.completed","item":{"type":"command_execution","id":"cmd-1","aggregated_output":"a.txt\nb.txt","exit_code":0,"status":"success"}}`)

	var got []AgentEvent
	parseCodexStreamJSON(started, func(e AgentEvent) { got = append(got, e) })
	parseCodexStreamJSON(completed, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Type != "tool_call" || got[0].Content != "shell" {
		t.Errorf("started event wrong: %+v", got[0])
	}
	startedMeta := got[0].Metadata.(map[string]interface{})
	input := startedMeta["input"].(map[string]interface{})
	if input["command"] != "ls /tmp" {
		t.Errorf("command lost: %v", input["command"])
	}

	if got[1].Type != "tool_result" || got[1].Content != "a.txt\nb.txt" {
		t.Errorf("completed event content lost — aggregated_output field rename regression? %+v", got[1])
	}
	completedMeta := got[1].Metadata.(map[string]interface{})
	if completedMeta["status"] != "success" {
		t.Errorf("status lost: %v", completedMeta["status"])
	}
	// metadata isn't JSON-roundtripped, so int stays int
	if exitCode, ok := completedMeta["exit_code"].(int); !ok || exitCode != 0 {
		t.Errorf("exit_code lost: %v (type %T)", completedMeta["exit_code"], completedMeta["exit_code"])
	}
}

// TestParseCodex_MCPToolCall pins the canonical mcp_tool_call schema:
// upstream emits separate `server` + `tool` + `arguments` + `result` fields,
// NOT a combined `name` + `args`. Pre-fix parser silently dropped both
// directions because field names didn't match.
func TestParseCodex_MCPToolCall(t *testing.T) {
	started := []byte(`{"type":"item.started","item":{"type":"mcp_tool_call","id":"mcp-1","server":"linear","tool":"create_issue","arguments":{"title":"bug"}}}`)
	completed := []byte(`{"type":"item.completed","item":{"type":"mcp_tool_call","id":"mcp-1","server":"linear","tool":"create_issue","result":"created issue #42"}}`)

	var got []AgentEvent
	parseCodexStreamJSON(started, func(e AgentEvent) { got = append(got, e) })
	parseCodexStreamJSON(completed, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	// Started event — composite display name, mcp_server + mcp_tool surfaced
	// separately for chat UI.
	if got[0].Type != "tool_call" || got[0].Content != "linear.create_issue" {
		t.Fatalf("started event wrong: %+v", got[0])
	}
	startedMeta := got[0].Metadata.(map[string]interface{})
	if startedMeta["transport"] != "mcp" {
		t.Errorf("transport tag lost")
	}
	if startedMeta["mcp_server"] != "linear" {
		t.Errorf("mcp_server lost (server field): %v", startedMeta["mcp_server"])
	}
	if startedMeta["mcp_tool"] != "create_issue" {
		t.Errorf("mcp_tool lost (tool field): %v", startedMeta["mcp_tool"])
	}
	input := startedMeta["input"].(map[string]interface{})
	if input["title"] != "bug" {
		t.Errorf("arguments → input mapping lost: %v", startedMeta["input"])
	}

	// Completed event — result field carries the response.
	if got[1].Type != "tool_result" || got[1].Content != "created issue #42" {
		t.Errorf("completed event wrong (result field rename regression?): %+v", got[1])
	}
}

// TestParseCodex_TodoList — plan/todo as system meta event.
func TestParseCodex_TodoList(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"type":"todo_list","items":[{"text":"Write tests","completed":true},{"text":"Ship","completed":false}]}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("todo_list wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "todo_list" {
		t.Errorf("subtype lost: %v", meta["subtype"])
	}
	items := meta["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("items count lost: %v", items)
	}
}

// TestParseCodex_CollabToolCall — multi-agent peer handoff feature.
func TestParseCodex_CollabToolCall(t *testing.T) {
	line := []byte(`{"type":"item.started","item":{"type":"collab_tool_call","sender_thread_id":"thr-1","receiver_thread_ids":["thr-2"],"prompt":"review my work","tool":"peer.escalate"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Fatalf("collab_tool_call wrong: %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["sender_thread_id"] != "thr-1" {
		t.Errorf("sender_thread_id lost: %v", meta["sender_thread_id"])
	}
	if meta["prompt"] != "review my work" {
		t.Errorf("prompt lost: %v", meta["prompt"])
	}
}

// TestParseCodex_ItemError — item-level error must NOT bubble as fatal error
// (would mis-fail healthy runs per upstream issue #19689). Should surface as
// a warning system event.
func TestParseCodex_ItemError(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"type":"error","id":"err-1","message":"upstream backpressure"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d: %+v", len(got), got)
	}
	// MUST NOT be type:error (that would mark the run failed in chat UI).
	if got[0].Type == "error" {
		t.Errorf("item-level error must NOT promote to fatal error event — would mis-fail otherwise-healthy runs")
	}
	if got[0].Type != "system" {
		t.Errorf("want system warning, got %s", got[0].Type)
	}
	if got[0].Content != "upstream backpressure" {
		t.Errorf("error message lost: %q", got[0].Content)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "warning" {
		t.Errorf("subtype must be warning, got %v", meta["subtype"])
	}
}

// TestParseCodex_PlanUpdate — plan/todo edits as system meta events.
func TestParseCodex_PlanUpdate(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"type":"plan_update","text":"1. Fix auth\n2. Add tests"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Errorf("want system event, got %+v", got)
	}
	meta := got[0].Metadata.(map[string]interface{})
	if meta["subtype"] != "plan_update" {
		t.Errorf("subtype lost: %v", meta["subtype"])
	}
}

// TestParseCodex_Error covers the top-level error envelope.
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
	parseCodexStreamJSON([]byte(`{"type":"thread.started","thread_id":"x"}`), nil)
}

// TestParseCodex_NotJSON falls through to text.
func TestParseCodex_NotJSON(t *testing.T) {
	var got []AgentEvent
	parseCodexStreamJSON([]byte("not json"), func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "text" {
		t.Errorf("want text fallback, got %+v", got)
	}
}

// TestParseCodex_UnknownItemType — forward-compat: unknown item subtypes
// preserved in journal as system events so we can debug + add handling later
// without the line being silently dropped.
func TestParseCodex_UnknownItemType(t *testing.T) {
	line := []byte(`{"type":"item.completed","item":{"type":"future_thing","id":"x"}}`)
	var got []AgentEvent
	parseCodexStreamJSON(line, func(e AgentEvent) { got = append(got, e) })

	if len(got) != 1 || got[0].Type != "system" {
		t.Errorf("unknown item.type should surface as system event: %+v", got)
	}
}
