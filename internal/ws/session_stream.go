package ws

import (
	"encoding/json"
	"sync"
	"time"
)

// Per-session replay buffering for agent-run output.
//
// Why this exists: an agent run streams events (text/thinking/tool/done) to the
// client over the session channel. If the client disconnects mid-run (navigates
// away, refresh, network blip) and later reconnects, it must be able to catch up
// on everything it missed WITHOUT the reply being lost or duplicated. We buffer
// the current run's frames keyed by a per-session monotonic sequence number and
// let a reconnecting client replay the gap (the Last-Event-ID pattern used by
// Vercel resumable-stream, Cloudflare ai-chat, LibreChat, etc.).
//
// Design:
//   - `counters[channel]` is a monotonic seq that PERSISTS across runs, so a
//     client that reconnects across a run boundary never sees seq numbers go
//     backwards (which would make client-side reassembly drop the new run).
//   - `streams[channel]` holds ONLY the current/most-recent run's frames. It is
//     reset at each run start so replay never re-emits an already-persisted
//     (completed) run — those come from chat history instead, and replaying them
//     too would double-render.
//   - Replay is offered only while a run is active (or just ended, within the
//     grace TTL). A completed+persisted run is recovered from history, so there
//     is no overlap between "what history returns" and "what replay returns".
//
// The buffer is capped; on overflow it marks itself truncated and a resuming
// client is told to reload history rather than replay a partial stream.

const (
	// sessionStreamEventCap bounds how many frames one run may buffer. A run is
	// turn-capped, so this is generous headroom; hitting it flips the buffer to
	// truncated (resume falls back to history reload).
	sessionStreamEventCap = 5000
	// sessionStreamByteCap bounds total buffered bytes per run (defense against a
	// pathological run streaming huge tool outputs).
	sessionStreamByteCap = 8 << 20 // 8 MiB
	// sessionStreamGraceTTL is how long a finished run's buffer lingers before
	// being swept, covering a client that reconnects right as the run ends.
	sessionStreamGraceTTL = 2 * time.Minute
	// sessionStreamSweepInterval is how often the hub GCs ended buffers.
	sessionStreamSweepInterval = 30 * time.Second
)

// bufferedFrame is one already-marshaled ServerMessage plus its seq.
type bufferedFrame struct {
	seq  int64
	data []byte
}

// sessionStream is the replay buffer for one channel's current run.
type sessionStream struct {
	startSeq  int64 // seq value at run start; run frames are (startSeq, ...]
	frames    []bufferedFrame
	bytes     int
	active    bool
	truncated bool
	endedAt   time.Time
}

// sessionStreams owns all per-channel replay state.
type sessionStreams struct {
	mu       sync.Mutex
	counters map[string]int64          // monotonic seq per channel (persists across runs)
	streams  map[string]*sessionStream // current-run buffer per channel (reset each run)
}

func newSessionStreams() *sessionStreams {
	return &sessionStreams{
		counters: make(map[string]int64),
		streams:  make(map[string]*sessionStream),
	}
}

// begin resets the buffer for a channel at run start and returns the baseline
// seq (the counter value before this run). The monotonic seq counter is
// preserved across runs so sequence numbers never regress.
func (s *sessionStreams) begin(channel string) int64 {
	s.mu.Lock()
	start := s.counters[channel]
	s.streams[channel] = &sessionStream{active: true, startSeq: start}
	s.mu.Unlock()
	return start
}

// record assigns the next per-channel seq to msg, marshals it, appends the frame
// to the active buffer (if any), and returns the marshaled bytes so the caller
// broadcasts the exact same seq'd frame to every recipient. When no run is
// active for the channel the frame is still marshaled but not buffered and gets
// no seq (seq 0 = "not part of a resumable stream").
func (s *sessionStreams) record(channel string, msg *ServerMessage) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.streams[channel]
	active := st != nil && st.active
	if active {
		s.counters[channel]++
		msg.Seq = s.counters[channel]
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}

	if active && !st.truncated {
		if len(st.frames) >= sessionStreamEventCap || st.bytes+len(data) > sessionStreamByteCap {
			// Overflow: drop the whole buffer's replayability. A resuming client
			// is told to reload history instead of replaying a partial stream.
			st.truncated = true
			st.frames = nil
			st.bytes = 0
		} else {
			st.frames = append(st.frames, bufferedFrame{seq: msg.Seq, data: data})
			st.bytes += len(data)
		}
	}
	return data, true
}

// end marks the run finished. The buffer lingers for the grace TTL (swept later)
// but replay is only offered while active — a finished+persisted run is served
// from history, so keeping the buffer replayable would double-render.
func (s *sessionStreams) end(channel string) {
	s.mu.Lock()
	if st := s.streams[channel]; st != nil {
		st.active = false
		st.endedAt = time.Now()
	}
	s.mu.Unlock()
}

// replayResult tells the resume handler what to send.
type replayResult struct {
	// fromSeq is the authoritative baseline: the client should set its
	// last-applied seq to this and expect contiguous frames from fromSeq+1.
	fromSeq int64
	frames  [][]byte
	// active is true if a run is still generating (client should stay live).
	active bool
	// reset is true when the buffer can't serve a coherent replay (truncated);
	// the client should reload history instead.
	reset bool
	// found is false when there's no buffer at all for the channel (nothing to
	// resume — history already covers it).
	found bool
}

// replay returns the frames a resuming client needs, given the last seq it has
// already applied. It clamps the baseline to the run's start so a client that
// joins mid-counter (a fresh tab on a chat whose channel already streamed
// earlier runs) doesn't wait forever for pre-run sequence numbers.
func (s *sessionStreams) replay(channel string, afterSeq int64) replayResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.streams[channel]
	if st == nil {
		return replayResult{found: false}
	}
	if st.truncated {
		return replayResult{found: true, reset: true, active: st.active}
	}

	from := afterSeq
	if from < st.startSeq {
		from = st.startSeq
	}
	var frames [][]byte
	for _, f := range st.frames {
		if f.seq > from {
			frames = append(frames, f.data)
		}
	}
	return replayResult{fromSeq: from, frames: frames, active: st.active, found: true}
}

// sweep drops ended buffers older than the grace TTL. Counters are retained
// (cheap, one int64 per channel) so seq numbers stay monotonic if the same chat
// runs again later.
func (s *sessionStreams) sweep(now time.Time) {
	s.mu.Lock()
	for ch, st := range s.streams {
		if !st.active && now.Sub(st.endedAt) > sessionStreamGraceTTL {
			delete(s.streams, ch)
		}
	}
	s.mu.Unlock()
}
