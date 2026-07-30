package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// The two tests in this file are a matched pair and must be read together.
//
// TestRunAgent_InBandResultError_MarksRunFailed pins the gate: an agent CLI
// that exits 0 but says "this turn failed" inside its own event stream (a
// refusal, an internal CLI error, an exhausted quota) must not produce a
// "completed" run. Before the fix the ONLY thing that flipped a run to error
// was a non-zero exit code, so every in-band failure was recorded as success —
// the mission/routine continued on an empty answer and the tokens were billed.
//
// TestRunAgent_ToolLevelError_KeepsRunCompleted pins the ceiling: a failed
// *tool* call in the middle of an otherwise fine run is normal agent work (a
// grep that matched nothing, a build that failed and got fixed) and must keep
// the run "completed". Overshooting the gate into tool-level errors would
// redden nearly every real run, so this test is as load-bearing as the first.

// inBandStreamMock builds a container mock whose *agent* exec streams `stream`
// and exits 0, while every setup exec (tmux probe, mkdir, manifest, canonical
// memory writes) returns an empty reader.
//
// The agent exec is identified exactly the way TestRunAgentSuccess identifies
// it — by the tmux session-name signature of the wrapper RunAgent builds —
// because the real CLI invocation only ever appears inside
// `sh -c "tmux new-session ... 'sh /tmp/agent-<slug>.sh' ..."`.
func inBandStreamMock(slug, stream string) *mockContainer {
	return &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			joined := strings.Join(cfg.Cmd, " ")
			if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-"+slug) {
				return &provider.ExecResult{
					ExecID: "exec-agent",
					Reader: io.NopCloser(strings.NewReader(stream)),
				}, nil
			}
			return &provider.ExecResult{
				ExecID: "noop",
				Reader: io.NopCloser(strings.NewReader("")),
			}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0}, // exit 0 everywhere — the whole point of these tests
	}
}

// runInBandCase drives one RunAgent invocation against the given adapter and
// stream, and returns the persisted terminal run status plus RunAgent's error.
func runInBandCase(t *testing.T, adapter, stream string) (string, error) {
	t.Helper()
	const slug = "test-agent"
	state := newMemState()
	o := New(inBandStreamMock(slug, stream), state, slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   slug,
		ChatID:      "s1",
		ContainerID: "c1",
		CLIAdapter:  adapter,
		UserMessage: "test",
		TimeoutSecs: 30,
	}, nil)

	data, getErr := state.Get(context.Background(), "agent_runs", "s1")
	if getErr != nil {
		t.Fatalf("read run state: %v", getErr)
	}
	if data == nil {
		t.Fatal("expected run state to be persisted")
	}
	var run RunState
	if uErr := json.Unmarshal(data, &run); uErr != nil {
		t.Fatalf("unmarshal run state: %v", uErr)
	}
	return run.Status, err
}

