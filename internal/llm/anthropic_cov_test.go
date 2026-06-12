package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- ListModels ---

func TestAnthropicListModels_TransportError(t *testing.T) {
	t.Parallel()
	a := NewAnthropic("key")
	a.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})
	_, err := a.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !strings.Contains(err.Error(), "anthropic http") {
		t.Errorf("error %q should wrap anthropic http", err)
	}
}

// --- Complete error paths ---

func TestAnthropicComplete_BuildBodyError(t *testing.T) {
	t.Parallel()
	a := NewAnthropic("key")
	_, err := a.Complete(context.Background(), Request{
		Model:    "claude-test",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolDef{{Name: "t", InputSchema: make(chan int)}}, // unmarshalable
	})
	if err == nil {
		t.Fatal("expected marshal error from request body build")
	}
}

func TestAnthropicComplete_RequestErrorWithCancelledContext(t *testing.T) {
	t.Parallel()
	a := NewAnthropic("key")
	a.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("refused")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := a.Complete(ctx, Request{Model: "claude-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "anthropic http") {
		t.Errorf("error %q should wrap anthropic http", err)
	}
}

func TestAnthropicComplete_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-json"))
	}))
	t.Cleanup(srv.Close)
	a := newTestAnthropic("key", srv)
	_, err := a.Complete(context.Background(), Request{Model: "claude-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode anthropic response") {
		t.Errorf("error %q should wrap decode", err)
	}
}

// --- Stream error paths ---

func TestAnthropicStream_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("build body error", func(t *testing.T) {
		t.Parallel()
		a := NewAnthropic("key")
		_, err := a.Stream(context.Background(), Request{
			Model:    "claude-test",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Tools:    []ToolDef{{Name: "t", InputSchema: make(chan int)}},
		}, func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected marshal error")
		}
	})

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		a := NewAnthropic("key")
		a.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("down")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := a.Stream(ctx, Request{Model: "claude-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}},
			func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected transport error")
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)
		a := newTestAnthropic("key", srv)
		_, err := a.Stream(context.Background(), Request{Model: "claude-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}},
			func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected status error")
		}
		if !strings.Contains(err.Error(), "400") {
			t.Errorf("error %q should carry status 400", err)
		}
	})
}

// --- doWithRetry: Retry-After + ctx deadline during backoff ---

func TestAnthropicDoWithRetry_RetryAfterHonouredUntilCtxDeadline(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "9")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	a := newTestAnthropic("key", srv)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Complete(ctx, Request{Model: "claude-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected ctx deadline during backoff")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want exactly 1", calls)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("retry slept past the context deadline")
	}
}

// --- buildRequestBody ---

func TestAnthropicBuildRequestBody_TemperatureAndDefaults(t *testing.T) {
	t.Parallel()
	temp := 0.1
	a := NewAnthropic("key")
	body, err := a.buildRequestBody(Request{
		Model:       "claude-test",
		Temperature: &temp,
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["temperature"] != 0.1 {
		t.Errorf("temperature = %v, want 0.1", got["temperature"])
	}
	if got["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens default = %v, want 4096", got["max_tokens"])
	}
	if _, hasStream := got["stream"]; hasStream {
		t.Error("non-streaming body must not set stream")
	}
}

// --- toResponse ---

func TestAnthropicToResponse_MaxTokensStop(t *testing.T) {
	t.Parallel()
	r := &anthropicResponse{StopReason: "max_tokens"}
	if got := r.toResponse().StopReason; got != StopMaxToks {
		t.Errorf("stop = %v, want max_tokens", got)
	}
}

func TestAnthropicToResponse_UnmarshalableToolInputFallsBack(t *testing.T) {
	t.Parallel()
	r := &anthropicResponse{
		StopReason: "tool_use",
		Content: []anthropicContentBlock{{
			Type:  "tool_use",
			ID:    "tu_1",
			Name:  "broken",
			Input: make(chan int), // cannot marshal
		}},
	}
	resp := r.toResponse()
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Input != "{}" {
		t.Errorf("input = %q, want {} fallback", resp.ToolCalls[0].Input)
	}
}

// --- toAnthropicMessage ---

func TestToAnthropicMessage_InvalidToolInputFallsBack(t *testing.T) {
	t.Parallel()
	m := toAnthropicMessage(Message{
		Role:      RoleAssistant,
		ToolCalls: []ToolCall{{ID: "tc1", Name: "f", Input: "{not json"}},
	})
	blocks, ok := m.Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one block", m.Content)
	}
	input, ok := blocks[0].Input.(map[string]any)
	if !ok || len(input) != 0 {
		t.Errorf("input = %#v, want empty map fallback", blocks[0].Input)
	}
}

// --- parseSSEStream edge cases ---

func TestAnthropicParseSSEStream_EdgeCases(t *testing.T) {
	t.Parallel()
	a := NewAnthropic("key")

	sse := strings.Join([]string{
		`event: ignored`,
		`data: {malformed`,
		`data: {"type":"content_block_delta"}`, // nil delta → skipped
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"message_delta","delta":{"type":"x","stop_reason":"max_tokens"},"usage":{"output_tokens":12}}`,
		`data: {"type":"message_stop"}`,
		`data: [DONE]`,
	}, "\n")

	var texts []string
	resp, err := a.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
		if ev.Type == "text" {
			texts = append(texts, ev.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(texts) != 1 || texts[0] != "hi" {
		t.Errorf("texts = %v", texts)
	}
	if resp.StopReason != StopMaxToks {
		t.Errorf("stop = %v, want max_tokens", resp.StopReason)
	}
	if resp.OutputToks != 12 {
		t.Errorf("output toks = %d, want 12", resp.OutputToks)
	}
}

func TestAnthropicParseSSEStream_HandlerErrors(t *testing.T) {
	t.Parallel()
	a := NewAnthropic("key")
	boom := errors.New("handler boom")

	t.Run("text handler error aborts", func(t *testing.T) {
		t.Parallel()
		sse := `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`
		_, err := a.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
			if ev.Type == "text" {
				return boom
			}
			return nil
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want handler error", err)
		}
	})

	t.Run("tool_call handler error aborts", func(t *testing.T) {
		t.Parallel()
		sse := strings.Join([]string{
			`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"tu1","name":"f"}}`,
			`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`data: {"type":"content_block_stop"}`,
		}, "\n")
		_, err := a.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
			if ev.Type == "tool_call" {
				if ev.ToolCall == nil || ev.ToolCall.ID != "tu1" || ev.ToolCall.Input != "{}" {
					t.Errorf("tool call = %+v", ev.ToolCall)
				}
				return boom
			}
			return nil
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want handler error", err)
		}
	})

	t.Run("done handler error propagates", func(t *testing.T) {
		t.Parallel()
		_, err := a.parseSSEStream(strings.NewReader("data: [DONE]"), func(ev StreamEvent) error {
			if ev.Type == "done" {
				return boom
			}
			return nil
		})
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want handler error", err)
		}
	})
}
