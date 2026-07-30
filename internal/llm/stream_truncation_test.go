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
// internal/llm/anthropic.go, internal/llm/openai.go, and internal/llm/ollama.go
// each read a streaming response and, until this fix, treated a cleanly-closed
// connection with no terminal event (no "message_stop" for Anthropic, no
// chunk with a "finish_reason" for OpenAI, no NDJSON line with "done":true for
// Ollama) exactly like a normal completion — returning nil error with
// whatever partial content had arrived. A network blip or load-shedding event
// mid-generation would silently hand the caller truncated content marked as
// success. Ollama's version of the bug was worse: its final Response.Content
// and Response.Thinking were only assembled *inside* the "done":true branch,
// so a truncated stream didn't just mislabel success — it dropped the
// aggregated content to "" entirely, even though the individual "text"
// StreamEvents had already reached the handler.
//
// Three situations must stay distinguishable:
//   - a full stream (terminal event received)            -> success, full content
//   - a stream that ends without a terminal event         -> error (the bug)
//   - a context.Canceled (deliberate cancellation)         -> stays a cancellation,
//     never relabeled as a provider/stream error
//
// sseServer wraps an http.HandlerFunc body with a helper that writes and
// flushes immediately, so the client sees each line as it's sent instead of
// buffered until the handler returns. Used for Anthropic/OpenAI's SSE and
// (with a different Content-Type) Ollama's NDJSON — neither provider's
// parser actually branches on Content-Type, so the same helper serves both.
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

// ndjsonServer is sseServer's Ollama-flavored twin: same flush-per-write
// shape, application/x-ndjson header for clarity (parseNDJSONStream doesn't
// actually check it).
func ndjsonServer(t *testing.T, write func(w http.ResponseWriter, flush func())) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
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

