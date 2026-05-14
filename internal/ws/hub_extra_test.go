package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/logging"
	"golang.org/x/net/websocket"
)

// defaultTestValidator returns a JWT validator initialised with a
// known test secret. Tests that need to forge tokens against the
// same key reach for this.
func defaultTestValidator(t *testing.T) *auth.JWTValidator {
	t.Helper()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return v
}

// ---------- Helpers ----------

// allowAllAuthorizer permits every CanSubscribe call.
type allowAllAuthorizer struct{}

func (allowAllAuthorizer) CanSubscribe(_ context.Context, _, _ string) bool { return true }

// denyAllAuthorizer rejects every CanSubscribe call.
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) CanSubscribe(_ context.Context, _, _ string) bool { return false }

// allowChannelsAuthorizer permits a fixed set of channels.
type allowChannelsAuthorizer struct {
	allowed map[string]bool
	calls   atomic.Int32
}

func (a *allowChannelsAuthorizer) CanSubscribe(_ context.Context, _, channel string) bool {
	a.calls.Add(1)
	return a.allowed[channel]
}

// stubChatHandler captures invocations and emits a configurable event sequence.
type stubChatHandler struct {
	mu     sync.Mutex
	calls  int
	events []ChatEvent
	err    error
	block  chan struct{} // if non-nil, HandleChatMessage waits until ctx cancels or block closes
	gotCtx context.Context
}

func (s *stubChatHandler) HandleChatMessage(ctx context.Context, _, _, _ string, stream func(event ChatEvent)) error {
	s.mu.Lock()
	s.calls++
	s.gotCtx = ctx
	events := s.events
	err := s.err
	block := s.block
	s.mu.Unlock()

	for _, e := range events {
		stream(e)
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	return err
}

// hubOpts lets a test swap in a custom validator or sessions store.
// Default: a fresh validator with the canonical test secret + a memory-
// backed sessions stub. Callers use withValidator(...) / withSessions(...)
// to override.
type hubOpts struct {
	validator *auth.JWTValidator
	sessions  sessions.Store
}

func withValidator(v *auth.JWTValidator) func(*hubOpts) {
	return func(o *hubOpts) { o.validator = v }
}

// newRunningHub starts a hub.Run in a goroutine and registers t.Cleanup to stop it.
// Tests get a working JWT validator + sessions store by default; both are now
// required positional parameters of NewHub. Callers that need to swap in
// custom dependencies do so via withValidator / withSessions options — there
// is no longer a variadic deps bag to abuse.
func newRunningHub(t *testing.T, opts ...func(*hubOpts)) *Hub {
	t.Helper()
	logger := logging.New("error", "json", io.Discard)
	o := &hubOpts{
		validator: defaultTestValidator(t),
		sessions:  NopSessionsForTests,
	}
	for _, fn := range opts {
		fn(o)
	}
	hub := NewHub(logger, nil, o.validator, o.sessions)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		hub.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("hub did not stop in time")
		}
	})
	return hub
}

// newClient registers a test client on the hub. The client is NOT a real
// websocket connection — its `send` channel is drained by the test.
func newClient(t *testing.T, hub *Hub, userID string) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		hub:      hub,
		userID:   userID,
		channels: make(map[string]bool),
		send:     make(chan []byte, 64),
		ctx:      ctx,
		cancel:   cancel,
	}
	hub.register <- c
	// Wait until the hub has actually registered the client so subsequent
	// Subscribe / Broadcast calls observe a stable state.
	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		return hub.clients[c]
	}, "client to register")
	t.Cleanup(func() {
		cancel()
	})
	return c
}

// waitFor blocks (up to 1s) for cond to become true.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// recvOrTimeout reads from ch within 500ms or fails the test.
func recvOrTimeout(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("send channel closed unexpectedly")
		}
		return b
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for message")
	}
	return nil
}

// expectNothing asserts the channel doesn't deliver within d.
func expectNothing(t *testing.T, ch <-chan []byte, d time.Duration) {
	t.Helper()
	select {
	case msg, ok := <-ch:
		if ok {
			t.Fatalf("expected no message, got %q", string(msg))
		}
	case <-time.After(d):
	}
}

