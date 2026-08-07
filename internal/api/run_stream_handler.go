package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Watching an agent run over plain HTTP (#1818).
//
// Before this, the only way to see a run's output was the WebSocket path:
// GET /api/v1/ws-token to mint a short-lived JWT, upgrade, subscribe to
// `session:{chatId}`. That is the right shape for the browser and the wrong
// shape for an agent driving Crewship from a shell, which would have to
// implement a WebSocket client and a token dance before it could read one line
// of output. This handler streams the SAME frames as newline-delimited JSON
// over the ordinary authenticated HTTP surface: `curl -N` is a sufficient
// client, and so is `crewship chat stream`.
//
// It is additive. The WebSocket path is untouched and the browser keeps using
// it. Both read the one event source (ws.Hub) and the one replay buffer
// (internal/ws/session_stream.go) so they cannot drift apart on ordering,
// sequence numbers, or who is allowed to watch what.
//
// Streaming mechanics follow the precedent already in this codebase —
// JournalHandler.Stream: assert http.Flusher up front, set no-buffering
// headers, Flush after every write, and return the moment the request context
// is done.

// runStreamSource is the slice of ws.Hub this handler needs. Narrow by design:
// it keeps the handler testable without a live socket, and it documents that
// the HTTP stream is a READER of hub state — it never starts, cancels, or
// otherwise steers a run.
type runStreamSource interface {
	// AddObserver attaches a non-socket subscriber to a channel.
	AddObserver(channel, userID string, buffer int) *ws.Observer
	// RemoveObserver detaches it and closes its frame channel.
	RemoveObserver(channel string, o *ws.Observer)
	// ReplaySession returns the buffered gap for a resuming caller.
	ReplaySession(channel string, afterSeq int64) ws.SessionReplay
	// CanSubscribeChannel is the same authorization the WS subscribe/resume
	// paths apply. (false, nil) is a definitive deny; (false, err) is a failed
	// check. Both deny; only the second is a server fault.
	CanSubscribeChannel(ctx context.Context, userID, channel string) (bool, error)
}

// RunStreamHandler serves the NDJSON view of a chat session's agent run.
type RunStreamHandler struct {
	src    runStreamSource
	logger *slog.Logger
}

// NewRunStreamHandler wires the handler around a hub (or any runStreamSource).
func NewRunStreamHandler(src runStreamSource, logger *slog.Logger) *RunStreamHandler {
	return &RunStreamHandler{src: src, logger: logger}
}

const (
	// runStreamObserverBuffer is how many frames may queue for one HTTP reader
	// before the hub starts dropping. Sized well above the WebSocket client's
	// 64 because an HTTP reader has no application-level flow control: if it
	// falls behind we cannot slow the run down, we can only drop and tell the
	// caller to resume. 512 covers a burst of tool output over a slow link
	// without letting one stalled `curl` pin megabytes.
	runStreamObserverBuffer = 512

	// runStreamHeartbeatInterval bounds how long the connection can sit
	// silent. Idle HTTP responses are exactly what reverse proxies and NAT
	// tables reap; the journal SSE stream solves this with `:` comments, and
	// NDJSON's equivalent is a frame the client is told to ignore.
	runStreamHeartbeatInterval = 20 * time.Second

	// runStreamDefaultIdle is how long the stream waits for a frame before
	// giving up. It applies to REAL frames only — heartbeats do not reset it,
	// or the stream would never time out. Chosen long enough to cover an agent
	// thinking between tool calls, short enough that a forgotten `curl` in a
	// script does not hang a CI job forever.
	runStreamDefaultIdle = 5 * time.Minute

	// runStreamMaxIdle caps what a caller may ask for. Without a ceiling,
	// `?idle=86400` turns into a file-descriptor leak with a URL. There is
	// deliberately no way to disable the idle timeout: an unbounded stream that
	// only ends when the client closes lets any authenticated member pin
	// goroutines, observer buffers and sockets until the server runs out of
	// file descriptors.
	runStreamMaxIdle = time.Hour

	// runStreamWriteTimeout bounds a single frame write. See runStreamWriter.write
	// for why an unbounded one is a goroutine leak rather than a slow client.
	// Generous enough for a large tool_result over a slow link.
	runStreamWriteTimeout = 30 * time.Second
)

// runStreamFrame is one NDJSON line.
//
// Two families share the struct, distinguished by Type:
//
//   - Run frames carry the agent event verbatim: `run_begin`, `text`,
//     `thinking`, `tool_call`, `tool_result`, `done`, `error`, plus whatever
//     else ws.ChatEvent grows. The nested {"type":"chat_event","payload":{…}}
//     envelope the socket uses is FLATTENED here, because the caller is a
//     shell pipeline: `jq -r 'select(.type=="text").content'` should not need
//     to know about the transport's envelope.
//   - Control frames are namespaced with a `stream.` prefix — `stream.open`,
//     `stream.heartbeat`, `stream.reset`, `stream.end`. The dot is what makes
//     the flattening safe: no ws.ChatEvent type contains one, so a caller can
//     always tell an agent event from a transport event.
type runStreamFrame struct {
	Type string `json:"type"`
	// Seq is the per-session monotonic sequence number from session_stream.go.
	// Feed the last one you saw back as `?last_seq=` to resume. Absent on
	// control frames, which are not part of the resumable sequence.
	Seq int64 `json:"seq,omitempty"`
	// Content / Metadata mirror ws.ChatEvent.
	Content  string `json:"content,omitempty"`
	Metadata any    `json:"metadata,omitempty"`

	// Control-frame fields.
	ChatID  string `json:"chat_id,omitempty"`
	FromSeq *int64 `json:"from_seq,omitempty"`
	Active  *bool  `json:"active,omitempty"`
	Reason  string `json:"reason,omitempty"`
	LastSeq *int64 `json:"last_seq,omitempty"`
}

// Stream serves GET /api/v1/chats/{chatId}/stream.
//
// Query parameters:
//
//	last_seq=<n>  resume watermark — replay buffered frames with seq > n.
//	              The `Last-Event-ID` header is accepted as an alias (that is
//	              the header the journal SSE stream and every browser
//	              EventSource already speak); the query parameter wins.
//	follow=1      stay open past the run's terminal `done` frame, so the next
//	              run on the same session streams too. Default off: the common
//	              case is "watch this run and exit with a status".
//	idle=<secs>   give up after this long with no run frame. Default 300,
//	              max 3600, 0 disables (bounded only by follow/done).
func (h *RunStreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		writeProblem(w, r, http.StatusUnauthorized, "authentication required")
		return
	}
	if h.src == nil {
		// Router wired without a hub (CLI-only build, some test routers). The
		// route still exists so the published API surface does not depend on
		// runtime wiring; it just cannot serve.
		writeProblem(w, r, http.StatusServiceUnavailable, "run streaming is not available on this server")
		return
	}
	chatID := r.PathValue("chatId")
	if chatID == "" {
		writeProblem(w, r, http.StatusBadRequest, "chat id required")
		return
	}

	lastSeq, follow, idle, err := parseRunStreamQuery(r)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, err.Error())
		return
	}

	channel := "session:" + chatID
	// Same gate as WS subscribe/resume, via the same authorizer — see
	// ws.Hub.CanSubscribeChannel. A failed CHECK is not a deny verdict: it is a
	// 503, because answering 404 would tell the caller the chat does not exist
	// when the truth is that we could not find out.
	allowed, authErr := h.src.CanSubscribeChannel(r.Context(), user.ID, channel)
	if authErr != nil {
		// 503, not 404: a DB hiccup (or a hub with no authorizer wired at all,
		// ws.ErrNoChannelAuthorizer) means we could not find out whether this
		// caller may watch — saying "not found" would assert something we do
		// not know. Both still deny, which is the fail-closed rule every grant
		// path in the hub follows.
		if h.logger != nil {
			h.logger.Warn("run stream authorization check failed", "error", authErr, "chat_id", chatID, "user_id", user.ID)
		}
		writeProblem(w, r, http.StatusServiceUnavailable, "could not verify access to this chat")
		return
	}
	if !allowed {
		// 404, not 403. The authorizer cannot distinguish "no such chat" from
		// "another tenant's chat", and 403 on the second would confirm the id
		// exists — the cross-tenant enumeration shape the journal single-entry
		// GET already closes the same way.
		writeProblem(w, r, http.StatusNotFound, "chat not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, r, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx/Caddy: don't buffer
	w.WriteHeader(http.StatusOK)

	// Attach BEFORE reading the replay buffer. The other order has a hole: a
	// frame emitted between the replay snapshot and the attach would be in
	// neither, and the client would never see it. Attaching first can only
	// produce DUPLICATES (a frame both replayed and delivered live), and
	// duplicates are removable — writeFrame drops anything whose seq we have
	// already written.
	obs := h.src.AddObserver(channel, user.ID, runStreamObserverBuffer)
	defer h.src.RemoveObserver(channel, obs)

	replay := h.src.ReplaySession(channel, lastSeq)

	st := &runStreamWriter{w: w, flusher: flusher, rc: http.NewResponseController(w), lastSeq: lastSeq}
	fromSeq := replay.FromSeq
	active := replay.Active
	st.write(runStreamFrame{Type: "stream.open", ChatID: chatID, FromSeq: &fromSeq, Active: &active})

	// resuming distinguishes "catch me up from where I was" from a first
	// attach. Only the former can be harmed by a buffer it cannot read.
	resuming := lastSeq > 0
	terminatedInReplay := false

	switch {
	case replay.Reset && resuming:
		// The buffer overflowed mid-run, so the gap this caller asked for is
		// genuinely gone. Say so and stop: emitting the surviving tail would
		// render as a complete run to a client that has no way to know the head
		// is missing. Chat history is the recovery path.
		st.write(runStreamFrame{Type: "stream.reset", Reason: "replay_truncated"})
		st.end("replay_truncated")
		return
	case replay.Reset:
		// Truncated, but this caller is NOT resuming — it asked for no replay,
		// so nothing it wanted has been lost and it can be served live from
		// here. Failing it outright (the previous behaviour) was worse than
		// useless: `truncated` is sticky for the whole run, so every fresh
		// attach for the rest of a long run got an instant non-retryable error,
		// and the fallback we point at is empty because the run is not
		// persisted yet. Announce that earlier output is unavailable — as an
		// informational reset, not a terminal one — and stream on.
		st.write(runStreamFrame{Type: "stream.reset", Reason: "replay_truncated"})
	case replay.Active:
		// Replay ONLY while a run is still generating — the same rule
		// Client.handleResume applies, and for the same reason: a finished run
		// is already persisted, so replaying its buffer here as well would hand
		// the caller a second copy of what chat history returns. A buffer that
		// lingers past its run (grace TTL, see session_stream.go) is therefore
		// deliberately not replayed.
		//
		// Adopt the run's baseline in BOTH directions before replaying. The seq
		// counter is in-memory, so a restart resets it while callers still hold
		// a watermark from the previous lifetime — one we handed them ourselves
		// as `last_seq`. Clamping only upward left st.lastSeq above every live
		// frame, and the dedupe below then silently dropped the entire run,
		// `done` included. The buffer is the authority on where this run sits.
		st.lastSeq = replay.FromSeq
		for _, frame := range replay.Frames {
			if st.writeHubFrame(frame) {
				// The gap contained the run's terminal frame. Record it: the
				// live duplicate is suppressed by the seq dedupe, so if this is
				// dropped nothing else can ever end the stream and the caller
				// waits out the full idle timeout for a run that is over.
				terminatedInReplay = true
			}
		}
	}
	st.flush()

	if terminatedInReplay && !follow {
		st.end("run_complete")
		return
	}

	if !follow && !replay.Active {
		// Nothing is generating. Close with a reason rather than hold the
		// connection: a script needs an exit status, and history already covers
		// anything that finished.
		//
		// There is an unavoidable race here — a run that begins between the
		// attach above and this check reads as "not active" and the stream
		// closes just before it would have delivered anything. `follow=1` is
		// the answer for a caller that wants to wait for a run rather than
		// watch one already in flight; the idle timeout still bounds the wait.
		st.end("no_active_run")
		return
	}

	h.pump(r.Context(), st, obs, follow, idle)
}

// pump is the live phase: copy hub frames to the response until a terminal
// condition. Kept separate from Stream so the setup (auth, headers, replay)
// reads as one linear story.
func (h *RunStreamHandler) pump(ctx context.Context, st *runStreamWriter, obs *ws.Observer, follow bool, idle time.Duration) {
	heartbeat := time.NewTicker(runStreamHeartbeatInterval)
	defer heartbeat.Stop()

	// The idle timer is always armed. parseRunStreamQuery guarantees a bound in
	// (0, runStreamMaxIdle]; the floor here restates that invariant locally so
	// a future caller of pump cannot reintroduce an unbounded stream. It is
	// reset by real frames, never by heartbeats — a heartbeat proves the socket
	// is alive, not that the run is.
	if idle <= 0 {
		idle = runStreamDefaultIdle
	}
	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()
	idleC := idleTimer.C

	for {
		select {
		case <-ctx.Done():
			// Client hung up (or the server is shutting down). Nothing to write
			// — the connection is already gone.
			return
		case <-idleC:
			st.end("idle_timeout")
			return
		case <-heartbeat.C:
			st.write(runStreamFrame{Type: "stream.heartbeat"})
			st.flush()
			if st.failed() {
				// The heartbeat is what detects a client that stopped reading
				// without closing: the write hits its deadline and latches.
				// Return so the deferred RemoveObserver actually runs.
				return
			}
		case data, open := <-obs.Frames():
			if !open {
				if obs.Revoked() {
					// The hub's re-authorization sweep took this stream's
					// access away (workspace membership removed). A distinct,
					// terminal reason — reporting it as an ordinary close would
					// send the CLI straight back into its reconnect loop
					// against a chat it may no longer read.
					st.end("access_revoked")
					return
				}
				st.end("stream_closed")
				return
			}
			if obs.Dropped() {
				// A frame was lost to backpressure. The stream now has a hole,
				// so end it honestly and hand back the watermark: the caller
				// reconnects with ?last_seq=<n> and the replay buffer fills the
				// gap. Pretending otherwise would ship a truncated transcript
				// that looks whole.
				st.write(runStreamFrame{Type: "stream.reset", Reason: "slow_consumer"})
				st.end("slow_consumer")
				return
			}
			terminal := st.writeHubFrame(data)
			st.flush()
			if st.failed() {
				return
			}
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idle)
			if terminal && !follow {
				st.end("run_complete")
				return
			}
		}
	}
}

