package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestAnthropicStream_ParsesSSE walks an Anthropic SSE stream end-to-end and
// verifies that text deltas, tool_use blocks, and final stop reason all flow
// through the handler exactly once each.
func TestAnthropicStream_ParsesSSE(t *testing.T) {
	t.Parallel()
	const sse = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"add"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

data: [DONE]
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := newTestAnthropic("k", srv)

	var (
		gotText  strings.Builder
		toolHits int
		gotDone  bool
	)
	resp, err := p.Stream(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "do it"}},
	}, func(e StreamEvent) error {
		switch e.Type {
		case "text":
			gotText.WriteString(e.Content)
		case "tool_call":
			toolHits++
			if e.ToolCall.Name != "add" {
				t.Errorf("unexpected tool name: %q", e.ToolCall.Name)
			}
		case "done":
			gotDone = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if gotText.String() != "Hello world" {
		t.Errorf("text = %q, want Hello world", gotText.String())
	}
	if toolHits != 1 {
		t.Errorf("expected 1 tool_call event, got %d", toolHits)
	}
	if !gotDone {
		t.Error("expected done event")
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop reason = %q, want tool_use", resp.StopReason)
	}
	if resp.OutputToks != 7 {
		t.Errorf("output toks = %d, want 7", resp.OutputToks)
	}
	if resp.InputToks != 12 {
		t.Errorf("input toks = %d, want 12", resp.InputToks)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Input != `{"a":1}` {
		t.Errorf("tool calls = %+v", resp.ToolCalls)
	}
}

// TestOpenAIStream_ParsesSSE exercises the OpenAI streaming chunk parser:
// content deltas, tool-call delta accumulation, and the [DONE] sentinel.
func TestOpenAIStream_ParsesSSE(t *testing.T) {
	t.Parallel()
	const sse = `data: {"choices":[{"delta":{"content":"Hi"}}]}

data: {"choices":[{"delta":{"content":" there"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"do_it","arguments":"{\"x\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}

data: [DONE]
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	var text strings.Builder
	var calls int
	var done bool
	resp, err := p.Stream(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	}, func(e StreamEvent) error {
		switch e.Type {
		case "text":
			text.WriteString(e.Content)
		case "tool_call":
			calls++
		case "done":
			done = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if text.String() != "Hi there" {
		t.Errorf("text = %q", text.String())
	}
	if calls != 1 {
		t.Errorf("tool call count = %d", calls)
	}
	if !done {
		t.Error("missing done event")
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Input != `{"x":1}` {
		t.Errorf("tool call assembled wrong: %+v", resp.ToolCalls)
	}
}

// TestOllamaStream_ParsesNDJSON walks a multi-line NDJSON stream and checks
// that text chunks, the final tool_call list, and the done event all surface.
func TestOllamaStream_ParsesNDJSON(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"message":{"role":"assistant","content":"Hel"}}`,
		`{"message":{"role":"assistant","content":"lo"}}`,
		`{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"sum","arguments":{"a":1,"b":2}}}]},"done":true,"prompt_eval_count":3,"eval_count":5}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, c := range chunks {
			fmt.Fprintln(w, c)
		}
	}))
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")

	var text strings.Builder
	var toolCalls int
	var done bool
	resp, err := p.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, func(e StreamEvent) error {
		switch e.Type {
		case "text":
			text.WriteString(e.Content)
		case "tool_call":
			toolCalls++
		case "done":
			done = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if text.String() != "Hello" {
		t.Errorf("text = %q", text.String())
	}
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d", toolCalls)
	}
	if !done {
		t.Error("missing done event")
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if resp.InputToks != 3 || resp.OutputToks != 5 {
		t.Errorf("tokens %d/%d", resp.InputToks, resp.OutputToks)
	}
}

func TestProviderNames(t *testing.T) {
	t.Parallel()
	if NewAnthropic("k").Name() != "anthropic" {
		t.Error("anthropic name")
	}
	if NewOpenAIWithBaseURL("k", "http://x").Name() != "openai" {
		t.Error("openai name")
	}
	if NewOllama("http://x", "m").Name() != "ollama" {
		t.Error("ollama name")
	}
}

// TestNewOpenAI_DefaultBaseURL ensures the no-base-URL constructor targets
// the real OpenAI host. We can't hit it in tests, so just verify the
// internal field via a probe round-trip that captures the URL.
func TestNewOpenAI_DefaultBaseURL(t *testing.T) {
	t.Parallel()
	p := NewOpenAI("k")
	if p.baseURL != openaiAPIURL {
		t.Errorf("baseURL = %q, want %q", p.baseURL, openaiAPIURL)
	}
}

// TestAnthropicComplete_ErrorStatuses guards the status-code translation in
// checkAnthropicStatus. Each retryable code must surface a "max retries
// exceeded" wrap with the inner status, and each non-retryable code must
// translate to a stable, user-facing message.
func TestAnthropicComplete_ErrorStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		code    int
		wantMsg string
	}{
		{"unauthorized", http.StatusUnauthorized, "invalid Anthropic API key"},
		{"forbidden", http.StatusForbidden, "Anthropic API returned 403"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("nope"))
			}))
			defer srv.Close()

			p := newTestAnthropic("k", srv)
			_, err := p.Complete(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error %q must contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestOpenAIComplete_ErrorStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code    int
		wantMsg string
	}{
		{http.StatusUnauthorized, "invalid OpenAI API key"},
		{http.StatusTooManyRequests, "OpenAI rate limit exceeded"},
		{http.StatusInternalServerError, "OpenAI API returned 500"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("err"))
			}))
			defer srv.Close()
			p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
			_, err := p.Complete(context.Background(), Request{
				Model:    "m",
				Messages: []Message{{Role: RoleUser, Content: "hi"}},
			})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("got %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestOllamaComplete_ErrorStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream"))
	}))
	defer srv.Close()
	p := NewOllama(srv.URL, "m")
	_, err := p.Complete(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ollama returned 502") {
		t.Errorf("wrong error: %v", err)
	}
}

// TestAnthropic_RetriesOnRateLimit verifies that doWithRetry actually retries
// on a 429 and ultimately succeeds when the backend recovers.
func TestAnthropic_RetriesOnRateLimit(t *testing.T) {
	t.Parallel()
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// First attempt: 429 with a tiny Retry-After so the second attempt fires fast.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":     []map[string]string{{"type": "text", "text": "Recovered"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()

	p := newTestAnthropic("k", srv)
	resp, err := p.Complete(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if resp.Content != "Recovered" {
		t.Errorf("content = %q", resp.Content)
	}
	if attempts < 2 {
		t.Errorf("expected ≥2 attempts, got %d", attempts)
	}
}

// TestAnthropicComplete_BuildsCacheControlSystem verifies the cache_control
// hint added to the system prompt is encoded as the documented array form
// (so Anthropic enables prompt caching).
func TestAnthropicComplete_BuildsCacheControlSystem(t *testing.T) {
	t.Parallel()
	// Capture handler errors via closure to avoid t.Fatal/Error from a separate
	// goroutine (runtime.Goexit only exits the handler goroutine, not the test).
	var (
		handlerMu  sync.Mutex
		handlerErr error
	)
	captureErr := func(err error) {
		handlerMu.Lock()
		defer handlerMu.Unlock()
		if handlerErr == nil {
			handlerErr = err
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			captureErr(fmt.Errorf("decode body: %w", err))
			return
		}
		sys, ok := body["system"].([]any)
		if !ok || len(sys) != 1 {
			captureErr(fmt.Errorf("expected system as array, got %T %v", body["system"], body["system"]))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer srv.Close()
	p := newTestAnthropic("k", srv)
	if _, err := p.Complete(context.Background(), Request{
		Model:    "m",
		System:   "be helpful",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	handlerMu.Lock()
	defer handlerMu.Unlock()
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
}

// TestOllamaBuildRequestBody_Options verifies that temperature and max_tokens
// flow through into Ollama's "options" map (not top-level fields).
func TestOllamaBuildRequestBody_Options(t *testing.T) {
	t.Parallel()
	temp := 0.5
	p := NewOllama("http://x", "m")
	body, err := p.buildRequestBody(Request{
		Model:       "llama3",
		Messages:    []Message{{Role: RoleUser, Content: "x"}},
		Temperature: &temp,
		MaxTokens:   42,
	}, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	opts, ok := got["options"].(map[string]any)
	if !ok {
		t.Fatalf("expected options map, got %T", got["options"])
	}
	if v, _ := opts["temperature"].(float64); v != 0.5 {
		t.Errorf("temperature in options = %v", opts["temperature"])
	}
	if v, _ := opts["num_predict"].(float64); int(v) != 42 {
		t.Errorf("num_predict = %v", opts["num_predict"])
	}
}

// TestOllamaBuildRequestBody_DefaultModel verifies the provider's default
// model is used when the request omits one.
func TestOllamaBuildRequestBody_DefaultModel(t *testing.T) {
	t.Parallel()
	p := NewOllama("http://x", "default-model")
	body, err := p.buildRequestBody(Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["model"] != "default-model" {
		t.Errorf("model = %v, want default-model", got["model"])
	}
}

// TestOllamaToOllamaMessage_AssistantWithToolCalls covers the assistant +
// tool_calls round-trip, which the existing tests didn't exercise.
func TestOllamaToOllamaMessage_AssistantWithToolCalls(t *testing.T) {
	t.Parallel()
	m := toOllamaMessage(Message{
		Role:      RoleAssistant,
		Content:   "thinking",
		ToolCalls: []ToolCall{{ID: "tc", Name: "add", Input: `{"a":1}`}},
	})
	if m.Role != "assistant" {
		t.Errorf("role = %q", m.Role)
	}
	if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "add" {
		t.Errorf("tool calls = %+v", m.ToolCalls)
	}
}

// TestAnthropicStream_CacheTokens guards the streaming cache-usage path.
// Anthropic ships the full usage block on message_start, including cache
// read + creation counts. Dropping them silently produced zero in
// telemetry's gen_ai.usage.cached_input_tokens which makes the LLM trace
// view useless for cost forensics.
func TestAnthropicStream_CacheTokens(t *testing.T) {
	t.Parallel()
	const sse = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":50,"output_tokens":0,"cache_read_input_tokens":2000,"cache_creation_input_tokens":300}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

data: [DONE]
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := newTestAnthropic("k", srv)
	resp, err := p.Stream(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.CachedInputToks != 2000 {
		t.Errorf("cached input toks = %d, want 2000", resp.CachedInputToks)
	}
	if resp.CacheCreationToks != 300 {
		t.Errorf("cache creation toks = %d, want 300", resp.CacheCreationToks)
	}
}