func TestRunAgent_InBandResultError_MarksRunFailed(t *testing.T) {
	// One row per adapter that exposes a RUN-level in-band failure signal.
	// The shapes are the ones the parsers in this package actually decode
	// (verified against parser_*.go field tags), not invented JSON.
	cases := []struct {
		name    string
		adapter string
		stream  string
		// wantDetail is a fragment of the CLI's own message that must survive
		// into the user-facing error, so chat/journal show a cause rather
		// than a bare "failed".
		wantDetail string
	}{
		{
			name:    "claude result is_error",
			adapter: "CLAUDE_CODE",
			stream: `{"type":"result","subtype":"error_during_execution","is_error":true,` +
				`"result":"I cannot help with that request."}` + "\n",
			wantDetail: "I cannot help with that request.",
		},
		{
			name:    "cursor result is_error",
			adapter: "CURSOR_CLI",
			stream: `{"type":"result","subtype":"error","is_error":true,` +
				`"result":"usage limit reached","request_id":"req-1"}` + "\n",
			wantDetail: "usage limit reached",
		},
		{
			name:    "droid result is_error (snake_case)",
			adapter: "FACTORY_DROID",
			stream: `{"type":"result","subtype":"error","is_error":true,` +
				`"result":"model provider returned 500","num_turns":1}` + "\n",
			wantDetail: "model provider returned 500",
		},
		{
			name:       "codex turn.failed",
			adapter:    "CODEX_CLI",
			stream:     `{"type":"turn.failed","error":"model refused to continue","usage":{}}` + "\n",
			wantDetail: "model refused to continue",
		},
		{
			name:       "gemini result status=error",
			adapter:    "GEMINI_CLI",
			stream:     `{"type":"result","status":"error","response":"quota exhausted","stats":{}}` + "\n",
			wantDetail: "quota exhausted",
		},
		{
			// OpenCode never stamps is_error on its result envelopes; its only
			// run-level failure signal is the fatal `error` event.
			name:    "opencode fatal error envelope",
			adapter: "OPENCODE",
			stream: `{"type":"error","sessionID":"s","error":{"name":"ProviderAuthError",` +
				`"data":{"message":"no api key configured"}}}` + "\n",
			wantDetail: "no api key configured",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := runInBandCase(t, tc.adapter, tc.stream)

			if status != "error" {
				t.Errorf("run status = %q, want %q — the CLI exited 0 but reported an in-band failure", status, "error")
			}
			if err == nil {
				t.Fatal("RunAgent returned nil error; the chat would render an empty assistant bubble for a failed run")
			}
			if !strings.Contains(err.Error(), tc.wantDetail) {
				t.Errorf("error %q does not carry the CLI's own message %q", err.Error(), tc.wantDetail)
			}
		})
	}
}

func TestRunAgent_ToolLevelError_KeepsRunCompleted(t *testing.T) {
	// A failed tool call followed by a successful terminal envelope. Every row
	// must stay "completed": the agent saw the tool failure and recovered.
	cases := []struct {
		name    string
		adapter string
		stream  string
	}{
		{
			name:    "claude failed tool_result block",
			adapter: "CLAUDE_CODE",
			stream: `{"type":"user","content":[{"type":"tool_result","tool_use_id":"t1",` +
				`"is_error":true,"text":"grep: no matches found"}]}` + "\n" +
				`{"type":"result","subtype":"success","is_error":false,"result":"done"}` + "\n",
		},
		{
			name:    "droid failed tool_result (camelCase isError)",
			adapter: "FACTORY_DROID",
			stream: `{"type":"tool_result","toolId":"t1","isError":true,"value":"exit 1"}` + "\n" +
				`{"type":"result","subtype":"success","is_error":false,"result":"done"}` + "\n",
		},
		{
			name:    "gemini tool_result status=error",
			adapter: "GEMINI_CLI",
			stream: `{"type":"tool_result","tool_id":"t1","status":"error","output":"boom"}` + "\n" +
				`{"type":"result","status":"success","response":"done","stats":{}}` + "\n",
		},
		{
			name:    "codex failed command_execution item",
			adapter: "CODEX_CLI",
			stream: `{"type":"item.completed","item":{"type":"command_execution","id":"c1",` +
				`"aggregated_output":"make: *** Error 1","exit_code":1,"status":"failed"}}` + "\n" +
				`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}` + "\n",
		},
		{
			name:    "opencode tool_use state.status=error",
			adapter: "OPENCODE",
			stream: `{"type":"tool_use","sessionID":"s","part":{"id":"tu-1","tool":"bash",` +
				`"state":{"status":"error","output":"boom","error":"exit 1"}}}` + "\n" +
				`{"type":"step_finish","sessionID":"s","part":{"id":"sf-1","reason":"stop",` +
				`"cost":0.01,"providerID":"anthropic","modelID":"claude-sonnet-5",` +
				`"tokens":{"input":10,"output":5}}}` + "\n",
		},
		{
			name:    "cursor completed tool_call then success",
			adapter: "CURSOR_CLI",
			stream: `{"type":"tool_call","subtype":"completed","call_id":"c1","tool_call":{"readToolCall":{}}}` + "\n" +
				`{"type":"result","subtype":"success","is_error":false,"result":"done"}` + "\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := runInBandCase(t, tc.adapter, tc.stream)

			if status != "completed" {
				t.Errorf("run status = %q, want %q — a failed TOOL call must not fail the run", status, "completed")
			}
			if err != nil {
				t.Errorf("RunAgent error = %v, want nil for a tool-level failure", err)
			}
		})
	}
}
