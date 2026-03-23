package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("missing API key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("missing anthropic-version header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content":     []map[string]string{{"type": "text", "text": "Hello from Claude"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer srv.Close()

	p := newTestAnthropic("test-key", srv)

	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		System:   "You are helpful.",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello from Claude" {
		t.Errorf("got content %q, want %q", resp.Content, "Hello from Claude")
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("got stop reason %q, want %q", resp.StopReason, StopEndTurn)
	}
	if resp.InputToks != 10 || resp.OutputToks != 5 {
		t.Errorf("got tokens %d/%d, want 10/5", resp.InputToks, resp.OutputToks)
	}
}

func TestAnthropicToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		tools, _ := body["tools"].([]any)
		if len(tools) == 0 {
			t.Error("expected tools in request")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "id": "tc_1", "name": "get_weather", "input": map[string]string{"city": "Prague"}},
			},
			"stop_reason": "tool_use",
			"usage":       map[string]int{"input_tokens": 20, "output_tokens": 15},
		})
	}))
	defer srv.Close()

	p := newTestAnthropic("test-key", srv)

	resp, err := p.Complete(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "Weather in Prague?"}},
		Tools: []ToolDef{{
			Name:        "get_weather",
			Description: "Get weather",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]string{"type": "string"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("got stop reason %q, want %q", resp.StopReason, StopToolUse)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("got tool name %q, want %q", resp.ToolCalls[0].Name, "get_weather")
	}
}

func TestOpenAIComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("missing Authorization header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": "Hello from GPT"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 8, "completion_tokens": 4},
		})
	}))
	defer srv.Close()

	p := NewOpenAIWithBaseURL("test-key", srv.URL+"/v1/chat/completions")
	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello from GPT" {
		t.Errorf("got content %q, want %q", resp.Content, "Hello from GPT")
	}
	if resp.InputToks != 8 || resp.OutputToks != 4 {
		t.Errorf("got tokens %d/%d, want 8/4", resp.InputToks, resp.OutputToks)
	}
}

func TestOpenAIToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":       "call_abc",
						"type":     "function",
						"function": map[string]string{"name": "list_crews", "arguments": "{}"},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 8},
		})
	}))
	defer srv.Close()

	p := NewOpenAIWithBaseURL("test-key", srv.URL+"/v1/chat/completions")
	resp, err := p.Complete(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "List crews"}},
		Tools: []ToolDef{{
			Name: "list_crews", Description: "List crews",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("got stop reason %q, want %q", resp.StopReason, StopToolUse)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "list_crews" {
		t.Errorf("unexpected tool calls: %+v", resp.ToolCalls)
	}
}

func TestOllamaComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "llama3" {
			t.Errorf("got model %v, want llama3", body["model"])
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"role": "assistant", "content": "Hello from Llama"},
			"done":              true,
			"prompt_eval_count": 6,
			"eval_count":        3,
		})
	}))
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")
	resp, err := p.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "Hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello from Llama" {
		t.Errorf("got content %q, want %q", resp.Content, "Hello from Llama")
	}
	if resp.InputToks != 6 || resp.OutputToks != 3 {
		t.Errorf("got tokens %d/%d, want 6/3", resp.InputToks, resp.OutputToks)
	}
}

func TestOllamaToolUse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{{
					"function": map[string]any{
						"name":      "get_stats",
						"arguments": map[string]any{"workspace_id": "ws1"},
					},
				}},
			},
			"done":              true,
			"prompt_eval_count": 10,
			"eval_count":        8,
		})
	}))
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")
	resp, err := p.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "Stats"}},
		Tools: []ToolDef{{
			Name: "get_stats", Description: "Get workspace stats",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("got stop reason %q, want %q", resp.StopReason, StopToolUse)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_stats" {
		t.Errorf("unexpected tool calls: %+v", resp.ToolCalls)
	}
}

func TestProviderInterface(t *testing.T) {
	var _ Provider = (*Anthropic)(nil)
	var _ Provider = (*OpenAI)(nil)
	var _ Provider = (*Ollama)(nil)
}

func TestAnthropicMessageConversion(t *testing.T) {
	// Tool result message
	m := toAnthropicMessage(Message{Role: RoleTool, ToolCallID: "tc_1", Content: "result data"})
	if m.Role != "user" {
		t.Errorf("tool result role should be user, got %s", m.Role)
	}
	blocks, ok := m.Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 {
		t.Fatal("expected 1 content block for tool result")
	}
	if blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "tc_1" {
		t.Errorf("unexpected block: %+v", blocks[0])
	}

	// Assistant message with tool calls
	m2 := toAnthropicMessage(Message{
		Role:      RoleAssistant,
		Content:   "thinking...",
		ToolCalls: []ToolCall{{ID: "tc_2", Name: "list", Input: "{}"}},
	})
	if m2.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", m2.Role)
	}
}

func TestOpenAIMessageConversion(t *testing.T) {
	// Tool result message
	m := toOpenAIMessage(Message{Role: RoleTool, ToolCallID: "call_1", Content: "ok"})
	if m.Role != "tool" || m.ToolCallID != "call_1" {
		t.Errorf("unexpected tool message: %+v", m)
	}

	// Assistant with tool calls
	m2 := toOpenAIMessage(Message{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: "call_2", Name: "fn", Input: `{"x":1}`}},
	})
	if len(m2.ToolCalls) != 1 || m2.ToolCalls[0].Function.Name != "fn" {
		t.Errorf("unexpected tool calls: %+v", m2.ToolCalls)
	}
}

func TestOllamaMessageConversion(t *testing.T) {
	m := toOllamaMessage(Message{Role: RoleTool, Content: "tool output"})
	if m.Role != "tool" {
		t.Errorf("expected tool role, got %s", m.Role)
	}
}

// --- helpers ---

// newTestAnthropic creates an Anthropic provider that points at a test server.
func newTestAnthropic(apiKey string, srv *httptest.Server) *Anthropic {
	p := NewAnthropic(apiKey)
	p.client = srv.Client()
	srvURL := srv.URL
	p.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srvURL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})
	return p
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
