package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Regression tests for the #1822 review findings that live in the handler's
// replay/watermark state machine and its resource bounds.

func bufferedFrame(t *testing.T, seq int64, evType, content string) []byte {
	t.Helper()
	b, err := json.Marshal(ws.ServerMessage{
		Type: "chat_event", Channel: "session:c_1", Seq: seq,
		Payload: ws.ChatEvent{Type: evType, Content: content},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Finding 3. The seq counter is in-memory, so a restart resets the channel to
// 0 while a caller still holds the watermark our own CLI printed. The handler
// seeded st.lastSeq from the request and only ever clamped it UP, so every
// live frame (seq 1, 2, 3 …) was below it and got dropped by the dedupe —
// including `done`. The caller saw stream.open, heartbeats, and an
// idle_timeout with empty output and exit 0.
//
// ws.SessionReplay.FromSeq is the run's authoritative baseline; the handler
// must adopt it in BOTH directions.
func TestRunStream_StaleWatermarkDoesNotSwallowTheRun(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{
		Hub:   hub,
		allow: true,
		// The buffer clamps a stale watermark down to the run baseline, so it
		// reports FromSeq 0 even though the caller asked for 42.
		replay: ws.SessionReplay{Found: true, Active: true, FromSeq: 0},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?last_seq=42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	waitForObserver(t, hub, "session:c_1")
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 1, Payload: ws.ChatEvent{Type: "text", Content: "post-restart output"}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 2, Payload: ws.ChatEvent{Type: "done"}})

	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	var sawText, sawDone bool
	for _, f := range frames {
		if f["content"] == "post-restart output" {
			sawText = true
		}
		if f["type"] == "done" {
			sawDone = true
		}
	}
	if !sawText || !sawDone {
		t.Fatalf("frames = %v; a watermark above the run's baseline must not filter out the whole run", frames)
	}
	if last := frames[len(frames)-1]; last["reason"] != "run_complete" {
		t.Errorf("stream.end reason = %v, want run_complete (not a 5-minute idle_timeout)", last["reason"])
	}
}

// Finding 7. The replay loop threw away writeHubFrame's terminal signal, so a
// `done` that arrived inside the replayed gap did not end the stream. The live
// duplicate of that same frame was then suppressed by the seq dedupe, so
// nothing else could end it either and the caller sat through heartbeats to
// the full idle timeout — exiting idle_timeout instead of run_complete.
func TestRunStream_TerminalDoneInReplayEndsTheStream(t *testing.T) {
	src := &fakeRunStreamSource{
		Hub:   newTestHubForStream(t),
		allow: true,
		replay: ws.SessionReplay{
			Found: true, Active: true, FromSeq: 1,
			Frames: [][]byte{
				bufferedFrame(t, 2, "text", "tail of the run"),
				bufferedFrame(t, 3, "done", ""),
			},
		},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	start := time.Now()
	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?last_seq=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := readFrames(t, resp.Body, 0, 5*time.Second)

	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("stream took %s to close; a replayed `done` must end it immediately", elapsed)
	}
	last := frames[len(frames)-1]
	if last["type"] != "stream.end" || last["reason"] != "run_complete" {
		t.Fatalf("last frame = %v, want stream.end/run_complete", last)
	}
}

// Finding 4. `truncated` is sticky for the whole run, so once a long run trips
// the 5000-frame / 8 MiB cap, replay() answers reset:true to EVERY caller —
// including a first attach with last_seq=0, which is not resuming and wants
// nothing replayed. That caller was handed an instant, CLI-non-retryable
// failure for the rest of the run, and the suggested fallback (chat history)
// is empty because the run has not been persisted yet.
func TestRunStream_TruncatedBufferStillServesAFirstAttach(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{
		Hub:    hub,
		allow:  true,
		replay: ws.SessionReplay{Found: true, Active: true, Reset: true},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	// No last_seq: a first attach, not a resume.
	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	waitForObserver(t, hub, "session:c_1")
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 7001, Payload: ws.ChatEvent{Type: "text", Content: "live after truncation"}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 7002, Payload: ws.ChatEvent{Type: "done"}})

	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	var sawLive bool
	for _, f := range frames {
		if f["content"] == "live after truncation" {
			sawLive = true
		}
	}
	if !sawLive {
		t.Fatalf("frames = %v; a first attach must keep streaming live output even when the run's replay buffer is truncated", frames)
	}
	last := frames[len(frames)-1]
	if last["reason"] == "replay_truncated" {
		t.Errorf("stream.end reason = replay_truncated for a caller that asked for no replay; that is a hard failure it cannot recover from")
	}
}

// The resuming caller keeps the old, correct behaviour: its gap really is
// unrecoverable, so it must be told rather than handed a stream with a hole.
func TestRunStream_TruncatedBufferStillFailsAResume(t *testing.T) {
	src := &fakeRunStreamSource{
		Hub:    newTestHubForStream(t),
		allow:  true,
		replay: ws.SessionReplay{Found: true, Active: true, Reset: true},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?last_seq=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	last := frames[len(frames)-1]
	if last["type"] != "stream.end" || last["reason"] != "replay_truncated" {
		t.Fatalf("last frame = %v, want stream.end/replay_truncated for a resuming caller", last)
	}
}

// Finding 6. parseRunStreamQuery rejected only secs < 0 and clamped only above
// the ceiling, so idle=0 left the timeout arm unreachable. With follow=true
// the stream then ended only when the client closed the socket — any
// authenticated member could pin goroutines, 512-frame observer buffers and
// sockets until the server ran out of file descriptors.
func TestParseRunStreamQuery_IdleIsAlwaysBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  time.Duration
	}{
		{"unset uses the default", "", runStreamDefaultIdle},
		{"zero is not a disable switch", "idle=0", runStreamDefaultIdle},
		{"a real value is honoured", "idle=45", 45 * time.Second},
		{"above the ceiling is clamped", "idle=99999", runStreamMaxIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/chats/c_1/stream?"+tc.query, nil)
			_, _, idle, err := parseRunStreamQuery(r)
			if err != nil {
				t.Fatalf("parseRunStreamQuery: %v", err)
			}
			if idle != tc.want {
				t.Errorf("idle = %s, want %s", idle, tc.want)
			}
			if idle <= 0 || idle > runStreamMaxIdle {
				t.Errorf("idle = %s is outside (0, %s]; an unbounded stream is a file-descriptor leak with a URL", idle, runStreamMaxIdle)
			}
		})
	}
}