// TestAnthropicStream_ErrorEvent_SurfacesProviderReason checks that an
// explicit Anthropic "error" event (e.g. overloaded_error) mid-stream
// reports its own type/message, rather than falling through to the generic
// "stream ended without a message_stop event" text once the connection
// closes right after. The generic message would still be an error -- an
// improvement over the pre-fix silent success -- but it would hide what the
// provider actually said.
func TestAnthropicStream_ErrorEvent_SurfacesProviderReason(t *testing.T) {
	t.Parallel()
	const sse = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	p := newTestAnthropic("k", srv)
	_, err := p.Stream(context.Background(), Request{
		Model:    "claude-3-5-haiku-20241022",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected an error from the error event")
	}
	if !strings.Contains(err.Error(), "overloaded_error") || !strings.Contains(err.Error(), "Overloaded") {
		t.Errorf("error must surface the provider's own type/message, got: %v", err)
	}
	if strings.Contains(err.Error(), "message_stop") {
		t.Errorf("error event must not fall through to the generic truncation message, got: %v", err)
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

// TestOpenAIStream_DoneSentinelWithoutFinishReason_ReturnsSuccess covers a
// real OpenAI-compatible shape that isn't hypothetical: the openai_compat
// governance path (NewOpenAIWithClient, internal/api/gov_model_resolver.go)
// dials arbitrary tenant-configured endpoints -- self-hosted vLLM,
// llama.cpp server, LM Studio, TGI, LocalAI. Real OpenAI always sends
// finish_reason on the last chunk before "[DONE]", but some of those
// self-hosted backends send only the "[DONE]" sentinel and never populate
// finish_reason on any chunk. "[DONE]" is itself a positive end-of-stream
// signal per the Chat Completions SSE protocol (forEachSSEData already
// breaks its scan on it) -- a stream that ends with it is complete, not
// truncated, even with no finish_reason chunk. Getting this wrong is worse
// than a bad error message: the synthesized error text contains "eof", which
// executor_retry.go's transientErrorMarkers matches, so every one of these
// backends would retry (and double-bill) every successful call.
func TestOpenAIStream_DoneSentinelWithoutFinishReason_ReturnsSuccess(t *testing.T) {
	t.Parallel()
	const sse = `data: {"choices":[{"delta":{"content":"all"},"finish_reason":null}]}

data: {"choices":[{"delta":{"content":" done"},"finish_reason":null}]}

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
		t.Fatalf("a stream ending in [DONE] without finish_reason must succeed, got: %v", err)
	}
	if resp.Content != "all done" {
		t.Errorf("content = %q, want %q", resp.Content, "all done")
	}
}

// TestOpenAIStream_IncludeUsageTrailingChunk_DoesNotFalsePositive is the
// converse check: OpenAI's stream_options.include_usage feature appends one
// extra chunk AFTER the finish_reason chunk, carrying only usage and an empty
// choices array (no delta, no finish_reason on that trailing chunk). That
// trailing chunk must not itself need to look "terminal" -- the earlier
// finish_reason chunk already satisfied that, and the empty choices array
// must not trip any nil-dereference or false-truncation path.
func TestOpenAIStream_IncludeUsageTrailingChunk_DoesNotFalsePositive(t *testing.T) {
	t.Parallel()
	const sse = `data: {"choices":[{"delta":{"content":"hi"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1}}

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
		t.Fatalf("include_usage trailing chunk must not cause a false truncation error, got: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("content = %q, want %q", resp.Content, "hi")
	}
	if resp.InputToks != 3 || resp.OutputToks != 1 {
		t.Errorf("usage not captured from trailing chunk: input=%d output=%d", resp.InputToks, resp.OutputToks)
	}
}

// --- Ollama ---

// TestOllamaStream_TruncatedConnection_ReturnsError mirrors the Anthropic and
// OpenAI cases: NDJSON content lines arrive, then the connection closes
// cleanly with no line ever carrying "done":true. Before the fix this wasn't
// just mislabeled as success — Response.Content/Thinking were only ever
// assembled inside the "done":true branch, so the aggregated content came
// back completely empty even though the individual "text" StreamEvents had
// already reached the handler.
func TestOllamaStream_TruncatedConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := ndjsonServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"partial"}}` + "\n"))
		flush()
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":" output"}}` + "\n"))
		flush()
		// No "done":true line. Handler returns; connection closes cleanly.
	})
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")
	var gotText strings.Builder
	resp, err := p.Stream(context.Background(), Request{
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
	if resp.Content != "partial output" {
		t.Errorf("final Response.Content must still carry the partial text (the pre-fix bug dropped it to \"\"), got %q", resp.Content)
	}
	t.Logf("truncated stream error: %v", err)
}

// TestOllamaStream_EmptyConnection_ReturnsError: connection dies before any
// content line arrives.
func TestOllamaStream_EmptyConnection_ReturnsError(t *testing.T) {
	t.Parallel()
	srv := ndjsonServer(t, func(w http.ResponseWriter, flush func()) {
		// Nothing written; handler returns immediately.
	})
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")
	resp, err := p.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("expected error for a stream with zero content, got nil (resp=%+v)", resp)
	}
}

// TestOllamaStream_CompleteConnection_ReturnsSuccess is the control: a stream
// ending with a "done":true line must still succeed with full content.
func TestOllamaStream_CompleteConnection_ReturnsSuccess(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"message":{"role":"assistant","content":"all"}}`,
		`{"message":{"role":"assistant","content":" done"}}`,
		`{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":3,"eval_count":2}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n"))
		}
	}))
	defer srv.Close()

	p := NewOllama(srv.URL, "llama3")
	resp, err := p.Stream(context.Background(), Request{
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

// TestOllamaStream_ContextCanceled_StaysCancellation: same guard as the other
// two providers — cancellation must never come back looking like a
// truncated-stream error.
func TestOllamaStream_ContextCanceled_StaysCancellation(t *testing.T) {
	t.Parallel()
	blockUntilDisconnect := make(chan struct{})
	srv := ndjsonServer(t, func(w http.ResponseWriter, flush func()) {
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"partial"}}` + "\n"))
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

	p := NewOllama(srv.URL, "llama3")
	_, err := p.Stream(ctx, Request{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("expected an error from the canceled stream")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled stream must surface as context.Canceled, got: %v", err)
	}
}
