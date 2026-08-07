package ws

import "sync/atomic"

// One agent run, published on one chat's session channel (#1823).
//
// A "session run" is the seq'd, buffered, fan-out lifecycle that makes an
// agent's output watchable: begin the channel's replay buffer, stamp every
// frame with the channel's monotonic sequence number, fan the exact same bytes
// out to every WebSocket subscriber and every HTTP observer, end the buffer.
// See session_stream.go for the buffer itself.
//
// Why this type exists rather than four copies of the same three calls:
// until #1823 this lifecycle was open-coded in exactly ONE place —
// Client.handleSendMessage — so a run started any other way (routine,
// webhook, pipeline step, agent-start IPC) published nothing and
// `crewship chat stream` answered `no_active_run` for a run that was very much
// running. The fix could have been four more copies of begin/record/end. It is
// one type instead, for two reasons:
//
//   - The invariants are not obvious. `end` must be balanced against `begin`
//     on every exit path or the channel reports "generating" forever; the
//     terminal `done` must be emitted BEFORE `end` or it lands outside the
//     buffer and a resuming watcher never sees the run finish; frames must be
//     marshaled once and fanned out as identical bytes or two subscribers
//     disagree about sequence numbers. Four copies means four chances to get
//     one of those wrong, and the failure mode is silent.
//   - The fifth caller. Anything that reaches orchestrator.RunAgent with a
//     chat id is published automatically (see orchestrator/session_stream.go);
//     this is the single implementation that call site drives.
//
// The WebSocket send path uses the same type — it needs one extra recipient
// (its own socket, which may not be subscribed to the channel it is sending
// on), which is the `origin` field, not a second implementation.

// SessionRun is a live recording of one agent run on a chat's session channel.
// Every frame it emits is sequenced and buffered for replay; End closes the
// recording. Both methods are safe to call from any goroutine, and End is
// idempotent so a defer and an explicit finish can coexist.
type SessionRun interface {
	// Emit publishes one agent event: sequenced, buffered for replay, and
	// delivered to every current subscriber and observer.
	Emit(event ChatEvent)
	// End marks the run finished. Emit nothing after it — a frame recorded
	// once the run is over gets no seq and is not buffered, so a client that
	// reconnects will never see it.
	End()
}

// sessionRun is the hub-backed SessionRun.
type sessionRun struct {
	hub     *Hub
	channel string
	// origin is the client that STARTED this run over the socket, when there
	// is one. It is delivered to directly rather than through the channel
	// fan-out: a sender is not necessarily subscribed to the session channel
	// it just sent on, and the pre-#1823 behaviour of handleSendMessage was to
	// serve it explicitly. nil for every server-initiated run, which is the
	// case this type was added for.
	origin *Client
	ended  atomic.Bool
}

// nopSessionRun is returned when there is no channel to publish on. Callers
// then need no nil checks: a run with no chat id is simply not watchable, and
// saying so with a no-op beats recording against `session:` — a channel no
// authorizer can ever grant and no client can ever subscribe to.
type nopSessionRun struct{}

func (nopSessionRun) Emit(ChatEvent) {}
func (nopSessionRun) End()           {}

// BeginSessionRun opens a recording for chatID and emits the run's `run_begin`
// frame. The caller MUST End it — deferring the End at the point of the Begin
// is the only shape that survives every error return, and it is what the
// orchestrator wrapper does.
//
// An empty chatID yields a no-op recorder (see nopSessionRun).
func (h *Hub) BeginSessionRun(chatID string) SessionRun {
	return h.beginSessionRun(chatID, nil)
}

// beginSessionRun is BeginSessionRun plus the socket sender's direct delivery.
// Unexported because `origin` is meaningful only inside the hub.
func (h *Hub) beginSessionRun(chatID string, origin *Client) SessionRun {
	if h == nil || chatID == "" {
		return nopSessionRun{}
	}
	r := &sessionRun{hub: h, channel: "session:" + chatID, origin: origin}
	// begin returns the channel's seq BEFORE this run. run_begin carries it as
	// `from_seq` so any client — the sender, a second tab, an HTTP observer
	// that attached mid-run — can anchor its in-order reassembly without
	// waiting for sequence numbers that belonged to a previous run on the same
	// channel.
	startSeq := h.streams.begin(r.channel)
	r.emit(&ServerMessage{
		Type:    "run_begin",
		Channel: r.channel,
		Payload: map[string]any{"from_seq": startSeq},
	})
	return r
}

func (r *sessionRun) Emit(event ChatEvent) {
	r.emit(&ServerMessage{Type: "chat_event", Channel: r.channel, Payload: event})
}

// emit stamps the per-channel seq on msg, buffers the frame for replay, and
// fans the EXACT SAME bytes out to every recipient — so the sender, a second
// tab and an HTTP observer all see identical sequence numbers.
func (r *sessionRun) emit(msg *ServerMessage) {
	data, ok := r.hub.streams.record(r.channel, msg)
	if !ok {
		return
	}
	if r.origin != nil {
		r.origin.safeSend(data)
		r.hub.dispatch(r.channel, data, func(c *Client) bool { return c != r.origin })
		return
	}
	r.hub.dispatch(r.channel, data, nil)
}

// End is idempotent. The refcount underneath is shared with any concurrent run
// on the same chat (a group chat, a second tab), so a double decrement would
// declare somebody else's live run finished.
func (r *sessionRun) End() {
	if r.ended.CompareAndSwap(false, true) {
		r.hub.streams.end(r.channel)
	}
}
