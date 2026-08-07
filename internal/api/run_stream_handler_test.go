package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// fakeRunStreamSource embeds a REAL hub so AddObserver/RemoveObserver return
// real observers fed by real dispatch (hub.Broadcast in the tests below), and
// overrides only the two decisions a test wants to drive: what the replay
// buffer holds and whether the caller is authorized. Constructing a hub run
// from the api package is not possible — sessionStreams.begin is unexported —
// and faking the fan-out would test the fake instead of the wiring.
type fakeRunStreamSource struct {
	*ws.Hub
	replay ws.SessionReplay
	allow  bool
	err    error
}

func (f *fakeRunStreamSource) ReplaySession(string, int64) ws.SessionReplay { return f.replay }

func (f *fakeRunStreamSource) CanSubscribeChannel(context.Context, string, string) (bool, error) {
	return f.allow, f.err
}

// newRunStreamTestServer wires the handler behind a real HTTP server with an
// authenticated context injected, so the test exercises actual chunked
// streaming rather than a ResponseRecorder's buffer.
func newRunStreamTestServer(t *testing.T, src runStreamSource, userID string) *httptest.Server {
	t.Helper()
	h := NewRunStreamHandler(src, newTestLogger())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/chats/{chatId}/stream", func(w http.ResponseWriter, r *http.Request) {
		if userID != "" {
			r = r.WithContext(context.WithValue(r.Context(), ctxUser, &AuthUser{ID: userID}))
		}
		h.Stream(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestHubForStream(t *testing.T) *ws.Hub {
	t.Helper()
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { hub.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return hub
}

// readFrames reads NDJSON lines until the stream closes or the deadline hits.
func readFrames(t *testing.T, body io.Reader, want int, deadline time.Duration) []map[string]any {
	t.Helper()
	type result struct {
		frames []map[string]any
	}
	out := make(chan result, 1)
	go func() {
		var frames []map[string]any
		sc := bufio.NewScanner(body)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var f map[string]any
			if err := json.Unmarshal([]byte(line), &f); err != nil {
				continue
			}
			frames = append(frames, f)
			if want > 0 && len(frames) >= want {
				break
			}
		}
		out <- result{frames}
	}()
	select {
	case r := <-out:
		return r.frames
	case <-time.After(deadline):
		t.Fatalf("timed out after %s waiting for %d NDJSON frames", deadline, want)
		return nil
	}
}

func TestRunStream_RequiresAuthentication(t *testing.T) {
	src := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: true}
	srv := newRunStreamTestServer(t, src, "") // no user in context

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// A chat the caller cannot subscribe to must answer 404, not 403: the
// authorizer cannot tell "no such chat" from "another tenant's chat", and
// answering 403 for the second would confirm the id exists.
func TestRunStream_UnauthorizedChatIsNotFound(t *testing.T) {
	src := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: false}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_other/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// A failed authorization CHECK (DB hiccup) is not a deny — it must not be
// reported as 404, which would tell the caller the chat does not exist.
func TestRunStream_AuthorizerErrorIsNotFoundNorGrant(t *testing.T) {
	src := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: false, err: ws.ErrNoChannelAuthorizer}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for a failed authorization check", resp.StatusCode)
	}
}

// With no run active and no buffer, the stream must open, say so, and close —
// not hang. An agent scripting `crewship chat stream` has to get an exit code.
func TestRunStream_NoActiveRunOpensAndEnds(t *testing.T) {
	src := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: true}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-ndjson") {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	if len(frames) != 2 {
		t.Fatalf("got %d frames %v, want stream.open + stream.end", len(frames), frames)
	}
	if frames[0]["type"] != "stream.open" {
		t.Errorf("first frame = %v, want stream.open", frames[0])
	}
	if frames[0]["chat_id"] != "c_1" {
		t.Errorf("stream.open chat_id = %v, want c_1", frames[0]["chat_id"])
	}
	if frames[1]["type"] != "stream.end" || frames[1]["reason"] != "no_active_run" {
		t.Errorf("last frame = %v, want stream.end/no_active_run", frames[1])
	}
}

// The core contract: live frames from the hub become NDJSON lines, chat_event
// payloads are flattened to a top-level type, and the terminal `done` closes
// the stream so a shell pipeline terminates.
func TestRunStream_FlattensLiveFramesAndEndsOnDone(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{
		Hub:    hub,
		allow:  true,
		replay: ws.SessionReplay{Found: true, Active: true, FromSeq: 0},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Wait until the handler has attached before broadcasting; otherwise the
	// frames race the observer registration.
	deadline := time.Now().Add(2 * time.Second)
	for hub.ObserverCount("session:c_1") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "run_begin", Channel: "session:c_1", Seq: 1, Payload: map[string]any{"from_seq": 0}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 2, Payload: ws.ChatEvent{Type: "text", Content: "hello"}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 3, Payload: ws.ChatEvent{Type: "done"}})

	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	types := make([]string, len(frames))
	for i, f := range frames {
		types[i], _ = f["type"].(string)
	}
	want := []string{"stream.open", "run_begin", "text", "done", "stream.end"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("frame types = %v, want %v", types, want)
	}
	if frames[2]["content"] != "hello" {
		t.Errorf("text frame content = %v, want hello", frames[2]["content"])
	}
	if got, ok := frames[2]["seq"].(float64); !ok || int64(got) != 2 {
		t.Errorf("text frame seq = %v, want 2", frames[2]["seq"])
	}
	if frames[4]["reason"] != "run_complete" {
		t.Errorf("stream.end reason = %v, want run_complete", frames[4]["reason"])
	}
}

