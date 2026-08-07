package ws

import (
	"context"
	"errors"
	"sync/atomic"
)

// Non-WebSocket subscribers to a channel's frames.
//
// Why this exists (#1818): the only way to watch an agent run used to be a
// WebSocket client — mint a `/api/v1/ws-token` JWT, upgrade, subscribe to
// `session:{id}`. That is right for the browser and wrong for an agent driving
// Crewship from a shell, which would have to implement a WS client and a token
// dance before it could see a single line of output. The HTTP NDJSON stream
// (internal/api/run_stream_handler.go) needs the SAME frames the socket gets,
// so it attaches here rather than growing a parallel event path that could
// drift from the one the browser uses.
//
// An Observer is deliberately thin: it is a buffered channel of already-
// marshaled frames plus a dropped flag. It carries no identity and no
// authorization of its own — the HTTP handler authorizes the caller against
// the same channelAuth the WS subscribe/resume paths use (CanSubscribeChannel)
// BEFORE attaching, exactly as Client.subscribe does.

// ErrNoChannelAuthorizer is returned by CanSubscribeChannel when the hub has
// no authorizer wired. It is an ERROR rather than a plain deny so callers can
// tell "this deployment is misconfigured" from "this user is not a member" —
// both must fail closed, but only one of them is worth logging loudly.
var ErrNoChannelAuthorizer = errors.New("ws: no channel authorizer configured")

// Observer is a non-WebSocket subscriber to one channel. Frames arrive on the
// channel returned by Frames() as raw JSON bytes — the exact bytes every
// socket subscriber receives, seq numbers included.
type Observer struct {
	ch chan []byte
	// userID is who is watching. Its consumers are snapshotObservers (the
	// hub's periodic channel re-authorization, which re-checks this user
	// against the channel) and RevokeObservers (which tears down that user's
	// streams when the check fails).
	//
	// It is deliberately NOT used for presence. Hub.IsUserSubscribed excludes
	// observers on purpose: its only consumer, chatnotify, treats "present" as
	// licence to skip inbox.UpsertMessage entirely, so counting a stream that
	// may be redirected to a file — or blocked in a write — would destroy the
	// user's durable record of the reply. TestObserverDoesNotCountAsPresence
	// pins that. Do not "restore" presence counting here.
	userID string
	// dropped flips when dispatch could not enqueue a frame because the buffer
	// was full. It is one-way: once a stream has a hole in it, the only honest
	// thing the reader can do is end and let the client resume from its last
	// seq. Clearing the flag would let the reader pretend the gap healed.
	dropped atomic.Bool
	// closed guards the close(ch) in RemoveObserver so a double remove (a
	// handler's defer plus an explicit call on the error path) can't panic.
	closed atomic.Bool
	// revoked distinguishes "the sweep took this stream's access away" from
	// "the stream ended". Both close ch; only the first is a permanent verdict
	// the caller must not retry against.
	revoked atomic.Bool
}

// Revoked reports whether this observer was detached by the hub's periodic
// channel re-authorization sweep (membership removed) rather than by its own
// reader finishing.
func (o *Observer) Revoked() bool { return o.revoked.Load() }

// Frames is the receive side. It is closed by RemoveObserver, so a reader
// ranging over it terminates when the stream is torn down.
func (o *Observer) Frames() <-chan []byte { return o.ch }

// Dropped reports whether any frame was lost to backpressure on this observer.
func (o *Observer) Dropped() bool { return o.dropped.Load() }

// AddObserver attaches a non-WebSocket subscriber to channel and returns it.
// buffer sizes the frame queue; a full queue drops frames (see Observer.dropped)
// rather than blocking Hub.dispatch, because one slow HTTP reader must never
// stall fan-out to the browser clients on the same channel.
//
// userID is the authenticated watcher, used only for presence
// (Hub.IsUserSubscribed). The caller must already have authorized that user
// for the channel — AddObserver performs no check of its own, exactly like
// Client.subscribe's callers.
//
// Callers MUST pair this with RemoveObserver (defer it) or the hub leaks the
// entry and keeps paying fan-out cost for a stream nobody reads.
func (h *Hub) AddObserver(channel, userID string, buffer int) *Observer {
	if buffer < 1 {
		buffer = 1
	}
	o := &Observer{ch: make(chan []byte, buffer), userID: userID}
	h.mu.Lock()
	if h.observers == nil {
		h.observers = make(map[string]map[*Observer]bool)
	}
	if h.observers[channel] == nil {
		h.observers[channel] = make(map[*Observer]bool)
	}
	h.observers[channel][o] = true
	h.mu.Unlock()
	return o
}

// RemoveObserver detaches o and closes its frame channel. Idempotent.
func (h *Hub) RemoveObserver(channel string, o *Observer) {
	if o == nil {
		return
	}
	h.mu.Lock()
	if subs := h.observers[channel]; subs != nil {
		delete(subs, o)
		if len(subs) == 0 {
			delete(h.observers, channel)
		}
	}
	h.mu.Unlock()
	// Close after the map removal so dispatch (which holds RLock) can never be
	// mid-send on a channel we are about to close.
	if o.closed.CompareAndSwap(false, true) {
		close(o.ch)
	}
}

