package fileserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The registration race these tests have to survive
// -------------------------------------------------
//
// fsnotify's kqueue backend (macOS, the BSDs) watches a file *descriptor* per
// file, not a path. When a file appears inside a watched directory the backend
// emits Create first and only then opens and registers that file's descriptor
// — sendCreateIfNew calls sendEvent before internalWatch (backend_kqueue.go).
// Its Events channel is unbuffered, so the Create is handed to our reader, and
// to whatever test goroutine that wakes, while the registration is still
// pending on fsnotify's own goroutine.
//
// A write issued inside that window lands on a file kqueue is not yet
// watching. kqueue is edge-triggered and never replays, so the file_modified
// event for that write is not late — it does not exist, and no amount of
// waiting produces it. That is why #1464 failed identically at the original 3s
// bound and at the 60s bound that replaced it: `3.00s` and `60.01s`, the
// deadline firing in both cases, on a test whose work takes milliseconds.
//
// inotify (Linux) has no such window. A directory watch already reports writes
// to the files inside it, so there is no per-file registration to race, which
// is why every linux leg of those runs passed while the macos leg went red.
//
// The remedy is to stop betting the test on a single edge. Where the stimulus
// can be re-applied, driveForEvent re-applies it until the watcher reports it,
// so a lost edge costs one retry interval instead of the whole run. Waits that
// ride a watch registered synchronously inside Watch (the crew directory
// itself, and any subtree addAllDirs walked at startup) are not exposed to the
// race and can still wait plainly — that is what waitForEvent is for.

// eventAbandonAfter is how long a FAILING wait takes to say so.
//
// It is a hang guard, not a latency assertion, and nothing in any pass
// condition depends on its value: a healthy run leaves these helpers in
// milliseconds and never reads this timer. It exists so a genuine regression
// reports its own diagnosis instead of hanging until the package -timeout
// kills every other test's output along with it.
//
// Raising it is not a remedy for a flake. #1464 raised it 20× and failed at
// exactly the new number, because the event it waited for was never coming.
const eventAbandonAfter = 30 * time.Second

// eventRetryInterval is how long driveForEvent gives the watcher to report a
// stimulus before assuming the edge was dropped and re-applying it.
const eventRetryInterval = 50 * time.Millisecond

// waitForEvent waits (bounded) for the next FileEvent matching the given event
// name and relative path.
//
// Only for stimuli that cannot be repeated, or that ride a watch registered
// synchronously by Watch. If the stimulus is repeatable and targets a watch
// that may still be registering, use driveForEvent — see the note above.
func waitForEvent(t *testing.T, ch <-chan FileEvent, event, relPath string) FileEvent {
	t.Helper()
	deadline := time.After(eventAbandonAfter)
	var seen []FileEvent
	for {
		select {
		case fe := <-ch:
			if fe.Event == event && fe.Path == relPath {
				return fe
			}
			// Different event for the same churn (e.g. a Write right after
			// Create) — keep draining.
			seen = append(seen, fe)
		case <-deadline:
			t.Fatalf("timed out after %s waiting for %s %s; %s",
				eventAbandonAfter, event, relPath, describeEvents(seen))
		}
	}
}

// driveForEvent re-applies stimulus until the watcher reports an event
// satisfying match, and returns that event.
//
// stimulus must be repeatable and must re-produce the same observable each
// time it runs, because it will run again for every retry interval that goes
// by without a match. That is the whole point: an edge dropped by a watch that
// had not finished registering (see the note above) is retried rather than
// waited out, so the pass condition no longer contains a bet on when the
// registration lands.
//
// The reported events are drained continuously while waiting, so the caller's
// channel cannot fill up with the retries' own churn.
func driveForEvent(
	t *testing.T,
	ch <-chan FileEvent,
	stimulus func() error,
	match func(FileEvent) bool,
	what string,
) FileEvent {
	t.Helper()

	abandon := time.After(eventAbandonAfter)
	var seen []FileEvent
	attempts := 0

	for {
		attempts++
		if err := stimulus(); err != nil {
			t.Fatalf("applying stimulus for %s (attempt %d): %v", what, attempts, err)
		}

		retry := time.After(eventRetryInterval)
		for {
			select {
			case fe := <-ch:
				if match(fe) {
					return fe
				}
				seen = append(seen, fe)
				continue
			case <-retry:
				// Nothing matched in time. The stimulus may have landed on a
				// file the backend had not registered yet, in which case its
				// event will never arrive — re-apply rather than wait.
			case <-abandon:
				t.Fatalf("timed out after %s waiting for %s; re-applied the stimulus %d times; %s",
					eventAbandonAfter, what, attempts, describeEvents(seen))
			}
			break
		}
	}
}

