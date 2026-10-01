//go:build linux

package restrictedruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func nativeClientFixture(t *testing.T, extra ...json.RawMessage) []byte {
	t.Helper()
	input := []json.RawMessage{json.RawMessage(`{"type":"message","role":"developer","id":null,"content":[{"type":"input_text","text":"ambient instructions must not escape"}]}`)}
	input = append(input, extra...)
	body, e := json.Marshal(map[string]any{"model": "gpt-5-mini", "instructions": "untrusted CLI instructions", "input": input, "tools": []any{map[string]any{"type": "function", "name": "view_image"}}, "tool_choice": "auto", "parallel_tool_calls": true, "reasoning": map[string]any{"summary": "auto"}, "include": []string{"reasoning.encrypted_content"}, "store": false, "stream": true, "prompt_cache_key": "foreign-cache", "client_metadata": map[string]any{"foreign": "state"}})
	if e != nil {
		t.Fatal(e)
	}
	return body
}

func TestNativeRequestUsesFrozenHostContext(t *testing.T) {
	p := NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128}
	body, state, e := p.NativeRequestBody(nativeClientFixture(t), NativeConversation{Instructions: "frozen scoped instructions", Input: "own scoped question"})
	if e != nil {
		t.Fatal(e)
	}
	for _, foreign := range []string{"untrusted CLI", "ambient instructions", "foreign-cache", "client_metadata", "view_image", "require_escalated"} {
		if strings.Contains(string(body), foreign) {
			t.Fatalf("forwarded %q: %s", foreign, body)
		}
	}
	if len(state.Prefix) != 1 || !strings.Contains(string(body), "frozen scoped instructions") || !strings.Contains(string(body), `"max_output_tokens":128`) {
		t.Fatalf("missing frozen state: %s", body)
	}
}

func TestNativeFollowupRequiresExactIssuedHistoryAndCall(t *testing.T) {
	p := NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128}
	_, state, e := p.NativeRequestBody(nativeClientFixture(t), NativeConversation{Instructions: "frozen", Input: "question"})
	if e != nil {
		t.Fatal(e)
	}
	call := json.RawMessage(`{"type":"function_call","id":"fc_own","call_id":"call_own","name":"exec_command","arguments":"{\"cmd\":\"true\"}"}`)
	state.History = []json.RawMessage{call}
	output := json.RawMessage(`{"type":"function_call_output","id":"fco_worker","call_id":"call_own","output":"own scratch output"}`)
	body, next, e := p.NativeRequestBody(nativeClientFixture(t, call, output), state)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.History) != 2 || strings.Contains(string(body), "fco_worker") {
		t.Fatalf("bad output binding: %s", body)
	}
	for name, raw := range map[string][]byte{"foreign call": nativeClientFixture(t, call, json.RawMessage(`{"type":"function_call_output","call_id":"call_foreign","output":"foreign"}`)), "missing issued call": nativeClientFixture(t, output), "edited issued call": nativeClientFixture(t, json.RawMessage(`{"type":"function_call","id":"fc_own","call_id":"call_own","name":"exec_command","arguments":"{\"cmd\":\"foreign\"}"}`), output), "duplicate output": nativeClientFixture(t, call, output, output)} {
		t.Run(name, func(t *testing.T) {
			if _, _, e := p.NativeRequestBody(raw, state); e == nil {
				t.Fatal("accepted unbound state")
			}
		})
	}
}

func TestNativeClosedRequestSchema(t *testing.T) {
	p := NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128}
	for name, mutate := range map[string]func(map[string]any){"external resource": func(m map[string]any) { m["previous_response_id"] = "foreign" }, "hosted tool": func(m map[string]any) { m["tools"] = []any{map[string]any{"type": "web_search"}} }, "model": func(m map[string]any) { m["model"] = "other" }, "client cap": func(m map[string]any) { m["max_output_tokens"] = 32768 }} {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			_ = json.Unmarshal(nativeClientFixture(t), &m)
			mutate(m)
			raw, _ := json.Marshal(m)
			if _, _, e := p.NativeRequestBody(raw, NativeConversation{Input: "own"}); e == nil {
				t.Fatal("accepted unsupported field")
			}
		})
	}
	raw := strings.Replace(string(nativeClientFixture(t)), `"model":"gpt-5-mini"`, `"model":"gpt-5-mini","model":"gpt-5-mini"`, 1)
	if _, _, e := p.NativeRequestBody([]byte(raw), NativeConversation{Input: "own"}); e == nil {
		t.Fatal("accepted duplicate key")
	}
}

func TestNativeReasoningOmissionStillReconstructsOnlyIssuedState(t *testing.T) {
	p := NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128}
	_, state, err := p.NativeRequestBody(nativeClientFixture(t), NativeConversation{Instructions: "frozen", Input: "own"})
	if err != nil {
		t.Fatal(err)
	}
	reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_own","summary":[],"encrypted_content":"opaque-own"}`)
	call := json.RawMessage(`{"type":"function_call","id":"fc_own","call_id":"call_own","name":"exec_command","arguments":"{\"cmd\":\"true\"}"}`)
	output := json.RawMessage(`{"type":"function_call_output","call_id":"call_own","output":"own"}`)
	state.History = []json.RawMessage{reasoning, call}
	body, _, err := p.NativeRequestBody(nativeClientFixture(t, call, output), state)
	if err != nil || !strings.Contains(string(body), "opaque-own") {
		t.Fatalf("host issued opaque state lost: %s %v", body, err)
	}
	echoed := json.RawMessage(`{"type":"reasoning","id":"rs_own","summary":[],"encrypted_content":"opaque-own","content":null}`)
	if _, _, err := p.NativeRequestBody(nativeClientFixture(t, echoed, call, output), state); err != nil {
		t.Fatal(err)
	}
	foreign := json.RawMessage(`{"type":"reasoning","id":"rs_own","summary":[],"encrypted_content":"foreign","content":null}`)
	if _, _, err := p.NativeRequestBody(nativeClientFixture(t, foreign, call, output), state); err == nil {
		t.Fatal("foreign opaque state admitted")
	}
	nonnull := json.RawMessage(`{"type":"reasoning","id":"rs_own","summary":[],"encrypted_content":"opaque-own","content":[]}`)
	if _, _, err := p.NativeRequestBody(nativeClientFixture(t, nonnull, call, output), state); err == nil {
		t.Fatal("foreign reasoning content admitted")
	}
}
