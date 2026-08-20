package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
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
		Debounce:        time.Minute,
		StorageBasePath: t.TempDir(),
		Since:           time.Hour,
		MinEntries:      5,
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
		Debounce:        time.Minute,
		StorageBasePath: t.TempDir(),
		Since:           time.Hour,
		MinEntries:      5,
		Now:             clock.Now,
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
		Debounce:        time.Minute,
		StorageBasePath: t.TempDir(),
		Since:           time.Hour,
		MinEntries:      5,
		Now:             clock.Now,
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
		Debounce:        time.Minute,
		StorageBasePath: t.TempDir(),
		Since:           time.Hour,
		MinEntries:      5,
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

// TestPostRunTrigger_WritesIntoTheCrewBindSource is the post-run half of
// #1663: the sleep-time trigger derived its OutputDir from the same
// container-absolute root the cron runner did, so an idle-time
// consolidation wrote pins.md at the host filesystem root too.
func TestPostRunTrigger_WritesIntoTheCrewBindSource(t *testing.T) {
	t.Setenv("CREWSHIP_CONSOLIDATE_HITL", "")
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	const (
		crewID   = "crew_y"
		crewSlug = "crew-y-slug"
		needle   = "post-run-pins-canary"
	)
	seedPriorityEntry(t, db, "j_pin_postrun", crewID, journal.PriorityPin, needle)

	basePath := t.TempDir()
	c := &Consolidator{DB: db, Journal: w, Summarizer: &stubSummarizer{Reply: `[]`}, Logger: quietLogger()}
	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:        time.Minute,
		StorageBasePath: basePath,
		Since:           time.Hour,
		MinEntries:      5,
	})

	if !tr.OnRunCompleted(context.Background(), "ws_test", crewID, crewSlug) {
		t.Fatalf("first call should fire")
	}

	wantPins := filepath.Join(
		hostPathForContainerPath(t, basePath, crewID, memory.ContainerCrewTopicsDir(crewSlug)),
		"pins.md",
	)
	// Poll for CONTENT, not existence. The original loop broke out on
	// the first successful os.ReadFile, and os.ReadFile of an empty
	// file returns a non-nil zero-length slice — so it accepted a
	// zero-byte pins.md, stopped polling, and asserted against "".
	// snapshotPins used to leave exactly that state on disk between its
	// O_CREATE and its write (#1807, fixed by the atomic replace in
	// consolidator.go), which is how this test failed Go Race on a
	// loaded runner while the race detector reported nothing.
	//
	// The writer is atomic now, so this can no longer see a half-state
	// from that source. Polling on the needle is kept anyway: it drops
	// the "the file appeared, therefore it is written" assumption
	// entirely, so the test stays honest no matter what the writer does
	// later, and a genuine failure now reports at the deadline instead
	// of on the first read that happens to win the race.
	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(wantPins)
		if err == nil {
			body = b
			if strings.Contains(string(b), needle) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if body == nil {
		t.Fatalf("post-run consolidation never wrote pins.md at %s — the trigger is not resolving the host side of %s",
			wantPins, memory.ContainerCrewTopicsDir(crewSlug))
	}
	if !strings.Contains(string(body), needle) {
		t.Errorf("pins.md missing the pinned entry (%d bytes):\n%s", len(body), body)
	}
}

// TestPostRunTrigger_RefusesUnresolvablePaths: an unsafe slug or an
// unconfigured storage root must not fire, and — the part that is easy
// to get wrong — must not burn the crew's debounce window either, so a
// later well-formed call still gets through.
func TestPostRunTrigger_RefusesUnresolvablePaths(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	c := &Consolidator{DB: db, Summarizer: &stubSummarizer{Reply: `[]`}, Logger: quietLogger()}

	unconfigured := NewPostRunTrigger(c, PostRunTriggerOptions{Debounce: time.Minute})
	if unconfigured.OnRunCompleted(context.Background(), "ws", "crew", "slug") {
		t.Errorf("no storage base path configured → must not fire (writing to a guessed root is #1663)")
	}

	tr := NewPostRunTrigger(c, PostRunTriggerOptions{
		Debounce:        time.Minute,
		StorageBasePath: t.TempDir(),
		Since:           time.Hour,
		MinEntries:      5,
	})
	for _, slug := range []string{"", "..", "a/b", `a\b`, "/etc"} {
		if tr.OnRunCompleted(context.Background(), "ws_test", "crew_test", slug) {
			t.Errorf("slug %q escapes the crew tree and must be refused", slug)
		}
	}
	// The refusals did not stamp the debounce map.
	if !tr.OnRunCompleted(context.Background(), "ws_test", "crew_test", "ok-slug") {
		t.Errorf("a refused call consumed the debounce window — a malformed slug must not suppress the next good run")
	}
}