// ---------- Broadcast routing ----------

func TestBroadcastDeliversToSubscriber(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	hub.Broadcast("session:s1", ServerMessage{Type: "ping", Payload: "hi"})
	got := recvOrTimeout(t, c.send)

	var msg map[string]interface{}
	if err := json.Unmarshal(got, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg["type"] != "ping" {
		t.Errorf("type = %v, want ping", msg["type"])
	}
}

func TestBroadcastSkipsUnsubscribed(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	// Do NOT subscribe.
	hub.Broadcast("session:s1", ServerMessage{Type: "ping", Payload: nil})

	// Give the hub a beat to process the broadcast then assert the buffer
	// stays empty.
	select {
	case msg := <-c.send:
		t.Fatalf("expected no delivery, got %q", string(msg))
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcastChannelHelpers(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("workspace:ws1")

	hub.BroadcastWorkspace("ws1", "workspace_event", map[string]string{"k": "v"})
	got := recvOrTimeout(t, c.send)

	var msg ServerMessage
	if err := json.Unmarshal(got, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Channel != "workspace:ws1" {
		t.Errorf("channel = %q, want workspace:ws1", msg.Channel)
	}
	if msg.Type != "workspace_event" {
		t.Errorf("type = %q, want workspace_event", msg.Type)
	}
}

// Calling on a nil hub must be a safe no-op (a common precaution at call sites).
func TestBroadcastChannelNilHubIsNoOp(t *testing.T) {
	t.Parallel()
	var hub *Hub
	// Must not panic.
	hub.BroadcastChannel("workspace", "x", "evt", nil)
}

func TestBroadcastExceptSkipsExcluded(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	cA := newClient(t, hub, "uA")
	cB := newClient(t, hub, "uB")
	cA.subscribe("session:s1")
	cB.subscribe("session:s1")

	hub.BroadcastExcept("session:s1", cA, ServerMessage{Type: "x", Payload: nil})

	// cB receives, cA does not (within a short window).
	recvOrTimeout(t, cB.send)
	select {
	case got := <-cA.send:
		t.Fatalf("excluded client received %q", string(got))
	case <-time.After(50 * time.Millisecond):
	}
}

// A client whose send buffer is full must NOT block other recipients and must
// NOT deadlock the hub. The hub uses a non-blocking send with a `default`
// branch — this test exercises that branch.
func TestBroadcastNonBlockingForSlowClient(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	slow := newClient(t, hub, "slow")
	slow.subscribe("session:s1")
	// Fill the slow client's buffer so the hub's `default` branch fires.
	for i := 0; i < cap(slow.send); i++ {
		slow.send <- []byte("filler")
	}

	fast := newClient(t, hub, "fast")
	fast.subscribe("session:s1")

	hub.Broadcast("session:s1", ServerMessage{Type: "ev", Payload: nil})
	// Fast client must still receive.
	recvOrTimeout(t, fast.send)
}

// TestConsecutiveDropsCounterResetsOnSuccess covers the success arm of
// dispatch — a single drop followed by a successful enqueue must reset
// consecutiveDrops back to zero. Without the reset, a long-lived but
// occasionally-bursty client would eventually hit the
// force-disconnect threshold despite never actually being stuck.
func TestConsecutiveDropsCounterResetsOnSuccess(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")
	// Fill the buffer so the next dispatch falls into the default branch.
	for i := 0; i < cap(c.send); i++ {
		c.send <- []byte("filler")
	}
	hub.dispatch("session:s1", []byte("dropped"), nil)
	if got := c.consecutiveDrops.Load(); got != 1 {
		t.Fatalf("consecutiveDrops after one drop = %d, want 1", got)
	}

	// Drain one slot so the next dispatch succeeds.
	<-c.send
	hub.dispatch("session:s1", []byte("delivered"), nil)
	if got := c.consecutiveDrops.Load(); got != 0 {
		t.Errorf("consecutiveDrops after successful send = %d, want 0", got)
	}
	if c.disconnectFired.Load() {
		t.Error("disconnectFired must remain false after a successful send")
	}
}

// TestSlowConsumerForceDisconnect drives consecutiveDropsBeforeDisconnect
// drops in a row past the cutoff and asserts the client is torn down
// exactly once. The disconnect runs in a goroutine so the test polls
// for ctx cancellation rather than asserting synchronously, but the
// counter and one-shot flag are observable immediately. dispatch is
// invoked directly (rather than via Broadcast) so the assertions don't
// race the buffered hub.broadcast channel + Run goroutine.
func TestSlowConsumerForceDisconnect(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	stuck := newClient(t, hub, "stuck")
	stuck.subscribe("session:s1")
	// Fill the buffer so every dispatch drops.
	for i := 0; i < cap(stuck.send); i++ {
		stuck.send <- []byte("filler")
	}

	// Drive exactly enough drops to cross the threshold. dispatch is
	// invoked directly (instead of via Broadcast) so the test doesn't
	// race the buffered hub.broadcast channel + Run goroutine.
	for i := 0; i < consecutiveDropsBeforeDisconnect; i++ {
		hub.dispatch("session:s1", []byte("drop"), nil)
	}
	if got := stuck.consecutiveDrops.Load(); got < consecutiveDropsBeforeDisconnect {
		t.Fatalf("consecutiveDrops = %d, want >= %d", got, consecutiveDropsBeforeDisconnect)
	}
	if !stuck.disconnectFired.Load() {
		t.Fatal("disconnectFired = false after crossing threshold, want true")
	}

	// forceDisconnect runs in a goroutine — wait for it to cancel ctx.
	waitFor(t, func() bool {
		return stuck.ctx.Err() != nil
	}, "stuck client context to cancel after force-disconnect")

	// A second pass of dropped broadcasts must NOT spawn another
	// disconnect goroutine (CompareAndSwap is the guard). The
	// observable invariant: the flag stays exactly true; we can't
	// directly count goroutines, but we can confirm extra drops don't
	// flip the flag back or otherwise misbehave.
	for i := 0; i < 16; i++ {
		hub.dispatch("session:s1", []byte("extra"), nil)
	}
	if !stuck.disconnectFired.Load() {
		t.Error("disconnectFired flipped after threshold — one-shot guard regressed")
	}
}

// ---------- Subscribe / authorize ----------

func TestSubscribeDeniedWithoutAuthorizer(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	// no authorizer configured

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	got := recvOrTimeout(t, c.send)
	var msg ServerMessage
	if err := json.Unmarshal(got, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" {
		t.Errorf("type = %q, want error", msg.Type)
	}

	// Channel must NOT be added to hub state.
	hub.mu.RLock()
	_, present := hub.channels["session:s1"]
	hub.mu.RUnlock()
	if present {
		t.Error("channel should not be registered when access is denied")
	}
}

func TestSubscribeDeniedByAuthorizer(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(denyAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")
	got := recvOrTimeout(t, c.send)

	var msg ServerMessage
	_ = json.Unmarshal(got, &msg)
	if msg.Type != "error" {
		t.Errorf("type = %q, want error", msg.Type)
	}
}

func TestSubscribeEmptyChannelIsNoOp(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(denyAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("") // must NOT trigger an authorizer call or response.

	expectNothing(t, c.send, 30*time.Millisecond)
}

func TestUnsubscribeRemovesChannel(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	// Confirm registered.
	hub.mu.RLock()
	subs, ok := hub.channels["session:s1"]
	regd := ok && subs[c]
	hub.mu.RUnlock()
	if !regd {
		t.Fatal("expected client to be registered on channel")
	}

	c.unsubscribe("session:s1")

	hub.mu.RLock()
	_, ok = hub.channels["session:s1"]
	hub.mu.RUnlock()
	if ok {
		t.Error("channel should be removed when last subscriber leaves")
	}
}

func TestUnsubscribeEmptyIsNoOp(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")
	c.unsubscribe("") // must not panic
}

// Unregistering a client must clean up its channel subscriptions and close send.
func TestUnregisterCleansUpChannels(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")
	c.subscribe("session:s1")

	if hub.ConnectionCount() != 1 {
		t.Fatalf("ConnectionCount = %d, want 1", hub.ConnectionCount())
	}

	hub.unregister <- c

	waitFor(t, func() bool { return hub.ConnectionCount() == 0 }, "ConnectionCount==0")

	hub.mu.RLock()
	_, present := hub.channels["session:s1"]
	hub.mu.RUnlock()
	if present {
		t.Error("channel should be cleaned up on unregister")
	}

	// send channel should be closed.
	select {
	case _, ok := <-c.send:
		if ok {
			t.Error("send should be closed after unregister")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("expected send to be closed")
	}
}

// Unregistering an unknown client must be a no-op (defensive).
func TestUnregisterUnknownClientNoOp(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rogue := &Client{
		hub:      hub,
		send:     make(chan []byte, 1),
		channels: make(map[string]bool),
		ctx:      ctx,
		cancel:   cancel,
	}
	hub.unregister <- rogue
	// If the path that closes send fires, this would panic on second close.
	// Sanity: connection count stays 0.
	if hub.ConnectionCount() != 0 {
		t.Errorf("ConnectionCount = %d, want 0", hub.ConnectionCount())
	}
}

// ---------- safeSend ----------

func TestSafeSendDeliversWhenSpace(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")

	if !c.safeSend([]byte("hello")) {
		t.Fatal("safeSend returned false on healthy channel")
	}
	got := recvOrTimeout(t, c.send)
	if string(got) != "hello" {
		t.Errorf("got %q want hello", string(got))
	}
}

func TestSafeSendReturnsFalseOnCancel(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")
	// Fill buffer so safeSend would block on send.
	for i := 0; i < cap(c.send); i++ {
		c.send <- []byte("x")
	}
	c.cancel()

	if c.safeSend([]byte("nope")) {
		t.Error("safeSend should return false when ctx is cancelled and buffer full")
	}
}

// ---------- handleSendMessage ----------

func TestHandleSendMessageNoChatHandler(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t) // chatHandler is nil

	c := newClient(t, hub, "u1")
	payload, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: payload})

	got := recvOrTimeout(t, c.send)
	var msg ServerMessage
	_ = json.Unmarshal(got, &msg)
	if msg.Type != "error" {
		t.Errorf("type = %q want error", msg.Type)
	}
}

func TestHandleSendMessageInvalidPayload(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChatHandler(&stubChatHandler{})

	c := newClient(t, hub, "u1")
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: []byte("not-json")})

	got := recvOrTimeout(t, c.send)
	var msg ServerMessage
	_ = json.Unmarshal(got, &msg)
	if msg.Type != "error" {
		t.Errorf("type = %q want error", msg.Type)
	}
}

func TestHandleSendMessageMissingFields(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChatHandler(&stubChatHandler{})

	tests := []struct {
		name    string
		payload sendMessagePayload
	}{
		{"missing chat id", sendMessagePayload{ChatID: "", Content: "hi"}},
		{"missing content", sendMessagePayload{ChatID: "s1", Content: ""}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, hub, "u1")
			body, _ := json.Marshal(tc.payload)
			c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})
			got := recvOrTimeout(t, c.send)
			var msg ServerMessage
			_ = json.Unmarshal(got, &msg)
			if msg.Type != "error" {
				t.Errorf("type = %q want error", msg.Type)
			}
		})
	}
}

