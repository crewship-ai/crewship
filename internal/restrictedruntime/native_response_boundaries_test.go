//go:build linux

package restrictedruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeTerminalRejectsMalformedLocalToolArguments(t *testing.T) {
	for _, tc := range []struct{ name, tool, args string }{
		{"malformed JSON", "exec_command", `{`},
		{"missing command", "exec_command", `{}`},
		{"nonstring command", "exec_command", `{"cmd":1}`},
		{"empty command", "exec_command", `{"cmd":""}`},
		{"NUL command", "exec_command", `{"cmd":"echo\u0000hidden"}`},
		{"unknown shell type", "exec_command", `{"cmd":"true","shell":false}`},
		{"workdir type", "exec_command", `{"cmd":"true","workdir":true}`},
		{"outside home", "exec_command", `{"cmd":"true","workdir":"/etc"}`},
		{"login type", "exec_command", `{"cmd":"true","login":"false"}`},
		{"tty type", "exec_command", `{"cmd":"true","tty":1}`},
		{"stdin unknown field", "write_stdin", `{"session_id":1,"sandbox_permissions":"require_escalated"}`},
		{"missing session", "write_stdin", `{}`},
		{"string session", "write_stdin", `{"session_id":"1"}`},
		{"zero session", "write_stdin", `{"session_id":0}`},
		{"fractional session", "write_stdin", `{"session_id":1.5}`},
		{"overflow session", "write_stdin", `{"session_id":9223372036854775808}`},
		{"stdin type", "write_stdin", `{"session_id":1,"chars":false}`},
		{"wait type", "write_stdin", `{"session_id":1,"yield_time_ms":"1"}`},
		{"zero wait", "write_stdin", `{"session_id":1,"yield_time_ms":0}`},
		{"fractional wait", "write_stdin", `{"session_id":1,"yield_time_ms":1.5}`},
		{"output bound", "write_stdin", `{"session_id":1,"max_output_tokens":10001}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": tc.tool, "arguments": tc.args}
			issued, stream, err := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini")
			if err == nil || len(issued) != 0 || len(stream) != 0 {
				t.Fatalf("invalid local tool escaped validation: %v", err)
			}
		})
	}
	for _, tool := range []string{"exec_command", "write_stdin"} {
		args := map[string]any{"cmd": strings.Repeat("x", 64<<10+1)}
		if tool == "write_stdin" {
			args = map[string]any{"session_id": 1, "chars": strings.Repeat("x", 64<<10+1)}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": tool, "arguments": string(raw)}
		if _, _, err := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini"); err == nil {
			t.Fatalf("oversized %s accepted", tool)
		}
	}
}

func TestNativeTerminalAcceptsBoundedShellAndSessionContinuation(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{"exec_command", `{"cmd":"pwd","shell":"/bin/bash","workdir":"/home/agent","login":false,"tty":true,"yield_time_ms":30000,"max_output_tokens":10000}`},
		{"exec_command", `{"cmd":"pwd","shell":"/bin/sh","workdir":"/home/agent/project"}`},
		{"write_stdin", `{"session_id":7,"chars":"next\n","yield_time_ms":1,"max_output_tokens":1}`},
		{"write_stdin", `{"session_id":7}`},
	} {
		item := map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": tc.tool, "arguments": tc.args}
		issued, stream, err := NativeTerminal(nativeStreamFixture(t, item), "gpt-5-mini")
		if err != nil || len(issued) != 1 || !strings.Contains(string(stream), "response.completed") {
			t.Fatalf("valid %s rejected: %v", tc.tool, err)
		}
		var got map[string]any
		if err := json.Unmarshal(issued[0], &got); err != nil {
			t.Fatal(err)
		}
		if got["arguments"] != tc.args {
			t.Fatalf("approved arguments changed: %+v", got)
		}
	}
}

func TestNativeTerminalRejectsMalformedMessagesAndReasoning(t *testing.T) {
	for _, tc := range []struct {
		name string
		item map[string]any
	}{
		{"unfinished item", map[string]any{"type": "message", "id": "m", "status": "in_progress"}},
		{"foreign role", map[string]any{"type": "message", "id": "m", "role": "user", "content": []any{}}},
		{"missing content", map[string]any{"type": "message", "id": "m", "role": "assistant"}},
		{"nonobject content", map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{"raw"}}},
		{"nontext content", map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{map[string]any{"type": "output_image", "text": "x"}}}},
		{"nonstring output", map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": 7}}}},
		{"unverified annotation", map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "x", "annotations": []any{map[string]any{"url": "https://example.test"}}}}}},
		{"annotation type", map[string]any{"type": "message", "id": "m", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "x", "annotations": false}}}},
		{"missing opaque state", map[string]any{"type": "reasoning", "id": "r", "summary": []any{}}},
		{"reasoning unknown field", map[string]any{"type": "reasoning", "id": "r", "encrypted_content": "opaque", "summary": []any{}, "secret": "untrusted"}},
		{"summary type", map[string]any{"type": "reasoning", "id": "r", "encrypted_content": "opaque", "summary": "raw"}},
		{"nonobject summary", map[string]any{"type": "reasoning", "id": "r", "encrypted_content": "opaque", "summary": []any{"raw"}}},
		{"summary text type", map[string]any{"type": "reasoning", "id": "r", "encrypted_content": "opaque", "summary": []any{map[string]any{"type": "summary_text", "text": false}}}},
		{"unknown output", map[string]any{"type": "web_search_call", "id": "search"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if issued, stream, err := NativeTerminal(nativeStreamFixture(t, tc.item), "gpt-5-mini"); err == nil || len(issued) != 0 || len(stream) != 0 {
				t.Fatalf("malformed output released: %v", err)
			}
		})
	}
}
