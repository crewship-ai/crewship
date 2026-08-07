package ws

import (
	"encoding/json"
	"testing"
	"time"
)

// receiveFrame drains one frame from an observer, failing the test if none
// arrives.
//
// Hub.Broadcast does NOT dispatch on the caller's goroutine: it enqueues onto
// h.broadcast, and Hub.Run drains that queue and calls dispatch on the hub's
// own goroutine. Everything below depends on knowing that, because it means a
// broadcast is not observable the instant Broadcast returns - see sentinel().
func receiveFrame(t *testing.T, o *Observer) ServerMessage {
	t.Helper()
	select {
	case data := <-o.Frames():
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal observed frame: %v", err)
		}
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an observed frame")
		return ServerMessage{}
	}
}

func TestObserverReceivesChannelBroadcasts(t *testing.T) {
	hub := newRunningHub(t)
	obs := hub.AddObserver("session:c1", "u1", 8)
	defer hub.RemoveObserver("session:c1", obs)

	hub.Broadcast("session:c1", ServerMessage{Type: "chat_event", Channel: "session:c1", Payload: ChatEvent{Type: "text", Content: "hi"}})

	got := receiveFrame(t, obs)
	if got.Type != "chat_event" {
		t.Errorf("observed frame type = %q, want chat_event", got.Type)
	}
}

// sentinel broadcasts a uniquely-typed frame on channel. Because Hub.Run
// drains h.broadcast in FIFO order on ONE goroutine, a sentinel that has been
// RECEIVED proves every broadcast enqueued before it was already dispatched.
//
// That is what makes the negative assertions below mean anything. "Nothing
// arrived within 100 ms" is a timing assumption, not a fact: on a loaded
// runner the hub may simply not have reached the broadcast yet, and the test
// passes having exercised nothing. Ordering against a sentinel turns absence
// into a property of dispatch rather than a property of the scheduler.
func sentinel(hub *Hub, channel string) {
	hub.Broadcast(channel, ServerMessage{Type: "sentinel", Channel: channel})
}

// An observer must stop receiving the moment it is removed - otherwise a
// finished HTTP stream keeps costing fan-out work on every subsequent frame
// (the same slow-consumer accounting Hub.dispatch does for sockets).
func TestObserverStopsAfterRemove(t *testing.T) {
	hub := newRunningHub(t)
	gone := hub.AddObserver("session:c1", "u1", 8)
	// A second observer that STAYS. Its receipt is the proof that dispatch
	// actually ran for this broadcast, so the removed one's silence is a real
	// verdict rather than a race the test happened to win.
	stays := hub.AddObserver("session:c1", "u2", 8)
	defer hub.RemoveObserver("session:c1", stays)

	hub.RemoveObserver("session:c1", gone)
	sentinel(hub, "session:c1")

	if got := receiveFrame(t, stays); got.Type != "sentinel" {
		t.Fatalf("still-attached observer got %q, want the sentinel - dispatch did not run", got.Type)
	}

	// Dispatch has demonstrably happened. A non-blocking read is now decisive:
	// the channel must be closed and must never have carried the sentinel.
	select {
	case data, ok := <-gone.Frames():
		if ok {
			t.Fatalf("removed observer still received a frame: %s", data)
		}
	default:
		t.Fatal("removed observer's frame channel was not closed")
	}
}

// Observers are scoped to one channel; a broadcast elsewhere must not leak
// into them (this is the same isolation Hub.channels gives WebSocket clients,
// and the HTTP stream's tenancy story depends on it).
func TestObserverIsChannelScoped(t *testing.T) {
	hub := newRunningHub(t)
	obs := hub.AddObserver("session:c1", "u1", 8)
	defer hub.RemoveObserver("session:c1", obs)

	// Enqueue the foreign frame FIRST, then the sentinel on our own channel.
	// FIFO means the foreign one is dispatched first, so if it leaked it would
	// be the frame we read here.
	hub.Broadcast("session:c2", ServerMessage{Type: "chat_event", Channel: "session:c2", Payload: ChatEvent{Type: "text", Content: "other"}})
	sentinel(hub, "session:c1")

	got := receiveFrame(t, obs)
	if got.Type != "sentinel" {
		t.Fatalf("first frame was %q from channel %q - another channel's frame leaked in", got.Type, got.Channel)
	}
}

// A full observer buffer must NOT block the hub's dispatch loop. The frame is
// dropped and the observer is flagged so the HTTP stream can end honestly
// (telling the client to resume from its last seq) instead of silently
// serving a stream with a hole in it.
func TestObserverDropsRatherThanBlockingDispatch(t *testing.T) {
	hub := newRunningHub(t)
	obs := hub.AddObserver("session:c1", "u1", 1)
	defer hub.RemoveObserver("session:c1", obs)

	// Two frames into a buffer of one, with nobody reading: the second must be
	// dropped rather than parking the dispatch goroutine.
	hub.Broadcast("session:c1", ServerMessage{Type: "chat_event", Channel: "session:c1", Payload: ChatEvent{Type: "text", Content: "one"}})
	hub.Broadcast("session:c1", ServerMessage{Type: "chat_event", Channel: "session:c1", Payload: ChatEvent{Type: "text", Content: "two"}})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !obs.Dropped() {
		time.Sleep(time.Millisecond)
	}
	if !obs.Dropped() {
		t.Fatal("observer did not report the dropped frame; a slow HTTP reader would silently lose events")
	}
}

// ReplaySession is the exported door onto the session_stream.go buffer that
// the HTTP handler resumes from. It must report the same three states the WS
// resume path distinguishes: not found, truncated, and a replayable gap.
func TestReplaySessionExposesBufferedGap(t *testing.T) {
	hub := newRunningHub(t)
	ch := "session:c1"

	if res := hub.ReplaySession(ch, 0); res.Found {
		t.Fatal("ReplaySession found a buffer for a channel that never ran")
	}

	hub.streams.begin(ch)
	defer hub.streams.end(ch)
	recordEvent(t, hub.streams, ch, "a") // seq 1
	recordEvent(t, hub.streams, ch, "b") // seq 2

	res := hub.ReplaySession(ch, 0)
	if !res.Found || !res.Active || res.Reset {
		t.Fatalf("ReplaySession(0) = %+v, want found+active and not reset", res)
	}
	if len(res.Frames) != 2 {
		t.Fatalf("ReplaySession(0) returned %d frames, want 2", len(res.Frames))
	}

	// Resuming past the first frame must hand back only the gap.
	res = hub.ReplaySession(ch, 1)
	if len(res.Frames) != 1 {
		t.Fatalf("ReplaySession(1) returned %d frames, want 1", len(res.Frames))
	}
	if got := seqOf(t, res.Frames[0]); got != 2 {
		t.Errorf("resumed frame seq = %d, want 2", got)
	}
	if res.FromSeq != 1 {
		t.Errorf("FromSeq = %d, want 1", res.FromSeq)
	}
}

// CanSubscribeChannel is the authorization the HTTP stream shares with the WS
// subscribe/resume paths. With no authorizer configured it must deny — the
// same fail-closed default handleResume applies.
func TestCanSubscribeChannelFailsClosedWithoutAuthorizer(t *testing.T) {
	hub := newRunningHub(t)
	ok, err := hub.CanSubscribeChannel(t.Context(), "u1", "session:c1")
	if ok {
		t.Fatal("CanSubscribeChannel granted access with no authorizer configured")
	}
	if err == nil {
		t.Error("CanSubscribeChannel returned a definitive deny for a missing authorizer; callers must be able to tell 'not configured' from 'not a member'")
	}
}