// Frontend sometimes sends `{"payload": "{...}"}` (double-encoded). Verify the
// unwrap path actually executes by sending a string-encoded payload and
// asserting the chat handler still runs.
func TestHandleSendMessageDoubleEncodedPayload(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	handler := &stubChatHandler{events: []ChatEvent{{Type: "text", Content: "ok"}}}
	hub.SetChatHandler(handler)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	c := newClient(t, hub, "u1")

	inner, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hello"})
	wrapped, _ := json.Marshal(string(inner))
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: wrapped})

	// First message will be the streamed event; we just need to wait until the
	// handler ran.
	recvOrTimeout(t, c.send)
	waitFor(t, func() bool {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		return handler.calls == 1
	}, "chat handler call")
}

func TestHandleSendMessageDeniedBySessionAuth(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	handler := &stubChatHandler{}
	hub.SetChatHandler(handler)
	hub.SetChannelAuthorizer(&allowChannelsAuthorizer{allowed: map[string]bool{}}) // deny all sessions

	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

	got := recvOrTimeout(t, c.send)
	var msg ServerMessage
	_ = json.Unmarshal(got, &msg)
	if msg.Type != "error" {
		t.Errorf("type = %q want error", msg.Type)
	}
	// Handler must not have been invoked.
	time.Sleep(20 * time.Millisecond)
	handler.mu.Lock()
	calls := handler.calls
	handler.mu.Unlock()
	if calls != 0 {
		t.Errorf("handler.calls = %d, want 0", calls)
	}
}

