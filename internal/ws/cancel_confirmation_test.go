package ws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
			if early := cancelResults(drainCancelFrames(caller, 20*time.Millisecond)); len(early) != 0 {
				t.Fatalf("confirmation before handler exit: %+v", early)
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

type cancelFrame struct {
	Type    string `json:"type"`
	Payload struct {
		Type      string `json:"type"`
		Content   string `json:"content"`
		RequestID string `json:"request_id"`
		ChatID    string `json:"session_id"`
		Cancelled bool   `json:"cancelled"`
		Reason    string `json:"reason"`
	} `json:"payload"`
}

// drainFrames returns every frame queued on c within wait.
func drainCancelFrames(c *Client, wait time.Duration) []cancelFrame {
	var out []cancelFrame
	deadline := time.After(wait)
	for {
		select {
		case b := <-c.send:
			var f cancelFrame
			if json.Unmarshal(b, &f) == nil {
				out = append(out, f)
			}
		case <-deadline:
			return out
		}
	}
}

func cancelResults(frames []cancelFrame) map[string]cancelFrame {
	out := map[string]cancelFrame{}
	for _, f := range frames {
		if f.Type == "cancel_result" {
			out[f.Payload.RequestID] = f
		}
	}
	return out
}

// streamingCancelHandler emits trailing text after the stop and only then
// reports the confirmed cancellation.
type streamingCancelHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *streamingCancelHandler) HandleChatMessage(ctx context.Context, _ string, _ string, _ string, emit func(ChatEvent), _ ...ChatMessageOption) error {
	emit(ChatEvent{Type: "text", Content: "head"})
	h.started <- struct{}{}
	<-h.release
	emit(ChatEvent{Type: "text", Content: "tail"})
	if ctx.Err() != nil {
		return ErrRunCancelled
	}
	return nil
}

func startCancelRun(t *testing.T, hub *Hub, origin *Client, started <-chan struct{}) {
	t.Helper()
	origin.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"hello"}`)})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler not started")
	}
}

// Every requester of the same run — a duplicate request on one socket, or a
// second socket of the same user — receives the shared outcome once the run
// completes; none gets an early denial. Another user's socket can neither
// cancel the run nor observe the owner's confirmations.
func TestCancelConfirmationSharedAmongRequesters(t *testing.T) {
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	h := &streamingCancelHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
	hub.SetChatHandler(h)
	origin := newClient(t, hub, "u1")
	second := newClient(t, hub, "u1")
	other := newClient(t, hub, "u2")
	other.subscribe("session:s1")
	startCancelRun(t, hub, origin, h.started)

	requestCancel(origin, "s1", "first")
	requestCancel(origin, "s1", "duplicate")
	requestCancel(second, "s1", "second-socket")
	requestCancel(other, "s1", "intruder")

	early := cancelResults(drainCancelFrames(origin, 30*time.Millisecond))
	if len(early) != 0 {
		t.Fatalf("result before the run completed: %+v", early)
	}
	close(h.release)

	for _, tc := range []struct {
		client *Client
		ids    []string
	}{{origin, []string{"first", "duplicate"}}, {second, []string{"second-socket"}}} {
		got := cancelResults(drainCancelFrames(tc.client, 200*time.Millisecond))
		if len(got) != len(tc.ids) {
			t.Fatalf("results %+v want %v", got, tc.ids)
		}
		for _, id := range tc.ids {
			if r, ok := got[id]; !ok || !r.Payload.Cancelled || r.Payload.ChatID != "s1" {
				t.Fatalf("request %s: %+v", id, r)
			}
		}
	}
	otherFrames := drainCancelFrames(other, 50*time.Millisecond)
	results := cancelResults(otherFrames)
	if len(results) != 1 || results["intruder"].Payload.Cancelled || results["intruder"].Payload.Reason != cancelReasonNoActiveRun {
		t.Fatalf("other user's results: %+v", results)
	}
	for _, f := range otherFrames {
		if f.Type == "cancel_accepted" {
			t.Fatalf("other user accepted: %+v", f)
		}
	}
}

// A cancel_result follows every frame the run emitted to the requesting
// socket: the trailing text and the run's done are queued first.
func TestCancelResultOrderedAfterRunFrames(t *testing.T) {
	for _, name := range []string{"origin", "subscriber"} {
		t.Run(name, func(t *testing.T) {
			hub := newRunningHub(t)
			hub.SetChannelAuthorizer(allowAllAuthorizer{})
			h := &streamingCancelHandler{started: make(chan struct{}, 1), release: make(chan struct{})}
			hub.SetChatHandler(h)
			origin := newClient(t, hub, "u1")
			requester := origin
			if name == "subscriber" {
				requester = newClient(t, hub, "u1")
				requester.subscribe("session:s1")
			}
			startCancelRun(t, hub, origin, h.started)
			requestCancel(requester, "s1", "ordered")
			close(h.release)
			frames := drainCancelFrames(requester, 200*time.Millisecond)
			var order []string
			for _, f := range frames {
				switch {
				case f.Type == "chat_event" && f.Payload.Type == "text" && f.Payload.Content == "tail":
					order = append(order, "tail")
				case f.Type == "chat_event" && f.Payload.Type == "done":
					order = append(order, "done")
				case f.Type == "cancel_result":
					order = append(order, "result")
				}
			}
			if strings.Join(order, ",") != "tail,done,result" {
				t.Fatalf("frame order %v", order)
			}
		})
	}
}

// A handler that never returns cannot pin a waiter: the deadline answers the
// request with an explicit reason.
func TestCancelConfirmationDeadline(t *testing.T) {
	prev := cancelResultDeadline
	cancelResultDeadline = 20 * time.Millisecond
	t.Cleanup(func() { cancelResultDeadline = prev })
	hub := newRunningHub(t)
	h := &confirmationHandler{started: make(chan context.Context, 1), release: make(chan struct{})}
	hub.SetChatHandler(h)
	t.Cleanup(func() { close(h.release) })
	origin := newClient(t, hub, "u1")
	origin.handleSendMessage(ClientMessage{Type: "send_message", Payload: json.RawMessage(`{"session_id":"s1","content":"hello"}`)})
	select {
	case <-h.started:
	case <-time.After(time.Second):
		t.Fatal("handler not started")
	}
	requestCancel(origin, "s1", "hung")
	got := cancelResults(drainCancelFrames(origin, 200*time.Millisecond))
	r, ok := got["hung"]
	if !ok || r.Payload.Cancelled || r.Payload.Reason != cancelReasonDeadline {
		t.Fatalf("deadline result %+v", got)
	}
	origin.cancelWaitMu.Lock()
	defer origin.cancelWaitMu.Unlock()
	if len(origin.cancelWaiters) != 0 {
		t.Fatal("waiter retained after deadline")
	}
}