// Finding 5. WriteTimeout is deliberately unset on the server, so a client
// that stops reading without closing produces TCP zero-window and Write blocks
// forever — parked inside Write, not in pump's select, so neither ctx.Done()
// nor the idle timer can fire and the deferred RemoveObserver never runs. Each
// one leaks a goroutine, a socket and a hub observer that dispatch keeps
// iterating. The writer must set a per-frame deadline.
func TestRunStreamWriter_SetsAWriteDeadline(t *testing.T) {
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	st := &runStreamWriter{w: rec, flusher: rec, rc: http.NewResponseController(rec)}
	st.write(runStreamFrame{Type: "stream.heartbeat"})

	if rec.deadlines == 0 {
		t.Fatal("no write deadline was set; a non-reading client parks this goroutine forever")
	}
}

// A write that fails must stop the pump rather than spin: the connection is
// gone and every further frame is wasted work on a leaked observer.
func TestRunStreamWriter_RecordsWriteFailure(t *testing.T) {
	rec := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), writeErr: http.ErrHandlerTimeout}
	st := &runStreamWriter{w: rec, flusher: rec, rc: http.NewResponseController(rec)}
	st.write(runStreamFrame{Type: "text", Content: "x"})

	if !st.failed() {
		t.Fatal("writer did not record the write failure; pump would keep writing to a dead connection")
	}
}

// deadlineRecorder counts SetWriteDeadline calls and can force a write error.
// http.NewResponseController finds SetWriteDeadline by interface assertion on
// the ResponseWriter, so declaring it here is what makes it reachable.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines int
	writeErr  error
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error { d.deadlines++; return nil }

func (d *deadlineRecorder) Write(b []byte) (int, error) {
	if d.writeErr != nil {
		return 0, d.writeErr
	}
	return d.ResponseRecorder.Write(b)
}

// waitForObserver blocks until the handler has attached, so a test's broadcast
// cannot race the subscription.
func waitForObserver(t *testing.T, hub *ws.Hub, channel string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.ObserverCount(channel) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("handler never attached an observer to %s", channel)
}

// Finding 1, seen from the HTTP side: when the hub's re-authorization sweep
// detaches a stream whose caller lost access, the response must end with a
// reason the caller will NOT retry against. Reporting it as an ordinary close
// would send the CLI straight back into its reconnect loop.
func TestRunStream_RevocationEndsTheStream(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{Hub: hub, allow: true, replay: ws.SessionReplay{Found: true, Active: true}}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	waitForObserver(t, hub, "session:c_1")

	// The verb the sweep uses when CanSubscribe returns a definitive deny.
	if n := hub.RevokeObservers("session:c_1", "u1"); n != 1 {
		t.Fatalf("RevokeObservers detached %d observers, want 1", n)
	}

	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	last := frames[len(frames)-1]
	if last["type"] != "stream.end" || last["reason"] != "access_revoked" {
		t.Fatalf("last frame = %v, want stream.end/access_revoked", last)
	}
}

// Revocation is scoped to the user who lost access — it must not tear down a
// colleague's stream on the same chat.
func TestRunStream_RevocationIsScopedToTheUser(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{Hub: hub, allow: true, replay: ws.SessionReplay{Found: true, Active: true}}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	waitForObserver(t, hub, "session:c_1")

	if n := hub.RevokeObservers("session:c_1", "someone-else"); n != 0 {
		t.Fatalf("RevokeObservers detached %d observers for an unrelated user, want 0", n)
	}
	if hub.ObserverCount("session:c_1") != 1 {
		t.Fatal("an unrelated user's revocation detached this stream")
	}
}