func TestHandleSendMessageRejectsConcurrent(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	handler := &stubChatHandler{block: block}
	hub.SetChatHandler(handler)

	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})

	// First call: launches an in-flight run that blocks on `block`.
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})
	// Wait until the run has registered its cancel fn so the second call is
	// guaranteed to hit the "already in progress" branch.
	waitFor(t, func() bool {
		hub.cancelMu.Lock()
		defer hub.cancelMu.Unlock()
		return len(hub.cancelFns) == 1
	}, "first run to register cancelFn")

	// Second call must error with "already being processed".
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

	// Drain until we see the rejection error (events from first run may also flow).
	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case raw := <-c.send:
			var msg ServerMessage
			_ = json.Unmarshal(raw, &msg)
			if msg.Type == "error" {
				m, _ := msg.Payload.(map[string]interface{})
				if errMsg, _ := m["error"].(string); strings.Contains(errMsg, "already being processed") {
					return
				}
			}
		case <-deadline:
			t.Fatal("did not see 'already being processed' error")
		}
	}
}

func TestHandleSendMessageStreamsAndCleansCancelFn(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	handler := &stubChatHandler{events: []ChatEvent{{Type: "text", Content: "hello"}}}
	hub.SetChatHandler(handler)

	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "ping"})
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

	// Drain at least one event.
	recvOrTimeout(t, c.send)

	waitFor(t, func() bool {
		hub.cancelMu.Lock()
		defer hub.cancelMu.Unlock()
		return len(hub.cancelFns) == 0
	}, "cancelFn to be removed")
}