// Resume: last_seq hands back the buffered gap before any live frame, and a
// frame already replayed must not be written twice when it also arrives live.
func TestRunStream_ReplaysGapAndDedupesAgainstLive(t *testing.T) {
	hub := newTestHubForStream(t)
	buffered := func(seq int64, content string) []byte {
		b, err := json.Marshal(ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: seq, Payload: ws.ChatEvent{Type: "text", Content: content}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	src := &fakeRunStreamSource{
		Hub:   hub,
		allow: true,
		replay: ws.SessionReplay{
			Found: true, Active: true, FromSeq: 1,
			Frames: [][]byte{buffered(2, "two"), buffered(3, "three")},
		},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?last_seq=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for hub.ObserverCount("session:c_1") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// seq 3 was already replayed: it must be suppressed, not duplicated.
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 3, Payload: ws.ChatEvent{Type: "text", Content: "three"}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 4, Payload: ws.ChatEvent{Type: "done"}})

	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	var seqs []int64
	for _, f := range frames {
		if s, ok := f["seq"].(float64); ok {
			seqs = append(seqs, int64(s))
		}
	}
	if len(seqs) != 3 || seqs[0] != 2 || seqs[1] != 3 || seqs[2] != 4 {
		t.Fatalf("seqs = %v, want [2 3 4] — replayed gap then live, no duplicate", seqs)
	}
}

// A truncated replay buffer cannot serve a coherent stream. The handler must
// say so rather than emit a partial run the client would render as complete.
func TestRunStream_TruncatedBufferAnnouncesReset(t *testing.T) {
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
	found := false
	for _, f := range frames {
		if f["type"] == "stream.reset" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no stream.reset frame in %v; a truncated buffer must tell the client to reload history", frames)
	}
}

// The route is registered even on a router with no hub, so the published API
// surface does not depend on runtime wiring. It must then answer 503 — never
// panic on a nil source, and never imply the chat does not exist.
func TestRunStream_NoHubIsServiceUnavailable(t *testing.T) {
	srv := newRunStreamTestServer(t, nil, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 on a router with no hub", resp.StatusCode)
	}
}

// A buffer that lingers past its run (grace TTL) must NOT be replayed: the run
// is persisted and history already returns it, so replaying would hand the
// caller a second copy. Without follow, the stream just ends.
func TestRunStream_DoesNotReplayAFinishedRun(t *testing.T) {
	stale, err := json.Marshal(ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 2, Payload: ws.ChatEvent{Type: "text", Content: "from the finished run"}})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeRunStreamSource{
		Hub:    newTestHubForStream(t),
		allow:  true,
		replay: ws.SessionReplay{Found: true, Active: false, Frames: [][]byte{stale}},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	frames := readFrames(t, resp.Body, 0, 5*time.Second)
	for _, f := range frames {
		if f["content"] == "from the finished run" {
			t.Fatalf("replayed a completed run's buffer: %v", frames)
		}
	}
	if len(frames) != 2 || frames[1]["reason"] != "no_active_run" {
		t.Fatalf("frames = %v, want stream.open + stream.end/no_active_run", frames)
	}
}

func TestRunStream_RejectsBadLastSeq(t *testing.T) {
	src := &fakeRunStreamSource{Hub: newTestHubForStream(t), allow: true}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?last_seq=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-numeric last_seq", resp.StatusCode)
	}
}

// follow=1 keeps the stream open past the run's terminal frame, so a caller
// tailing a session sees the next run too.
func TestRunStream_FollowSurvivesDone(t *testing.T) {
	hub := newTestHubForStream(t)
	src := &fakeRunStreamSource{
		Hub:    hub,
		allow:  true,
		replay: ws.SessionReplay{Found: true, Active: true},
	}
	srv := newRunStreamTestServer(t, src, "u1")

	resp, err := http.Get(srv.URL + "/api/v1/chats/c_1/stream?follow=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for hub.ObserverCount("session:c_1") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 1, Payload: ws.ChatEvent{Type: "done"}})
	hub.Broadcast("session:c_1", ws.ServerMessage{Type: "chat_event", Channel: "session:c_1", Seq: 2, Payload: ws.ChatEvent{Type: "text", Content: "next run"}})

	// stream.open, done, text — and no stream.end, because follow keeps it open.
	frames := readFrames(t, resp.Body, 3, 5*time.Second)
	if len(frames) != 3 || frames[2]["content"] != "next run" {
		t.Fatalf("frames = %v, want the post-done frame to still arrive under follow=1", frames)
	}
}
