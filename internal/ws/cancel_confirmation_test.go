package ws

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type confirmationHandler struct {
	started            chan context.Context
	release            chan struct{}
	ignoreCancellation bool
}

func (h *confirmationHandler) HandleChatMessage(ctx context.Context, _ string, _ string, _ string, emit func(ChatEvent), _ ...ChatMessageOption) error {
	h.started <- ctx
	<-h.release
	if h.ignoreCancellation {
		emit(ChatEvent{Type: "done"})
		return nil
	}
	if ctx.Err() != nil {
		return ErrRunCancelled
	}
	return nil
}
func requestCancel(c *Client, chat, request string) {
	b, _ := json.Marshal(map[string]string{"session_id": chat, "request_id": request})
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: b})
}
func readCancelResult(t *testing.T, c *Client, request string) bool {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case b := <-c.send:
			var result struct {
				Type    string `json:"type"`
				Payload struct {
					RequestID string `json:"request_id"`
					ChatID    string `json:"session_id"`
					Cancelled bool   `json:"cancelled"`
				} `json:"payload"`
			}
			if json.Unmarshal(b, &result) != nil || result.Type != "cancel_result" {
				continue
			}
			if result.Payload.RequestID != request || result.Payload.ChatID != "s1" {
				t.Fatalf("uncorrelated result: %s", b)
			}
			return result.Payload.Cancelled
		case <-deadline:
			t.Fatal("no cancellation result")
			return false
		}
	}
}
func TestCancelConfirmationWaitsForExactRunCompletion(t *testing.T) {
	for _, ignores := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "natural-completion"}[ignores], func(t *testing.T) {
			hub := newRunningHub(t)
			h := &confirmationHandler{started: make(chan context.Context, 1), release: make(chan struct{}), ignoreCancellation: ignores}
			hub.SetChatHandler(h)
			origin := newClient(t, hub, "u1")
			caller := newClient(t, hub, "u1")
			other := newClient(t, hub, "u2")
			origin.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"hello"}`)})
			var ctx context.Context
			select {
			case ctx = <-h.started:
			case <-time.After(time.Second):
				t.Fatal("handler not started")
			}
			requestCancel(other, "s1", "other-request")
			if readCancelResult(t, other, "other-request") || ctx.Err() != nil {
				t.Fatal("another user cancelled the owner's run")
			}
			requestCancel(caller, "s1", "my-request")
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("own run context not cancelled")
			}
			select {
			case b := <-caller.send:
				t.Fatalf("confirmation before handler exit: %s", b)
			case <-time.After(20 * time.Millisecond):
			}
			close(h.release)
			if got := readCancelResult(t, caller, "my-request"); got == ignores {
				t.Fatalf("cancelled=%v for ignoreCancellation=%v", got, ignores)
			}
			// The original run has left the registry before its completion reply.
			requestCancel(caller, "s1", "already-finished")
			if readCancelResult(t, caller, "already-finished") {
				t.Fatal("completed run confirmed cancelled")
			}
			// A new run on the same caller/chat cannot satisfy the old request: it has
			// a new completion object and a separately correlated cancellation reply.
			next := &confirmationHandler{started: make(chan context.Context, 1), release: make(chan struct{})}
			hub.SetChatHandler(next)
			origin.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"next"}`)})
			select {
			case <-next.started:
			case <-time.After(time.Second):
				t.Fatal("next run not started")
			}
			requestCancel(caller, "s1", "new-request")
			close(next.release)
			if !readCancelResult(t, caller, "new-request") {
				t.Fatal("new own run not confirmed")
			}
		})
	}
}

func TestCancelConfirmationOriginOnlyCannotStopReplacementRun(t *testing.T) {
	hub := newRunningHub(t)
	old := newClient(t, hub, "u1")
	first := &confirmationHandler{started: make(chan context.Context, 1), release: make(chan struct{}), ignoreCancellation: true}
	hub.SetChatHandler(first)
	old.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"old"}`)})
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("old run not started")
	}
	close(first.release)
	waitFor(t, func() bool { hub.cancelMu.Lock(); defer hub.cancelMu.Unlock(); return len(hub.cancelFns) == 0 }, "old run finished")
	replacement := newClient(t, hub, "u1")
	next := &confirmationHandler{started: make(chan context.Context, 1), release: make(chan struct{})}
	hub.SetChatHandler(next)
	replacement.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"new"}`)})
	var live context.Context
	select {
	case live = <-next.started:
	case <-time.After(time.Second):
		t.Fatal("replacement not started")
	}
	old.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: json.RawMessage(`{"session_id":"s1","request_id":"old-signal","origin_only":true}`)})
	if readCancelResult(t, old, "old-signal") || live.Err() != nil {
		t.Fatal("old signal cancelled or confirmed the replacement run")
	}
	replacement.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: json.RawMessage(`{"session_id":"s1","request_id":"new-signal","origin_only":true}`)})
	close(next.release)
	if !readCancelResult(t, replacement, "new-signal") {
		t.Fatal("replacement could not cancel its own invocation")
	}
}