// runStreamWriter serializes frames to the response and tracks the highest seq
// already written, which is what makes replay-then-live safe to concatenate.
type runStreamWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	rc      *http.ResponseController
	lastSeq int64
	// writeErr latches the first failed write.
	writeErr error
}

// failed reports whether a write has already failed, so the pump can stop
// rather than keep serialising frames into a dead connection.
func (s *runStreamWriter) failed() bool { return s.writeErr != nil }

func (s *runStreamWriter) write(f runStreamFrame) {
	if s.writeErr != nil {
		return
	}
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	// A per-frame write deadline is what keeps this goroutine reclaimable.
	// internal/server/server.go leaves http.Server.WriteTimeout unset (streams
	// are long-lived by design), so without a deadline a client that stops
	// reading WITHOUT closing — TCP zero-window — blocks this Write forever.
	// That parks the goroutine inside Write rather than in pump's select, so
	// neither the request context nor the idle timer can fire, and the deferred
	// RemoveObserver never runs: one leaked goroutine, socket and hub observer
	// per stuck client, with dispatch still iterating the observer on every
	// frame. errors.ErrUnsupported is tolerated so a ResponseWriter without
	// deadline support (some middleware wrappers, test recorders) still works.
	if s.rc != nil {
		if derr := s.rc.SetWriteDeadline(time.Now().Add(runStreamWriteTimeout)); derr != nil && !errors.Is(derr, errors.ErrUnsupported) {
			s.writeErr = derr
			return
		}
	}
	if _, werr := s.w.Write(append(data, '\n')); werr != nil {
		// Latch it. The connection is gone; every further frame is wasted work
		// on a stream nobody is reading, and pump uses this to tear down.
		s.writeErr = werr
	}
}

