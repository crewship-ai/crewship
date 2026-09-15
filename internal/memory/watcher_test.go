package memory

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatcher_DebounceCoalesce writes 10 files in one burst and asserts
// the watcher coalesces them: fewer events than writes, and the union of
// paths across those events is exactly the written set — nothing lost,
// nothing duplicated.
//
// It deliberately does not assert "exactly one event". The debounce
// window is wall-clock, and under CI load the write loop can straddle
// its edge, in which case the watcher correctly emits twice (#2486).
// Asserting on the union keeps the test independent of that margin
// while a real coalescing regression — one event per write, or a path
// dropped or repeated across flushes — still fails.
func TestWatcher_DebounceCoalesce(t *testing.T) {
	dir := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	w, err := StartWatcher(context.Background(), dir, WatchConfig{
		Debounce:     200 * time.Millisecond,
		PollInterval: 5 * time.Second, // long enough not to interfere
		UseFsnotify:  true,
		Logger:       silent,
	})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer w.Stop()

	// No sleep between writes: the burst is what is under test, and the
	// watcher keys its pending set by path, so the Create+Write pair
	// each WriteFile raises for the same file needs no spacing to dedupe.
	const writes = 10
	want := make(map[string]bool, writes)
	for i := 0; i < writes; i++ {
		path := filepath.Join(dir, "f"+string(rune('0'+i))+".md")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		want[path] = true
	}

	// Drain events until every written path has been seen, recording
	// each path's first sighting so a repeat is reported with both events.
	seen := make(map[string]int, writes)
	var events []WatchEvent
	deadline := time.After(5 * time.Second)
	for len(seen) < writes {
		select {
		case ev := <-w.Events():
			if len(ev.Paths) == 0 {
				t.Errorf("event %d carries no paths", len(events))
			}
			for _, p := range ev.Paths {
				if !want[p] {
					t.Errorf("event %d carries unexpected path %s", len(events), p)
					continue
				}
				if first, dup := seen[p]; dup {
					t.Errorf("path %s repeated: first in event %d, again in event %d", p, first, len(events))
					continue
				}
				seen[p] = len(events)
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("saw %d of %d written paths in %d events before deadline", len(seen), writes, len(events))
		}
	}

	// Coalescing proper: a burst of N writes must not surface as N
	// events. By pigeonhole this also means at least one event carried
	// more than one path.
	if len(events) >= writes {
		t.Errorf("got %d events for %d writes; expected the debounce to coalesce them", len(events), writes)
	}

	// Once the union is complete, nothing is pending — any further event
	// could only repeat a path already delivered.
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected event after all %d paths were delivered: %+v", writes, ev)
	case <-time.After(500 * time.Millisecond):
		// good
	}
}

// TestWatcher_PollFallback simulates the Docker Desktop case where
// fsnotify never fires: with UseFsnotify=false the mtime poll still
// has to catch the change.
func TestWatcher_PollFallback(t *testing.T) {
	dir := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	w, err := StartWatcher(context.Background(), dir, WatchConfig{
		Debounce:     50 * time.Millisecond,
		PollInterval: 150 * time.Millisecond,
		UseFsnotify:  false,
		Logger:       silent,
	})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer w.Stop()

	// Wait one poll cycle so the baseline snapshot is in place, then write.
	time.Sleep(200 * time.Millisecond)

	path := filepath.Join(dir, "AGENT.md")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case ev := <-w.Events():
		found := false
		for _, p := range ev.Paths {
			if filepath.Base(p) == "AGENT.md" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected AGENT.md in event paths, got %v", ev.Paths)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("poll fallback did not detect write within 3s")
	}
}

// TestWatcher_Stop_ClosesChannel asserts Stop drains the event channel
// cleanly so consumers iterating with `for ev := range w.Events()`
// terminate.
func TestWatcher_Stop_ClosesChannel(t *testing.T) {
	dir := t.TempDir()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))

	w, err := StartWatcher(context.Background(), dir, WatchConfig{
		Debounce:     50 * time.Millisecond,
		PollInterval: 5 * time.Second,
		UseFsnotify:  true,
		Logger:       silent,
	})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}

	w.Stop()

	deadline := time.NewTimer(1 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatalf("Events channel did not close within 1s of Stop")
		}
	}
}

// TestWatcher_MissingRoot returns an error rather than blocking.
func TestWatcher_MissingRoot(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := StartWatcher(context.Background(), "/definitely/does/not/exist/anywhere", WatchConfig{
		Debounce:     50 * time.Millisecond,
		PollInterval: 5 * time.Second,
		UseFsnotify:  true,
		Logger:       silent,
	})
	if err == nil {
		w.Stop()
		t.Fatalf("expected error from missing root, got nil")
	}
}
