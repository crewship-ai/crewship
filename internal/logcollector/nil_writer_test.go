package logcollector

import "testing"

// A nil *Writer is a legitimate state: every call site that owns one treats it
// as an optional dependency (`if s.logWriter != nil { ... }` in scheduler,
// pipeline and routes_agent), and two sites — internal/api/webhook.go and
// internal/chatbridge/bridge.go — build an OutputBuffer from it without that
// check. Before the guard, those two paths dereferenced nil inside
// Writer.Append and took the whole daemon down from a webhook handler.
//
// These tests pin the nil path down at the type that actually dereferences,
// so no future call site has to remember the check.

func TestWriter_NilReceiver_DoesNotPanic(t *testing.T) {
	var w *Writer

	if err := w.Append("crew-1", "agent-1", LogEntry{Event: "text", Content: "hi"}); err != nil {
		t.Fatalf("Append on nil Writer: want nil error, got %v", err)
	}
	// Flush and Close take the same lock and must be equally safe: both are
	// reached from deferred cleanup, where a panic is hardest to attribute.
	w.Flush()
	w.Close()
}

func TestOutputBuffer_NilWriter_DoesNotPanic(t *testing.T) {
	ob := NewOutputBuffer(nil, "crew", "agent")
	if ob == nil {
		t.Fatal("NewOutputBuffer(nil, ...) returned nil")
	}

	// Non-streamed events go straight through to Writer.Append.
	if err := ob.Append(LogEntry{Event: "result", Content: "done"}); err != nil {
		t.Fatalf("Append(result) on nil-writer buffer: want nil error, got %v", err)
	}
	// Streamed events buffer first, then reach Writer.Append via flushLocked —
	// a newline forces that flush inside the same call.
	if err := ob.Append(LogEntry{Event: "text", Content: "line\n"}); err != nil {
		t.Fatalf("Append(text) on nil-writer buffer: want nil error, got %v", err)
	}
	// Buffered-then-closed is the third route into Writer.Append.
	if err := ob.Append(LogEntry{Event: "output", Content: "trailing"}); err != nil {
		t.Fatalf("Append(output) on nil-writer buffer: want nil error, got %v", err)
	}
	ob.Close()
}

func TestOutputBuffer_NilReceiver_DoesNotPanic(t *testing.T) {
	// OutputBuffer is passed around as an optional dependency too
	// (orchestrator.BufferingHandlerOpts.LogBuf is documented "when non-nil"),
	// so the nil buffer itself must be inert rather than fatal.
	var ob *OutputBuffer

	if err := ob.Append(LogEntry{Event: "text", Content: "hi"}); err != nil {
		t.Fatalf("Append on nil OutputBuffer: want nil error, got %v", err)
	}
	ob.Close()
}