func (s *runStreamWriter) flush() { s.flusher.Flush() }

// end writes the terminal control frame with the resume watermark, so a caller
// that wants to reconnect knows exactly where to pick up.
func (s *runStreamWriter) end(reason string) {
	last := s.lastSeq
	s.write(runStreamFrame{Type: "stream.end", Reason: reason, LastSeq: &last})
	s.flush()
}

// writeHubFrame translates one already-marshaled ws.ServerMessage into an
// NDJSON line. Reports whether the frame terminates the run.
//
// Frames whose seq we have already written are dropped — that is the dedupe
// that makes "attach, then replay" correct (see Stream). Frames with seq 0 are
// not part of the resumable sequence (heartbeats, control frames, broadcasts
// emitted outside a run) and are passed through without touching the
// watermark.
func (s *runStreamWriter) writeHubFrame(data []byte) bool {
	var msg ws.ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return false
	}
	if msg.Seq != 0 {
		if msg.Seq <= s.lastSeq {
			return false
		}
		s.lastSeq = msg.Seq
	}

	switch msg.Type {
	case "chat_event":
		var ev ws.ChatEvent
		// Payload came off the wire as generic JSON; re-marshal narrowly rather
		// than reaching into a map, so a ChatEvent field added later flows
		// through here without another edit.
		raw, err := json.Marshal(msg.Payload)
		if err != nil || json.Unmarshal(raw, &ev) != nil {
			return false
		}
		s.write(runStreamFrame{Type: ev.Type, Seq: msg.Seq, Content: ev.Content, Metadata: ev.Metadata})
		// `done` is the run's terminal frame. An `error` is always followed by
		// a `done` (ws/client.go emits the pair), so terminating on `done`
		// alone cannot strand a caller after a failure.
		return ev.Type == "done"
	case "run_begin":
		f := runStreamFrame{Type: "run_begin", Seq: msg.Seq}
		if payload, ok := msg.Payload.(map[string]any); ok {
			if v, ok := payload["from_seq"].(float64); ok {
				from := int64(v)
				f.FromSeq = &from
			}
		}
		s.write(f)
		return false
	case "ping", "pong":
		// Socket-level heartbeats never reach dispatch, but be explicit: they
		// are not run output and must not appear in a transcript.
		return false
	default:
		// Anything else broadcast on the session channel (resume_reset, future
		// control types) is passed through under its own name rather than
		// dropped — a caller that does not recognise it can ignore it, but a
		// silent drop would hide a real event.
		s.write(runStreamFrame{Type: msg.Type, Seq: msg.Seq})
		return false
	}
}