// When chat handler returns an error and ctx wasn't cancelled, an error event
// must reach the client.
func TestHandleSendMessageHandlerError(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	handler := &stubChatHandler{err: errors.New("boom")}
	hub.SetChatHandler(handler)

	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "ping"})
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})

	deadline := time.After(500 * time.Millisecond)
	for {
		select {
		case raw := <-c.send:
			var msg ServerMessage
			_ = json.Unmarshal(raw, &msg)
			if msg.Type == "chat_event" {
				if pmap, ok := msg.Payload.(map[string]interface{}); ok {
					if pmap["type"] == "error" {
						return
					}
				}
			}
		case <-deadline:
			t.Fatal("did not see error chat_event")
		}
	}
}

// ---------- handleCancelMessage ----------

func TestHandleCancelMessageInvokesCancel(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	handler := &stubChatHandler{block: block}
	hub.SetChatHandler(handler)

	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "s1", Content: "hi"})
	c.handleSendMessage(ClientMessage{Type: "send_message", Channel: "session:s1", Payload: body})
	waitFor(t, func() bool {
		hub.cancelMu.Lock()
		defer hub.cancelMu.Unlock()
		return len(hub.cancelFns) == 1
	}, "cancelFn registered")

	cancelBody, _ := json.Marshal(sendMessagePayload{ChatID: "s1"})
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: cancelBody})

	waitFor(t, func() bool {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		return handler.gotCtx != nil && handler.gotCtx.Err() != nil
	}, "ctx to be cancelled")
}

func TestHandleCancelMessageInvalidPayloadIsNoOp(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: []byte("not-json")})
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: nil})
	expectNothing(t, c.send, 30*time.Millisecond)
}

// Cancel for a session that has no in-flight run — must be a quiet no-op.
func TestHandleCancelMessageUnknownSession(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	c := newClient(t, hub, "u1")
	body, _ := json.Marshal(sendMessagePayload{ChatID: "ghost"})
	c.handleCancelMessage(ClientMessage{Type: "cancel_message", Payload: body})
	expectNothing(t, c.send, 30*time.Millisecond)
}

