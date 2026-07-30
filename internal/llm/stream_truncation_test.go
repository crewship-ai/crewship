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

// This file covers the silent-truncation gap found in the 2026-07-30 audit:
// internal/llm/anthropic.go and internal/llm/openai.go read an SSE stream and,
// until this fix, treated a cleanly-closed connection with no terminal event
// (no "message_stop" for Anthropic, no chunk with a "finish_reason" for
// OpenAI) exactly like a normal completion — returning nil error with
// whatever partial content had arrived. A network blip or load-shedding event
// mid-generation would silently hand the caller truncated content marked as
// success.
//
// Three situations must stay distinguishable:
//   - a full stream (terminal event received)            -> success, full content
//   - a stream that ends without a terminal event         -> error (the bug)
//   - a context.Canceled (deliberate cancellation)         -> stays a cancellation,
//     never relabeled as a provider/stream error
//
// flushingHandler wraps an http.HandlerFunc body with a helper that writes and
// flushes immediately, so the client sees each line as it's sent instead of
// buffered until the handler returns.
func sseServer(t *testing.T, write func(w http.ResponseWriter, flush func())) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		write(w, flusher.Flush)
	}))
}

// --- Anthropic ---

// TestAnthropicStream_TruncatedConnection_ReturnsError is the red test: the
// server sends valid, partial content (message_start + a couple of text
// deltas) and then returns from the handler without ever sending
// "message_stop" or "[DONE]". From the client's point of view this looks
// exactly like a normal chunked-response end (clean io.EOF) — which is
// precisely how a mid-generation network cut or load-shed present. The
// caller must get an error, not nil with truncated content.
func TestAnthropicStream_TruncatedConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte("event: message_start\n" +
			`data: {"type":"message_start","message":{"usage":{"input_tokens":12,"output_tokens":0}}}` + "\n\n"))
		flush()
		_, _ = w.Write([]byte("event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"))
		flush()
		_, _ = w.Write([]byte("event: content_block_delta\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"func main() {"}}` + "\n\n"))
		flush()
		// No content_block_stop, no message_delta, no message_stop, no [DONE].
		// The handler just returns — connection closes cleanly.
	})
	defer srv.Close()

	p := newTestAnthropic("k", srv)
	var gotText strings.Builder
	resp, err := p.Stream(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "write code"}},
	}, func(e StreamEvent) error {
		if e.Type == "text" {
			gotText.WriteString(e.Content)
		}
		return nil
	})
	if err == nil {
		t.Fatalf("expected error for truncated stream, got nil (resp=%+v)", resp)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("truncated stream must not look like a cancellation: %v", err)
	}
	if gotText.String() != "func main() {" {
		t.Errorf("handler should still see the partial text that did arrive, got %q", gotText.String())
	}
	t.Logf("truncated stream error: %v", err)
}

// TestAnthropicStream_EmptyConnection_ReturnsError is the boundary case: the
// connection dies before a single byte of content arrives. This must be at
// least as loud an error as the "some content, then cut" case above — a
// silently-empty "success" is the worst possible outcome.
func TestAnthropicStream_EmptyConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		// Nothing written at all; handler returns immediately.
	})
	defer srv.Close()

	p := newTestAnthropic("k", srv)
	resp, err := p.Stream(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected error for a stream with zero content, got nil (resp=%+v)", resp)
	}
}

// TestAnthropicStream_CompleteConnection_ReturnsSuccess is the control: a
// stream that ends WITH message_stop must still succeed with full content.
// Without this, the two tests above wouldn't prove the fix distinguishes
// truncation from normal completion.
func TestAnthropicStream_CompleteConnection_ReturnsSuccess(t *testing.T) {
	t.Parallel()
	const sse = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"all done"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

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
		t.Fatalf("complete stream should succeed, got: %v", err)
	}
	if resp.Content != "all done" {
		t.Errorf("content = %q, want %q", resp.Content, "all done")
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop reason = %q, want end_turn", resp.StopReason)
	}
}