// revokeObserver detaches o because it is no longer authorized for channel,
// flagging it first so its reader can tell a permanent verdict from an
// ordinary end-of-stream and refuse to retry.
//
// This is the observer half of sweepChannelAuthorization. Membership change is
// a distinct event from session revocation, and without it the HTTP stream's
// authorization would be a one-time check at request time — a member removed
// from the workspace would keep receiving the chat's frames for as long as
// they held the connection, while a socket on the same channel was cut on the
// next tick.
func (h *Hub) revokeObserver(channel string, o *Observer) {
	if o == nil {
		return
	}
	o.revoked.Store(true)
	h.RemoveObserver(channel, o)
}

// RevokeObservers detaches every HTTP run-stream observer on channel that
// belongs to userID, returning how many were detached. An empty userID matches
// every observer on the channel.
//
// This is the enforcement verb for "this caller may no longer watch this
// chat". sweepChannelAuthorization drives it on the periodic membership
// re-check; it is exported because the decision (who lost access) belongs to
// whatever policy made it, while the teardown belongs here.
func (h *Hub) RevokeObservers(channel, userID string) int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	var doomed []*Observer
	for o := range h.observers[channel] {
		if userID == "" || o.userID == userID {
			doomed = append(doomed, o)
		}
	}
	h.mu.RUnlock()
	for _, o := range doomed {
		h.revokeObserver(channel, o)
	}
	return len(doomed)
}

// observerSnapshot is one attached observer plus what the sweep needs to
// re-check it. Taken under the lock and consumed outside it, because
// CanSubscribe hits the DB and must never run while the hub lock is held.
type observerSnapshot struct {
	channel  string
	userID   string
	observer *Observer
}

// snapshotObservers copies the current observer set for the sweep.
func (h *Hub) snapshotObservers() []observerSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []observerSnapshot
	for channel, set := range h.observers {
		for o := range set {
			out = append(out, observerSnapshot{channel: channel, userID: o.userID, observer: o})
		}
	}
	return out
}

// dispatchObservers fans data out to every observer on channel. Called from
// Hub.dispatch with h.mu already held for reading — it must not take the lock
// itself and must not block.
func (h *Hub) dispatchObservers(channel string, data []byte) {
	for o := range h.observers[channel] {
		select {
		case o.ch <- data:
		default:
			o.dropped.Store(true)
		}
	}
}

// ObserverCount reports how many non-WebSocket observers are attached to
// channel. Exported for tests and for the run-stream handler's own diagnostics.
func (h *Hub) ObserverCount(channel string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.observers[channel])
}

// SessionReplay is the exported shape of session_stream.go's replayResult, so
// the HTTP stream can resume from the SAME buffer the WS `resume` message
// reads. See session_stream.go for why replay is only offered while a run is
// active and why a truncated buffer must not serve a partial stream.
type SessionReplay struct {
	// FromSeq is the authoritative baseline: the caller should treat this as
	// its last-applied seq and expect contiguous frames from FromSeq+1.
	FromSeq int64
	// Frames are the already-marshaled ServerMessages the caller missed.
	Frames [][]byte
	// Active is true while a run is still generating on this channel.
	Active bool
	// Reset is true when the buffer overflowed and cannot serve a coherent
	// replay; the caller must fall back to chat history rather than stitch a
	// partial stream together.
	Reset bool
	// Found is false when there is no buffer at all — no run has started, or
	// the last one's grace TTL expired and history already covers it.
	Found bool
}

// ReplaySession returns the frames a resuming caller missed on channel, given
// the last seq it already applied. Nil-safe on a hub with no run history.
func (h *Hub) ReplaySession(channel string, afterSeq int64) SessionReplay {
	if h == nil || h.streams == nil {
		return SessionReplay{}
	}
	res := h.streams.replay(channel, afterSeq)
	return SessionReplay{
		FromSeq: res.fromSeq,
		Frames:  res.frames,
		Active:  res.active,
		Reset:   res.reset,
		Found:   res.found,
	}
}

// CanSubscribeChannel exposes the hub's channel authorizer to HTTP handlers
// that stream the same channels the socket serves, so the two paths cannot
// drift apart on who may watch what.
//
// Contract matches ChannelAuthorizer.CanSubscribe: (false, nil) is a
// definitive deny, (false, err) is a failed check. Grant paths must treat both
// as deny. A missing authorizer returns ErrNoChannelAuthorizer.
func (h *Hub) CanSubscribeChannel(ctx context.Context, userID, channel string) (bool, error) {
	if h == nil || h.channelAuth == nil {
		return false, ErrNoChannelAuthorizer
	}
	return h.channelAuth.CanSubscribe(ctx, userID, channel)
}