// describeEvents renders what a failing wait actually observed.
//
// #1464 was reopened because the old message could not tell "no events at all"
// (the watch never registered, or the path is wrong) from "events, but never
// the one we wanted" (the projection is wrong) — it reported only the last
// size it had seen. Those are different bugs and the failure line should name
// which one it is looking at.
func describeEvents(seen []FileEvent) string {
	if len(seen) == 0 {
		return "no other events were delivered at all, so the watch never fired for this path"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d other event(s) were delivered:", len(seen))
	for _, fe := range seen {
		fmt.Fprintf(&b, "\n\t%s %s (agent %q, size %d)", fe.Event, fe.Path, fe.Agent, fe.Size)
	}
	return b.String()
}

// TestDriveForEvent_SurvivesADroppedEdge pins the property #1464 needed and a
// single-shot wait does not have: when the event source silently swallows a
// stimulus — precisely what kqueue does to a write issued before it has
// registered the file — the helper must re-apply and still succeed, rather
// than wait out a bound for an event that is never coming.
//
// The real race is macos-only and cannot be reproduced on linux, so the drop
// is modelled directly here. This is the mechanism under test, not the
// platform.
func TestDriveForEvent_SurvivesADroppedEdge(t *testing.T) {
	ch := make(chan FileEvent, 8)
	attempts := 0

	got := driveForEvent(t, ch,
		func() error {
			attempts++
			// The first two stimuli vanish, like a write to a descriptor the
			// backend has not registered yet. Only the third is observed.
			if attempts > 2 {
				ch <- FileEvent{Event: "file_modified", Path: "report.md", Size: 9}
			}
			return nil
		},
		func(fe FileEvent) bool {
			return fe.Event == "file_modified" && fe.Path == "report.md" && fe.Size == 9
		},
		"file_modified report.md",
	)

	if got.Path != "report.md" || got.Size != 9 {
		t.Errorf("matched event = %+v, want file_modified report.md size 9", got)
	}
	if attempts < 3 {
		t.Errorf("stimulus applied %d time(s); the helper has to re-apply a dropped one", attempts)
	}
}

// TestDriveForEvent_IgnoresNonMatchingChurn pins the drain: unrelated events
// on the channel must not satisfy the wait, and must not stall it either.
func TestDriveForEvent_IgnoresNonMatchingChurn(t *testing.T) {
	ch := make(chan FileEvent, 8)
	ch <- FileEvent{Event: "file_created", Path: "report.md", Size: 2}
	ch <- FileEvent{Event: "file_modified", Path: "other.md", Size: 9}

	got := driveForEvent(t, ch,
		func() error {
			ch <- FileEvent{Event: "file_modified", Path: "report.md", Size: 9}
			return nil
		},
		func(fe FileEvent) bool {
			return fe.Event == "file_modified" && fe.Path == "report.md" && fe.Size == 9
		},
		"file_modified report.md",
	)
	if got.Size != 9 {
		t.Errorf("matched event = %+v, want size 9", got)
	}
}

// TestWatch_EmitsLifecycleEvents drives the full watcher loop end-to-end:
// create → modify → delete on a real temp dir, asserting the handler gets
// the projected FileEvents with crew-relative paths and agent slugs.
func TestWatch_EmitsLifecycleEvents(t *testing.T) {
	base := t.TempDir()
	events := make(chan FileEvent, 64)
	w := NewWatcher(base, discardLogger(), func(crewID string, fe FileEvent) {
		if crewID != "crew-1" {
			t.Errorf("handler crewID = %q, want crew-1", crewID)
		}
		events <- fe
	})
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-1"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	crewDir := filepath.Join(base, "crew-1")
	target := filepath.Join(crewDir, "report.md")

	if err := os.WriteFile(target, []byte("v1"), 0o640); err != nil {
		t.Fatalf("create: %v", err)
	}
	created := waitForEvent(t, events, "file_created", "report.md")
	if created.Agent != "report.md" {
		t.Errorf("root-level agent slug = %q, want report.md (first path segment)", created.Agent)
	}
	if created.Timestamp.IsZero() {
		t.Error("event timestamp not set")
	}

	// This is the wait that flaked in #1464, and the reason it is driven
	// rather than merely awaited: report.md was created a moment ago, so on
	// kqueue the write below can land before its descriptor is registered and
	// produce no event at all (see the note at the top of this file). Each
	// rewrite is another chance once the registration completes.
	//
	// The content is fixed, so every attempt performs the identical
	// modification and the size stays an exact assertion rather than a
	// "whatever we ended up with" one.
	//
	// Draining non-matching events matters on Linux too: the initial create
	// emits CREATE+MODIFY, both at the v1 size, so a stale file_modified for
	// v1 can arrive before the v2 write's event.
	const v2 = "v2-longer"
	wantSize := int64(len(v2))
	modified := driveForEvent(t, events,
		func() error { return os.WriteFile(target, []byte(v2), 0o640) },
		func(fe FileEvent) bool {
			return fe.Event == "file_modified" && fe.Path == "report.md" && fe.Size == wantSize
		},
		fmt.Sprintf("file_modified report.md at size %d", wantSize),
	)
	if modified.Agent != "report.md" {
		t.Errorf("modified agent slug = %q, want report.md (first path segment)", modified.Agent)
	}
	if modified.Timestamp.IsZero() {
		t.Error("modified event timestamp not set")
	}

	if err := os.Remove(target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	waitForEvent(t, events, "file_deleted", "report.md")
}

// TestWatch_NewSubdirGetsWatched pins the dynamic re-watch: a directory
// created AFTER Watch starts must itself be watched, so files written
// inside it still produce events — and their Agent field carries the
// first path segment (the agent slug convention).
func TestWatch_NewSubdirGetsWatched(t *testing.T) {
	base := t.TempDir()
	events := make(chan FileEvent, 64)
	w := NewWatcher(base, discardLogger(), func(_ string, fe FileEvent) { events <- fe })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-2"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	agentDir := filepath.Join(base, "crew-2", "claude-dev")
	if err := os.Mkdir(agentDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	waitForEvent(t, events, "file_created", "claude-dev")

	// A fixed sleep used to sit here — "give fsnotify a beat to register the
	// new directory watch" — which is the same bet #1464 lost, just with a
	// smaller number. The watch on a directory created after Watch starts is
	// registered *after* its Create is delivered (by our own loop on Linux, by
	// fsnotify's on kqueue), so a file written before that lands unwatched and
	// emits nothing; 50ms is a guess at how long that takes on the busiest
	// runner we will ever have.
	//
	// Removing and re-creating the file instead makes every attempt a genuine
	// create, so file_created and the exact path both stay exact assertions,
	// and the test stops caring when the registration lands.
	out := filepath.Join(agentDir, "out.txt")
	fe := driveForEvent(t, events,
		func() error {
			if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.WriteFile(out, []byte("hi"), 0o640)
		},
		func(fe FileEvent) bool {
			return fe.Event == "file_created" && fe.Path == filepath.Join("claude-dev", "out.txt")
		},
		"file_created claude-dev/out.txt",
	)
	if fe.Agent != "claude-dev" {
		t.Errorf("agent slug = %q, want claude-dev", fe.Agent)
	}
}

// TestWatch_PreexistingSubdirWatched pins addAllDirs: directories that
// already exist when Watch starts are walked and watched recursively.
func TestWatch_PreexistingSubdirWatched(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "crew-3", "agent-a", "deep")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	events := make(chan FileEvent, 64)
	w := NewWatcher(base, discardLogger(), func(_ string, fe FileEvent) { events <- fe })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-3"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(nested, "n.txt"), []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	fe := waitForEvent(t, events, "file_created", filepath.Join("agent-a", "deep", "n.txt"))
	if fe.Agent != "agent-a" {
		t.Errorf("agent slug = %q, want agent-a", fe.Agent)
	}
}

// TestWatch_MkdirAllError pins the create-output-dir failure: basePath
// occupied by a regular file makes MkdirAll fail.
func TestWatch_MkdirAllError(t *testing.T) {
	root := t.TempDir()
	fileAsBase := filepath.Join(root, "base")
	if err := os.WriteFile(fileAsBase, []byte("x"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	w := NewWatcher(fileAsBase, discardLogger(), nil)
	err := w.Watch(context.Background(), "crew-1")
	if err == nil {
		t.Fatal("expected create output dir error")
	}
}

// TestToFileEvent_Projection unit-tests the fsnotify → FileEvent mapping,
// including the dropped ops and the Rel failure guard.
func TestToFileEvent_Projection(t *testing.T) {
	base := t.TempDir()
	w := NewWatcher(base, discardLogger(), nil)

	existing := filepath.Join(base, "agent-x", "f.txt")
	if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(existing, []byte("12345"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("create includes size and agent", func(t *testing.T) {
		fe := w.toFileEvent(fsnotify.Event{Name: existing, Op: fsnotify.Create}, base)
		if fe == nil {
			t.Fatal("nil event")
		}
		if fe.Event != "file_created" || fe.Path != filepath.Join("agent-x", "f.txt") {
			t.Errorf("event = %+v", fe)
		}
		if fe.Size != 5 {
			t.Errorf("size = %d, want 5", fe.Size)
		}
		if fe.Agent != "agent-x" {
			t.Errorf("agent = %q", fe.Agent)
		}
	})

	t.Run("write maps to modified", func(t *testing.T) {
		fe := w.toFileEvent(fsnotify.Event{Name: existing, Op: fsnotify.Write}, base)
		if fe == nil || fe.Event != "file_modified" {
			t.Errorf("event = %+v", fe)
		}
	})

	t.Run("remove maps to deleted with zero size", func(t *testing.T) {
		gone := filepath.Join(base, "agent-x", "gone.txt")
		fe := w.toFileEvent(fsnotify.Event{Name: gone, Op: fsnotify.Remove}, base)
		if fe == nil || fe.Event != "file_deleted" {
			t.Fatalf("event = %+v", fe)
		}
		if fe.Size != 0 {
			t.Errorf("size for missing file = %d, want 0", fe.Size)
		}
	})

	t.Run("rename maps to deleted", func(t *testing.T) {
		fe := w.toFileEvent(fsnotify.Event{Name: existing, Op: fsnotify.Rename}, base)
		if fe == nil || fe.Event != "file_deleted" {
			t.Errorf("event = %+v", fe)
		}
	})

	t.Run("chmod dropped", func(t *testing.T) {
		if fe := w.toFileEvent(fsnotify.Event{Name: existing, Op: fsnotify.Chmod}, base); fe != nil {
			t.Errorf("chmod should be dropped, got %+v", fe)
		}
	})

	t.Run("rel failure dropped", func(t *testing.T) {
		// Relative event name against an absolute base → filepath.Rel error.
		if fe := w.toFileEvent(fsnotify.Event{Name: "relative.txt", Op: fsnotify.Create}, base); fe != nil {
			t.Errorf("Rel error should drop event, got %+v", fe)
		}
	})
}

// TestWatcher_CloseIsSafe pins the Close contract — callable on a watcher
// that never watched, idempotent, and terminal. Close is a wait, not a
// signal: the caller cancels the Watch context, Close only drains.
func TestWatcher_CloseIsSafe(t *testing.T) {
	base := t.TempDir()

	// Close with nothing to wait for is fine, and closes the watcher for good.
	fresh := NewWatcher(base, discardLogger(), nil)
	fresh.Close()
	if err := fresh.Watch(context.Background(), "crew-z"); !errors.Is(err, ErrWatcherClosed) {
		t.Fatalf("watch after close = %v, want ErrWatcherClosed", err)
	}

	events := make(chan FileEvent, 8)
	w := NewWatcher(base, discardLogger(), func(_ string, fe FileEvent) { events <- fe })

	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Watch(ctx, "crew-z"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	// The loop is live until the context is cancelled, Close notwithstanding.
	if err := os.WriteFile(filepath.Join(base, "crew-z", "alive.txt"), []byte("y"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForEvent(t, events, "file_created", "alive.txt")

	cancel()
	w.Close()
	w.Close() // idempotent once drained
}

// TestWatch_UnwatchableSubdirErrors pins the addAllDirs failure path in
// Watch: a pre-existing subdirectory the watcher cannot open makes Watch
// return a wrapped "watch output dir" error instead of silently watching
// a partial tree.
func TestWatch_UnwatchableSubdirErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits ignored")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "crew-locked", "agent-x")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })

	w := NewWatcher(base, discardLogger(), nil)
	err := w.Watch(context.Background(), "crew-locked")
	if err == nil {
		t.Fatal("expected error for unwatchable subdir")
	}
	if !strings.Contains(err.Error(), "watch output dir") {
		t.Errorf("error should be wrapped as watch output dir, got %v", err)
	}
}