// parseRunStreamQuery reads and validates the three knobs. A malformed value is
// a 400 rather than a silent default: an agent that typos `?last_seq=abc` must
// be told, not handed the whole run again.
func parseRunStreamQuery(r *http.Request) (lastSeq int64, follow bool, idle time.Duration, err error) {
	q := r.URL.Query()

	raw := strings.TrimSpace(q.Get("last_seq"))
	if raw == "" {
		// Last-Event-ID is the header every EventSource and our own journal SSE
		// client already send on reconnect. Accepting it here means one resume
		// convention across both streaming endpoints.
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw != "" {
		n, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || n < 0 {
			return 0, false, 0, errors.New("last_seq must be a non-negative integer")
		}
		lastSeq = n
	}

	if v := strings.TrimSpace(q.Get("follow")); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return 0, false, 0, errors.New("follow must be a boolean")
		}
		follow = b
	}

	idle = runStreamDefaultIdle
	if v := strings.TrimSpace(q.Get("idle")); v != "" {
		secs, perr := strconv.Atoi(v)
		if perr != nil || secs < 0 {
			return 0, false, 0, errors.New("idle must be a non-negative number of seconds")
		}
		// 0 means "use the server default", NOT "disable". It used to disable,
		// which left the timeout arm unreachable: with follow=1 the stream then
		// ended only when the client closed, so any authenticated member could
		// hold streams open indefinitely and exhaust file descriptors. There is
		// no way to opt out of the bound — only to choose one within the ceiling.
		if secs > 0 {
			idle = time.Duration(secs) * time.Second
		}
		if idle > runStreamMaxIdle {
			idle = runStreamMaxIdle
		}
	}
	return lastSeq, follow, idle, nil
}