// ---------- SetChatHandler ----------

func TestSetChatHandlerReplaces(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	first := &stubChatHandler{}
	second := &stubChatHandler{}
	hub.SetChatHandler(first)
	hub.SetChatHandler(second)
	if hub.chatHandler != second {
		t.Error("SetChatHandler did not replace handler")
	}
}

// ---------- HandleUpgrade auth paths ----------

func TestHandleUpgradeMissingToken(t *testing.T) {
	t.Parallel()
	hub := newRunningHub(t)
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestNewHubPanicsWithoutValidator replaces the old "no validator → 503"
// test. NewHub now requires a typed validator at construction time, so
// the misconfiguration trips the panic at startup rather than letting
// the hub run accepting every WS upgrade.
func TestNewHubPanicsWithoutValidator(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected NewHub to panic when given a nil JWT validator")
		}
	}()
	logger := logging.New("error", "json", io.Discard)
	NewHub(logger, nil, nil, NopSessionsForTests)
}

// TestNewHubPanicsWithoutSessions covers the parallel guard for the
// sessions store: also required, also rejected at construction.
func TestNewHubPanicsWithoutSessions(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected NewHub to panic when given a nil sessions store")
		}
	}()
	logger := logging.New("error", "json", io.Discard)
	NewHub(logger, nil, defaultTestValidator(t), nil)
}

func TestHandleUpgradeInvalidToken(t *testing.T) {
	t.Parallel()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
	hub := newRunningHub(t, withValidator(v))
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/?token=garbage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// End-to-end: real WebSocket upgrade with a valid token, subscribe, broadcast,
// receive. Exercises HandleUpgrade, readPump, writePump, subscribe (auth),
// Broadcast, and ping/pong serialization on the client message side.
func TestHandleUpgradeAndBroadcastEndToEnd(t *testing.T) {
	t.Parallel()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
	hub := newRunningHub(t, withValidator(v))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	tok, err := v.IssueWSTicket("user-1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Build ws:// URL from httptest server URL.
	u, _ := url.Parse(srv.URL)
	wsURL := fmt.Sprintf("ws://%s/?token=%s", u.Host, url.QueryEscape(tok))

	conn, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Subscribe.
	sub, _ := json.Marshal(ClientMessage{Type: "subscribe", Channel: "session:s1"})
	if _, err := conn.Write(sub); err != nil {
		t.Fatal(err)
	}

	// Wait until the hub registers the subscription.
	waitFor(t, func() bool {
		hub.mu.RLock()
		defer hub.mu.RUnlock()
		_, ok := hub.channels["session:s1"]
		return ok
	}, "channel subscription")

	// Broadcast — must arrive at the client.
	hub.Broadcast("session:s1", ServerMessage{Type: "ev", Payload: "hi"})

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("recv: %v", err)
	}
	var msg ServerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "ev" {
		t.Errorf("type = %q want ev", msg.Type)
	}
}

// ping → pong reply path through the websocket loop.
func TestPingProducesPongReply(t *testing.T) {
	t.Parallel()
	v, err := auth.NewJWTValidator("test-secret-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
	hub := newRunningHub(t, withValidator(v))
	hub.SetChannelAuthorizer(allowAllAuthorizer{})
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(srv.Close)

	tok, err := v.IssueWSTicket("user-1", "", "", "")
	if err != nil {
		t.Fatalf("issue ws ticket: %v", err)
	}
	u, _ := url.Parse(srv.URL)
	wsURL := fmt.Sprintf("ws://%s/?token=%s", u.Host, url.QueryEscape(tok))

	var conn *websocket.Conn
	conn, err = websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ping, _ := json.Marshal(ClientMessage{Type: "ping"})
	if _, err := conn.Write(ping); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("recv: %v", err)
	}
	var msg ServerMessage
	_ = json.Unmarshal(raw, &msg)
	if msg.Type != "pong" {
		t.Errorf("type = %q want pong", msg.Type)
	}
}