// TestAnthropicStream_ContextCanceled_StaysCancellation makes sure the fix
// for the truncation gap does not relabel a deliberate cancellation as a
// provider/stream error. The server sends one event and then blocks until the
// client disconnects (simulating a long-running generation); the test cancels
// the context while the client is mid-read.
func TestAnthropicStream_ContextCanceled_StaysCancellation(t *testing.T) {
	t.Parallel()
	blockUntilDisconnect := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte("event: content_block_start\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"))
		flush()
		<-blockUntilDisconnect
	})
	defer srv.Close()
	defer close(blockUntilDisconnect)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(75 * time.Millisecond)
		cancel()
	}()

	p := newTestAnthropic("k", srv)
	_, err := p.Stream(ctx, Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected an error from the canceled stream")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled stream must surface as context.Canceled, got: %v", err)
	}
}

// --- OpenAI ---

// TestOpenAIStream_TruncatedConnection_ReturnsError mirrors the Anthropic
// case: content deltas arrive, then the connection closes cleanly with no
// chunk ever carrying a finish_reason and no "[DONE]" sentinel.
func TestOpenAIStream_TruncatedConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
		flush()
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":" output"}}]}` + "\n\n"))
		flush()
		// No finish_reason chunk, no [DONE]. Handler returns; clean EOF.
	})
	defer srv.Close()

	p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	var gotText strings.Builder
	resp, err := p.Stream(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "ping"}},
	}, func(e StreamEvent) error {
		if e.Type == "text" {
			gotText.WriteString(e.Content)
		}
		return nil
	})
	if err == nil {
		t.Fatalf("expected error for truncated stream, got nil (resp=%+v)", resp)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("truncated stream must not look like a cancellation: %v", err)
	}
	if gotText.String() != "partial output" {
		t.Errorf("handler should still see the partial text that did arrive, got %q", gotText.String())
	}
	t.Logf("truncated stream error: %v", err)
}

// TestOpenAIStream_EmptyConnection_ReturnsError: connection dies before any
// content chunk arrives.
func TestOpenAIStream_EmptyConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		// Nothing written; handler returns immediately.
	})
	defer srv.Close()

	p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	resp, err := p.Stream(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected error for a stream with zero content, got nil (resp=%+v)", resp)
	}
}

// TestOpenAIStream_CompleteConnection_ReturnsSuccess is the control: a stream
// ending with a finish_reason chunk (and [DONE]) must succeed with full
// content.
func TestOpenAIStream_CompleteConnection_ReturnsSuccess(t *testing.T) {
	t.Parallel()
	const sse = `data: {"choices":[{"delta":{"content":"all"}}]}

data: {"choices":[{"delta":{"content":" done"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}

data: [DONE]
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	resp, err := p.Stream(context.Background(), Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err != nil {
		t.Fatalf("complete stream should succeed, got: %v", err)
	}
	if resp.Content != "all done" {
		t.Errorf("content = %q, want %q", resp.Content, "all done")
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop reason = %q, want end_turn", resp.StopReason)
	}
}

// TestOpenAIStream_ContextCanceled_StaysCancellation: same guard as the
// Anthropic version — cancellation must never come back looking like a
// truncated-stream provider error.
func TestOpenAIStream_ContextCanceled_StaysCancellation(t *testing.T) {
	t.Parallel()
	blockUntilDisconnect := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
		flush()
		<-blockUntilDisconnect
	})
	defer srv.Close()
	defer close(blockUntilDisconnect)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(75 * time.Millisecond)
		cancel()
	}()

	p := NewOpenAIWithBaseURL("k", srv.URL+"/v1/chat/completions")
	_, err := p.Stream(ctx, Request{
		Model:    "gpt-4o-mini",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected an error from the canceled stream")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled stream must surface as context.Canceled, got: %v", err)
	}
}
