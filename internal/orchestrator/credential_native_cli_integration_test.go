//go:build integration

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Deterministic local providers drive actual CLI shell tools without paid model
// calls or inherited logins. This fixture intentionally isolates env filtering
// from the separately tested container sandbox preflight.
func TestNativeCLIShellEnvironment(t *testing.T) {
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
		t.Run(adapter, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			home := t.TempDir()
			req := AgentRunRequest{AgentSlug: "fixture", RunID: "fixture-run", CLIAdapter: adapter, ToolProfile: "FULL", UserMessage: "Run the fixture shell tool once and report its result.", LLMModel: "gpt-5-mini", Credentials: []Credential{
				{ID: "token", Type: "CLI_TOKEN", EnvVarName: "NATIVE_TEST_TOKEN", PlainValue: "fixture-token-value"},
				{ID: "file", Type: "GENERIC_SECRET", EnvVarName: "NATIVE_TEST_SECRET_FILE", PlainValue: "fixture-file-value"},
			}}
			script, _, _, err := buildCredFileScript(req.Credentials, home, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := exec.CommandContext(ctx, "sh", "-c", script).Run(); err != nil {
				t.Fatal(err)
			}
			env, err := appendCredentialFileEnv(BuildEnvVarsSidecar(req, true), req.Credentials, home, true)
			if err != nil {
				t.Fatal(err)
			}
			command := `test "$NATIVE_TEST_TOKEN" = fixture-token-value && test -f "$NATIVE_TEST_SECRET_FILE" && test "$(cat "$NATIVE_TEST_SECRET_FILE")" = fixture-file-value && sh -c 'test "$NATIVE_TEST_TOKEN" = fixture-token-value' && printf '%s%s' NATIVE_ENV_ OK`
			var passed atomic.Bool
			var calls atomic.Int32
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "count_tokens") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"input_tokens":10}`)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					w.WriteHeader(400)
					return
				}
				followup := false
				if adapter == "CODEX_CLI" {
					input, _ := body["input"].([]any)
					for _, raw := range input {
						item, _ := raw.(map[string]any)
						if item["type"] == "function_call_output" {
							followup = true
							encoded, _ := json.Marshal(item["output"])
							if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
								passed.Store(true)
							}
						}
					}
				} else {
					messages, _ := body["messages"].([]any)
					for _, raw := range messages {
						message, _ := raw.(map[string]any)
						if message["role"] == "tool" {
							followup = true
							encoded, _ := json.Marshal(message["content"])
							if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
								passed.Store(true)
							}
						}
						content, _ := message["content"].([]any)
						for _, rawPart := range content {
							part, _ := rawPart.(map[string]any)
							if part["type"] == "tool_result" {
								followup = true
								encoded, _ := json.Marshal(part["content"])
								if strings.Contains(string(encoded), "NATIVE_ENV_OK") {
									passed.Store(true)
								}
							}
						}
					}
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				send := func(event string, value any) {
					encoded, _ := json.Marshal(value)
					if event != "" {
						fmt.Fprintf(w, "event: %s\n", event)
					}
					fmt.Fprintf(w, "data: %s\n\n", encoded)
				}
				if adapter == "CLAUDE_CODE" {
					send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "fixture-message", "type": "message", "role": "assistant", "model": "fixture", "content": []any{}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 0}}})
					if !followup {
						send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "fixture-tool", "name": "Bash", "input": map[string]any{}}})
						args, _ := json.Marshal(map[string]any{"command": command, "description": "Check fixture credential environment"})
						send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
					} else {
						send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": "FIXTURE_DONE"}})
					}
					send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
					reason := "tool_use"
					if followup {
						reason = "end_turn"
					}
					send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 10}})
					send("message_stop", map[string]any{"type": "message_stop"})
					return
				}
				if adapter == "OPENCODE" {
					delta := map[string]any{"role": "assistant"}
					finish := "tool_calls"
					if !followup {
						args, _ := json.Marshal(map[string]any{"command": command, "description": "Check fixture credential environment"})
						delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "fixture-tool", "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}}
					} else {
						delta["content"] = "FIXTURE_DONE"
						finish = "stop"
					}
					send("", map[string]any{"id": "fixture-chat", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
					send("", map[string]any{"id": "fixture-chat", "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})
					fmt.Fprint(w, "data: [DONE]\n\n")
					return
				}
				var item map[string]any
				if !followup {
					args, _ := json.Marshal(map[string]any{"cmd": command, "yield_time_ms": 1000, "max_output_tokens": 300})
					item = map[string]any{"type": "function_call", "id": "fixture-call", "call_id": "fixture-tool", "name": "exec_command", "arguments": string(args), "status": "completed"}
				} else {
					item = map[string]any{"type": "message", "id": "fixture-final", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "FIXTURE_DONE", "annotations": []any{}}}}
				}
				response := map[string]any{"id": "fixture-response", "status": "completed", "model": "gpt-5-mini", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}
				send("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
				send("response.completed", map[string]any{"type": "response.completed", "response": response})
			}))
			defer fixture.Close()
			env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
			env = overrideEnv(env, "HOME", home)
			env = overrideEnv(env, "CODEX_HOME", filepath.Join(home, "codex"))
			env = overrideEnv(env, "ANTHROPIC_API_KEY", "synthetic-fixture-key")
			env = overrideEnv(env, "ANTHROPIC_BASE_URL", fixture.URL)
			env = append(env, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_CONFIG_PROJECT_DISABLE=1", "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"))
			var argv []string
			switch adapter {
			case "CLAUDE_CODE":
				argv = (claudeCodeAdapter{}).BuildCommand(req)
				argv = append(argv, req.UserMessage)
			case "CODEX_CLI":
				argv = (codexAdapter{}).BuildCommand(req)
				for i := range argv {
					if argv[i] == "workspace-write" {
						argv[i] = "danger-full-access"
					}
				}
				argv = append(argv, "--config", `model_provider="fixture"`, "--config", `model_providers.fixture.name="Fixture"`, "--config", fmt.Sprintf("model_providers.fixture.base_url=%q", fixture.URL), "--config", `model_providers.fixture.wire_api="responses"`, "--config", `model_providers.fixture.requires_openai_auth=false`)
			case "OPENCODE":
				req.LLMModel = "fixture/fixture"
				configuration := map[string]any{"provider": map[string]any{"fixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Fixture", "options": map[string]any{"baseURL": fixture.URL + "/v1", "apiKey": "synthetic-fixture-key"}, "models": map[string]any{"fixture": map[string]any{"name": "Fixture", "limit": map[string]any{"context": 200000, "output": 1000}}}}}, "permission": map[string]any{"*": "allow"}}
				encoded, _ := json.Marshal(configuration)
				env = overrideEnv(env, "OPENCODE_CONFIG_CONTENT", string(encoded))
				argv = (opencodeAdapter{}).BuildCommand(req)
			}
			path, err := exec.LookPath(argv[0])
			if err != nil {
				t.Fatal("required native CLI unavailable", err)
			}
			cmd := exec.CommandContext(ctx, path, argv[1:]...)
			cmd.Env = env
			cmd.Dir = home
			out, err := cmd.CombinedOutput()
			if err != nil || !passed.Load() {
				t.Fatalf("native %s shell did not verify credential delivery (calls=%d): %v\n%s", adapter, calls.Load(), err, out)
			}
		})
	}
}
