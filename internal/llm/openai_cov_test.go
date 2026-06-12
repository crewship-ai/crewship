package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- ListModels error paths ---

func TestOpenAIListModels_BadBaseURL(t *testing.T) {
	t.Parallel()
	o := NewOpenAIWithBaseURL("key", "http://bad\x7f.example/v1/chat/completions")
	_, err := o.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected parse error for control character in base URL")
	}
	if !strings.Contains(err.Error(), "parse base url") {
		t.Errorf("error %q should wrap parse base url", err)
	}
}

func TestOpenAIListModels_TransportError(t *testing.T) {
	t.Parallel()
	o := NewOpenAIWithBaseURL("key", "http://example.invalid/v1/chat/completions")
	o.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("conn refused")
	})
	_, err := o.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !strings.Contains(err.Error(), "openai http") {
		t.Errorf("error %q should wrap openai http", err)
	}
}

func TestOpenAIListModels_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-json"))
	}))
	t.Cleanup(srv.Close)
	o := NewOpenAIWithBaseURL("key", srv.URL+"/v1/chat/completions")
	_, err := o.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode openai models") {
		t.Errorf("error %q should wrap decode", err)
	}
}

// --- Complete / Stream error paths ---

func TestOpenAIComplete_BuildBodyError(t *testing.T) {
	t.Parallel()
	o := NewOpenAIWithBaseURL("key", "http://example.invalid")
	_, err := o.Complete(context.Background(), Request{
		Model:    "gpt-test",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolDef{{Name: "t", InputSchema: make(chan int)}}, // unmarshalable
	})
	if err == nil {
		t.Fatal("expected marshal error from request body build")
	}
}

func TestOpenAIComplete_RequestErrorWithCancelledContext(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	o := NewOpenAIWithBaseURL("key", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Do fails immediately and ctx.Err() != nil stops the retry loop
	_, err := o.Complete(ctx, Request{Model: "gpt-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !strings.Contains(err.Error(), "openai http") {
		t.Errorf("error %q should wrap openai http", err)
	}
}

func TestOpenAIComplete_DecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("not-json"))
	}))
	t.Cleanup(srv.Close)
	o := NewOpenAIWithBaseURL("key", srv.URL)
	_, err := o.Complete(context.Background(), Request{Model: "gpt-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode openai response") {
		t.Errorf("error %q should wrap decode", err)
	}
}

func TestOpenAIStream_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("build body error", func(t *testing.T) {
		t.Parallel()
		o := NewOpenAIWithBaseURL("key", "http://example.invalid")
		_, err := o.Stream(context.Background(), Request{
			Model:    "gpt-test",
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
			Tools:    []ToolDef{{Name: "t", InputSchema: make(chan int)}},
		}, func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected marshal error")
		}
	})

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		o := NewOpenAIWithBaseURL("key", "http://example.invalid")
		o.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("down")
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := o.Stream(ctx, Request{Model: "gpt-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}},
			func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected transport error")
		}
	})

	t.Run("non-200 status", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)
		o := NewOpenAIWithBaseURL("key", srv.URL)
		_, err := o.Stream(context.Background(), Request{Model: "gpt-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}},
			func(StreamEvent) error { return nil })
		if err == nil {
			t.Fatal("expected status error")
		}
		if !strings.Contains(err.Error(), "400") {
			t.Errorf("error %q should carry status 400", err)
		}
	})
}

// --- doWithRetry: Retry-After + context deadline during backoff ---

func TestOpenAIDoWithRetry_RetryAfterHonouredUntilCtxDeadline(t *testing.T) {
	t.Parallel()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "7") // > 1s base delay → delay takes Retry-After
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	o := NewOpenAIWithBaseURL("key", srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := o.Complete(ctx, Request{Model: "gpt-test", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatal("expected ctx deadline error during backoff")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want exactly 1 (backoff interrupted)", calls)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("retry must not sleep the full Retry-After when ctx dies first")
	}
}

func TestOpenAIDoWithRetry_BadBaseURLFailsRequestBuild(t *testing.T) {
	t.Parallel()
	o := NewOpenAIWithBaseURL("key", "http://bad\x7f.example")
	_, err := o.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("expected request build error")
	}
	if !strings.Contains(err.Error(), "create request") {
		t.Errorf("error %q should wrap create request", err)
	}
}

// --- buildRequestBody knobs ---

