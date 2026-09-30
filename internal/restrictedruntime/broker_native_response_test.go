//go:build linux

package restrictedruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func nativeStreamFixture(t *testing.T, item map[string]any) []byte {
	t.Helper()
	raw, e := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_own", "model": "gpt-5-mini", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}})
	if e != nil {
		t.Fatal(e)
	}
	return append(append([]byte("event: response.completed\ndata: "), raw...), []byte("\n\n")...)
}

func TestNativeTerminalRebuildsOnlyValidatedTools(t *testing.T) {
	item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": "exec_command", "arguments": `{"cmd":"printf SAFE","shell":"/bin/sh","login":false}`, "status": "completed"}
	raw := append([]byte("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"malicious unchecked delta\"}\n\n"), nativeStreamFixture(t, item)...)
	issued, stream, e := NativeTerminal(raw, "gpt-5-mini")
	if e != nil {
		t.Fatal(e)
	}
	if len(issued) != 1 || strings.Contains(string(stream), "malicious") || strings.Contains(string(issued[0]), "status") {
		t.Fatalf("unvetted stream/state: %s", stream)
	}
	if !strings.Contains(string(stream), "printf SAFE") {
		t.Fatal("missing vetted call")
	}
}

func TestNativeTerminalDeniesExternalToolsAndEscalation(t *testing.T) {
	for name, args := range map[string]string{"escalation": `{"cmd":"true","sandbox_permissions":"require_escalated"}`, "foreign workdir": `{"cmd":"true","workdir":"/home/agent/../broker"}`, "oversized wait": `{"cmd":"true","yield_time_ms":300001}`, "unknown shell": `{"cmd":"true","shell":"/host/bin/bash"}`} {
		t.Run(name, func(t *testing.T) {
			item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": "exec_command", "arguments": args}
			if _, _, e := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini"); e == nil {
				t.Fatal("accepted unsafe tool")
			}
		})
	}
	for _, name := range []string{"view_image", "web_search", "spawn_agent", "mcp_tool"} {
		item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": name, "arguments": `{}`}
		if _, _, e := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini"); e == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestNativeOpaqueReasoningRemainsExactAttemptState(t *testing.T) {
	item := map[string]any{"type": "reasoning", "id": "rs_own", "summary": []any{}, "encrypted_content": "opaque-issued-state"}
	issued, _, e := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini")
	if e != nil {
		t.Fatal(e)
	}
	p := NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128}
	_, state, e := p.NativeRequestBody(nativeClientFixture(t), NativeConversation{Input: "own"})
	if e != nil {
		t.Fatal(e)
	}
	// A reasoning-only completion has no pending local tool call and cannot be
	// continued by attaching arbitrary client messages or foreign state.
	state.History = issued
	if _, _, e := p.NativeRequestBody(nativeClientFixture(t, issued[0]), state); e == nil {
		t.Fatal("continued terminal reasoning without an issued tool")
	}
	foreign := json.RawMessage(strings.Replace(string(issued[0]), "opaque-issued-state", "foreign", 1))
	if _, _, e := p.NativeRequestBody(nativeClientFixture(t, foreign), state); e == nil {
		t.Fatal("accepted foreign opaque state")
	}
}
