package ws

import (
	"encoding/json"
	"testing"
	"time"
)

func seqOf(t *testing.T, data []byte) int64 {
	t.Helper()
	var m ServerMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	return m.Seq
}

func recordEvent(t *testing.T, s *sessionStreams, channel, content string) []byte {
	t.Helper()
	msg := &ServerMessage{Type: "chat_event", Channel: channel, Payload: ChatEvent{Type: "text", Content: content}}
	data, ok := s.record(channel, msg)
	if !ok {
		t.Fatalf("record returned !ok")
	}
	return data
}

func TestSessionStream_SeqMonotonicWithinRun(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	if start := s.begin(ch); start != 0 {
		t.Fatalf("first run startSeq = %d, want 0", start)
	}
	if got := seqOf(t, recordEvent(t, s, ch, "a")); got != 1 {
		t.Errorf("first seq = %d, want 1", got)
	}
	if got := seqOf(t, recordEvent(t, s, ch, "b")); got != 2 {
		t.Errorf("second seq = %d, want 2", got)
	}
}

func TestSessionStream_SeqNeverRegressesAcrossRuns(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	recordEvent(t, s, ch, "a") // seq 1
	recordEvent(t, s, ch, "b") // seq 2
	s.end(ch)

	// Second run on the SAME channel must continue the counter, not reset to 1 —
	// else a client that reconnects across the boundary would see seq go
	// backwards and drop the new run as "already seen".
	start := s.begin(ch)
	if start != 2 {
		t.Fatalf("second run startSeq = %d, want 2", start)
	}
	if got := seqOf(t, recordEvent(t, s, ch, "c")); got != 3 {
		t.Errorf("first seq of run 2 = %d, want 3", got)
	}
}

func TestSessionStream_ReplayReturnsGapAfterSeq(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	recordEvent(t, s, ch, "a") // 1
	recordEvent(t, s, ch, "b") // 2
	recordEvent(t, s, ch, "c") // 3

	res := s.replay(ch, 1)
	if !res.found || !res.active {
		t.Fatalf("replay found=%v active=%v, want both true", res.found, res.active)
	}
	if res.fromSeq != 1 {
		t.Errorf("fromSeq = %d, want 1", res.fromSeq)
	}
	if len(res.frames) != 2 {
		t.Fatalf("frames = %d, want 2 (seq 2,3)", len(res.frames))
	}
	if seqOf(t, res.frames[0]) != 2 || seqOf(t, res.frames[1]) != 3 {
		t.Errorf("replay seqs = %d,%d, want 2,3", seqOf(t, res.frames[0]), seqOf(t, res.frames[1]))
	}
}

func TestSessionStream_ReplayFromZeroClampsToRunStart(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	// Prior run advanced the counter to 2.
	s.begin(ch)
	recordEvent(t, s, ch, "a")
	recordEvent(t, s, ch, "b")
	s.end(ch)

	// New run; a fresh client resumes with last_seq=0.
	s.begin(ch)
	recordEvent(t, s, ch, "c") // seq 3

	res := s.replay(ch, 0)
	// Baseline must clamp to the run's start (2), NOT 0 — the client must not
	// wait for seq 1,2 which belong to the previous (already-persisted) run.
	if res.fromSeq != 2 {
		t.Errorf("fromSeq = %d, want 2 (run start)", res.fromSeq)
	}
	if len(res.frames) != 1 || seqOf(t, res.frames[0]) != 3 {
		t.Fatalf("frames = %d, want 1 frame seq 3", len(res.frames))
	}
}

func TestSessionStream_ReplayInactiveAfterEnd(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	recordEvent(t, s, ch, "a")
	s.end(ch)

	res := s.replay(ch, 0)
	if !res.found {
		t.Fatalf("found = false, want true (buffer lingers in grace window)")
	}
	if res.active {
		t.Errorf("active = true, want false after end (history serves a finished run)")
	}
}

func TestSessionStream_NoBufferReturnsNotFound(t *testing.T) {
	s := newSessionStreams()
	res := s.replay("session:never", 0)
	if res.found {
		t.Errorf("found = true, want false for a channel with no run")
	}
}

func TestSessionStream_TruncationSignalsReset(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	for i := 0; i < sessionStreamEventCap+5; i++ {
		recordEvent(t, s, ch, "x")
	}
	res := s.replay(ch, 0)
	if !res.reset {
		t.Errorf("reset = false, want true after exceeding the event cap")
	}
}

func TestSessionStream_RecordWithoutActiveRunGetsNoSeq(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	// No begin() — event outside a run must marshal but not get a seq or buffer.
	data := recordEvent(t, s, ch, "stray")
	if got := seqOf(t, data); got != 0 {
		t.Errorf("seq = %d, want 0 for a frame outside a run", got)
	}
	if res := s.replay(ch, 0); res.found {
		t.Errorf("found = true, want false — nothing should have been buffered")
	}
}

func TestSessionStream_ConcurrentRunsShareBufferNoClobber(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)                 // run A → activeRuns 1
	recordEvent(t, s, ch, "a1") // seq 1
	startB := s.begin(ch)       // run B concurrent → activeRuns 2, must NOT wipe A
	if startB != 1 {
		t.Fatalf("run B startSeq = %d, want 1 (shared counter)", startB)
	}
	recordEvent(t, s, ch, "b1") // seq 2

	res := s.replay(ch, 0)
	if len(res.frames) != 2 {
		t.Fatalf("frames = %d, want 2 — a concurrent begin must not clobber the first run's buffer", len(res.frames))
	}
	if !res.active {
		t.Fatal("active = false while two runs are in flight")
	}
	// One run ends — still active (refcount).
	s.end(ch)
	if r := s.replay(ch, 0); !r.active {
		t.Fatal("buffer went inactive after only one of two runs ended (refcount broken)")
	}
	// Last run ends — now inactive.
	s.end(ch)
	if r := s.replay(ch, 0); r.active {
		t.Fatal("buffer still active after the last run ended")
	}
}

func TestSessionStream_SweepReclaimsIdleCounter(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	recordEvent(t, s, ch, "a")
	s.end(ch)
	// Age the stream past its grace TTL and the counter past its (longer) TTL.
	s.mu.Lock()
	s.streams[ch].endedAt = time.Now().Add(-2 * sessionStreamGraceTTL)
	s.counters[ch].touchedAt = time.Now().Add(-2 * sessionStreamCounterTTL)
	s.mu.Unlock()
	s.sweep(time.Now())

	s.mu.Lock()
	_, hasCounter := s.counters[ch]
	s.mu.Unlock()
	if hasCounter {
		t.Error("idle counter not reclaimed after its buffer was swept and it aged past the TTL")
	}
}

func TestSessionStream_SweepDropsEndedButKeepsCounter(t *testing.T) {
	s := newSessionStreams()
	ch := "session:c1"
	s.begin(ch)
	recordEvent(t, s, ch, "a") // seq 1
	recordEvent(t, s, ch, "b") // seq 2
	s.end(ch)

	// Force the ended buffer past its grace TTL and sweep.
	s.mu.Lock()
	s.streams[ch].endedAt = time.Now().Add(-2 * sessionStreamGraceTTL)
	s.mu.Unlock()
	s.sweep(time.Now())

	if res := s.replay(ch, 0); res.found {
		t.Errorf("found = true after sweep, want false (buffer GC'd)")
	}
	// Counter must survive the sweep so the next run keeps seq monotonic.
	if start := s.begin(ch); start != 2 {
		t.Errorf("startSeq after sweep = %d, want 2 (counter retained)", start)
	}
}
