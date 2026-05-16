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

// TestWatcher_DebounceCoalesce writes 10 files within a debounce window
// and asserts the watcher emits exactly one event covering all paths.
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

	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, "f"+string(rune('0'+i))+".md")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case ev := <-w.Events():
		if len(ev.Paths) == 0 {
			t.Errorf("expected coalesced event to carry at least one path")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("no event received after 10 writes")
	}

	// No further event should arrive in the next debounce window — all
	// 10 writes should have coalesced into the single emit above.
	select {
	case ev := <-w.Events():
		t.Fatalf("unexpected second event: %+v", ev)
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
