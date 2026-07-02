package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// captureCtxHandler records the context a run is invoked with and blocks until
// released, so a test can inspect the run context while the run is "in flight".
type captureCtxHandler struct {
	got     chan context.Context
	release chan struct{}
}

func (h *captureCtxHandler) HandleChatMessage(ctx context.Context, userID, sessionID, content string, streamFn func(event ChatEvent), opts ...ChatMessageOption) error {
	h.got <- ctx
	<-h.release
	return nil
}

// TestRunSurvivesClientDisconnect pins the core fix: an in-flight agent run must
// NOT be cancelled when the originating client's socket goes away (navigating
// away, refresh, network blip). Previously runCtx descended from c.ctx, so a
// disconnect aborted generation mid-stream and the reply was lost. It now
// descends from the hub's server-lifetime context; only an explicit Stop
// (cancelFns) cancels it.
func TestRunSurvivesClientDisconnect(t *testing.T) {
	hub := newRunningHub(t)
	h := &captureCtxHandler{got: make(chan context.Context, 1), release: make(chan struct{})}
	hub.SetChatHandler(h)
	defer close(h.release)

	c := newClient(t, hub, "u1")
	c.handleSendMessage(ClientMessage{
		Type:    "send_message",
		Payload: json.RawMessage(`{"session_id":"s1","content":"hi"}`),
	})

	var runCtx context.Context
	select {
	case runCtx = <-h.got:
	case <-time.After(time.Second):
		t.Fatal("chat handler was never invoked")
	}

	// Simulate the client navigating away: its socket context is cancelled.
	c.cancel()
	time.Sleep(30 * time.Millisecond)

	if runCtx.Err() != nil {
		t.Fatalf("run context was cancelled by client disconnect (%v) — the run must survive to finish + persist server-side", runCtx.Err())
	}
}

// TestExplicitStopCancelsRun pins the other half: after decoupling from the
// socket, an explicit Stop (cancel_message → cancelFns) must STILL cancel the
// run, so the decouple didn't neuter the stop button.
func TestExplicitStopCancelsRun(t *testing.T) {
	hub := newRunningHub(t)
	h := &captureCtxHandler{got: make(chan context.Context, 1), release: make(chan struct{})}
	hub.SetChatHandler(h)
	defer close(h.release)

	c := newClient(t, hub, "u1")
	c.handleSendMessage(ClientMessage{
		Type:    "send_message",
		Payload: json.RawMessage(`{"session_id":"s1","content":"hi"}`),
	})

	var runCtx context.Context
	select {
	case runCtx = <-h.got:
	case <-time.After(time.Second):
		t.Fatal("chat handler was never invoked")
	}

	// Explicit stop via the same path the frontend's cancel_message uses.
	c.handleCancelMessage(ClientMessage{
		Type:    "cancel_message",
		Payload: json.RawMessage(`{"session_id":"s1"}`),
	})

	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit Stop did not cancel the run context")
	}
}
