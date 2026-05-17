package consolidate

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestPostRunTrigger_FiresOnFirstCall asserts the trigger kicks off
// consolidation when no debounce window is active.
func TestPostRunTrigger_FiresOnFirstCall(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	seedEntries(t, db, w, "ws_x", "crew_y", 12, journal.EntryPeerEscalation)

	var summCalls atomic.Int32
	summ := &countingSummarizer{counter: &summCalls, reply: `[]`}
	c := &Consolidator{DB: db, Journal: w, Summarizer: summ, Logger: quietLogger()}

	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:       time.Minute,
		CrewMemoryRoot: t.TempDir(),
		Since:          time.Hour,
		MinEntries:     5,
	})

	fired := tr.OnRunCompleted(context.Background(), "ws_x", "crew_y", "crew-y-slug")
	if !fired {
		t.Fatalf("first call should fire, returned false")
	}
	// The goroutine runs asynchronously; give it a generous moment
	// to call the summarizer. 200ms is comfortably enough for a
	// stub summarizer that returns instantly; bumps to 500ms only
	// if loaded systems flake.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && summCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if summCalls.Load() == 0 {
		t.Errorf("consolidator did not run within 500ms of OnRunCompleted")
	}
}

// TestPostRunTrigger_DebouncesSecondCall asserts a second call
// inside the debounce window is a no-op.
func TestPostRunTrigger_DebouncesSecondCall(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	seedEntries(t, db, w, "ws_x", "crew_y", 12, journal.EntryPeerEscalation)

	var summCalls atomic.Int32
	summ := &countingSummarizer{counter: &summCalls, reply: `[]`}
	c := &Consolidator{DB: db, Journal: w, Summarizer: summ, Logger: quietLogger()}

	// Fixed clock — first call fires at t=0, second at t=10s
	// which is well inside the 1-minute debounce window.
	nowAt := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: nowAt}

	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:       time.Minute,
		CrewMemoryRoot: t.TempDir(),
		Since:          time.Hour,
		MinEntries:     5,
		Now:            clock.Now,
	})

	if !tr.OnRunCompleted(context.Background(), "ws_x", "crew_y", "slug") {
		t.Fatalf("first call should fire")
	}
	clock.advance(10 * time.Second)
	if tr.OnRunCompleted(context.Background(), "ws_x", "crew_y", "slug") {
		t.Errorf("second call inside debounce window should NOT fire")
	}
}

// TestPostRunTrigger_FiresAfterDebounceWindow asserts that once
// debounce elapses the trigger fires again.
func TestPostRunTrigger_FiresAfterDebounceWindow(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	seedEntries(t, db, w, "ws_x", "crew_y", 12, journal.EntryPeerEscalation)

	var summCalls atomic.Int32
	summ := &countingSummarizer{counter: &summCalls, reply: `[]`}
	c := &Consolidator{DB: db, Journal: w, Summarizer: summ, Logger: quietLogger()}
	clock := &fakeClock{now: time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)}

	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:       time.Minute,
		CrewMemoryRoot: t.TempDir(),
		Since:          time.Hour,
		MinEntries:     5,
		Now:            clock.Now,
	})

	if !tr.OnRunCompleted(context.Background(), "ws_x", "crew_y", "slug") {
		t.Fatalf("first call should fire")
	}
	clock.advance(2 * time.Minute) // beyond debounce
	if !tr.OnRunCompleted(context.Background(), "ws_x", "crew_y", "slug") {
		t.Errorf("call after debounce window should fire")
	}
}

// TestPostRunTrigger_PerCrewIsolation asserts the debounce key is
// (workspace, crew) so two crews under one workspace fire
// independently.
func TestPostRunTrigger_PerCrewIsolation(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)
	// Only seed once; this test exercises debounce-key isolation,
	// not actual consolidation output. The goroutine for crew_b
	// will see no entries for that crew and skip — harmless.
	seedEntries(t, db, w, "ws_x", "crew_a", 12, journal.EntryPeerEscalation)

	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: `[]`}, Logger: quietLogger()}
	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:       time.Minute,
		CrewMemoryRoot: t.TempDir(),
		Since:          time.Hour,
		MinEntries:     5,
	})

	if !tr.OnRunCompleted(context.Background(), "ws_x", "crew_a", "a") {
		t.Errorf("crew_a first call should fire")
	}
	if !tr.OnRunCompleted(context.Background(), "ws_x", "crew_b", "b") {
		t.Errorf("crew_b first call should fire (different debounce bucket)")
	}
	// And the same crew is still debounced.
	if tr.OnRunCompleted(context.Background(), "ws_x", "crew_a", "a") {
		t.Errorf("crew_a second call inside window should NOT fire")
	}
}

// TestPostRunTrigger_NilConsolidator_NoFire guards against panics
// when wiring code constructs the trigger before the consolidator
// is ready. Returns false silently, no goroutine.
func TestPostRunTrigger_NilConsolidator_NoFire(t *testing.T) {
	tr := NewPostRunTrigger(nil, PostRunTriggerOptions{})
	if tr.OnRunCompleted(context.Background(), "ws", "crew", "slug") {
		t.Errorf("nil consolidator should return false")
	}
}

func TestPostRunTrigger_MissingIDs_NoFire(t *testing.T) {
	c := &Consolidator{}
	tr := NewPostRunTrigger(c, PostRunTriggerOptions{})
	if tr.OnRunCompleted(context.Background(), "", "crew", "slug") {
		t.Errorf("empty workspace should return false")
	}
	if tr.OnRunCompleted(context.Background(), "ws", "", "slug") {
		t.Errorf("empty crew should return false")
	}
}

// fakeClock is a deterministic time source for the debounce tests.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// countingSummarizer counts Summarize calls so the test can confirm
// the consolidator actually ran (vs. just claiming to fire).
type countingSummarizer struct {
	counter *atomic.Int32
	reply   string
}

func (s *countingSummarizer) Summarize(_ context.Context, _ string) (string, error) {
	s.counter.Add(1)
	return s.reply, nil
}