func TestOpenAIBuildRequestBody_AllFields(t *testing.T) {
	t.Parallel()
	temp := 0.3
	o := NewOpenAI("key")
	body, err := o.buildRequestBody(Request{
		Model:       "gpt-test",
		System:      "be brief",
		MaxTokens:   77,
		Temperature: &temp,
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		Tools:       []ToolDef{{Name: "lookup", Description: "d", InputSchema: map[string]any{"type": "object"}}},
	}, true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		`"role":"system"`, `"content":"be brief"`,
		`"max_tokens":77`, `"temperature":0.3`,
		`"stream":true`, `"name":"lookup"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("body %s missing %s", s, want)
		}
	}
}

// --- toResponse edge cases ---

func TestOpenAIToResponse_NoChoices(t *testing.T) {
	t.Parallel()
	r := &openaiResponse{}
	r.Usage.PromptTokens = 5
	resp := r.toResponse()
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop = %v, want end_turn", resp.StopReason)
	}
	if resp.InputToks != 5 {
		t.Errorf("input toks = %d, want 5", resp.InputToks)
	}
	if resp.Content != "" || len(resp.ToolCalls) != 0 {
		t.Errorf("empty response must carry no content/tool calls: %+v", resp)
	}
}

func TestOpenAIToResponse_LengthFinishReason(t *testing.T) {
	t.Parallel()
	var r openaiResponse
	r.Choices = make([]struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}, 1)
	r.Choices[0].FinishReason = "length"
	r.Choices[0].Message.Content = "truncat"
	resp := r.toResponse()
	if resp.StopReason != StopMaxToks {
		t.Errorf("stop = %v, want max_tokens", resp.StopReason)
	}
	if resp.Content != "truncat" {
		t.Errorf("content = %q", resp.Content)
	}
}

// --- parseSSEStream edge cases ---

func TestOpenAIParseSSEStream_EdgeCases(t *testing.T) {
	t.Parallel()
	o := NewOpenAI("key")

	sse := strings.Join([]string{
		`: comment line ignored`,
		`data: {malformed json`,
		`data: {"choices":[]}`,
		`data: {"choices":[{"delta":{"content":"part1"},"finish_reason":""}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","function":{"name":"f0","arguments":"{\"a\""}}]},"finish_reason":""}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":1}"}},{"index":2,"id":"call_2","function":{"name":"f2","arguments":"{}"}}]},"finish_reason":""}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"length"}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	}, "\n")

	var texts []string
	var toolCalls []ToolCall
	var done int
	resp, err := o.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
		switch ev.Type {
		case "text":
			texts = append(texts, ev.Content)
		case "tool_call":
			toolCalls = append(toolCalls, *ev.ToolCall)
		case "done":
			done++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(texts) != 1 || texts[0] != "part1" {
		t.Errorf("texts = %v", texts)
	}
	// Index 1 is a gap in toolMap — it must be skipped, indices 0 and 2 kept.
	if len(toolCalls) != 1 {
		// toolMap iteration goes 0..len(toolMap)-1; with indices {0,2} the
		// loop sees i=1 as nil and stops short of i=2 by length — only the
		// fully-assembled call_0 is emitted.
		t.Fatalf("toolCalls = %+v, want exactly the assembled call_0", toolCalls)
	}
	if toolCalls[0].ID != "call_0" || toolCalls[0].Name != "f0" || toolCalls[0].Input != `{"a":1}` {
		t.Errorf("assembled tool call = %+v", toolCalls[0])
	}
	if done != 1 {
		t.Errorf("done events = %d, want 1", done)
	}
	// Last finish_reason wins: "stop" after "length".
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop = %v, want end_turn", resp.StopReason)
	}
}

func TestOpenAIParseSSEStream_HandlerErrors(t *testing.T) {
	t.Parallel()
	o := NewOpenAI("key")
	boom := errors.New("handler boom")

	t.Run("text handler error aborts", func(t *testing.T) {
		t.Parallel()
		sse := `data: {"choices":[{"delta":{"content":"x"},"finish_reason":""}]}`
		_, err := o.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
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
		sse := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		_, err := o.parseSSEStream(strings.NewReader(sse), func(ev StreamEvent) error {
			if ev.Type == "tool_call" {
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
		_, err := o.parseSSEStream(strings.NewReader("data: [DONE]"), func(ev StreamEvent) error {
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
