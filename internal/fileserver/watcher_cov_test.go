package fileserver

import (
	"context"
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

// collectEvent waits (bounded) for the next FileEvent matching the given
// event name and relative path.
func waitForEvent(t *testing.T, ch <-chan FileEvent, event, relPath string) FileEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case fe := <-ch:
			if fe.Event == event && fe.Path == relPath {
				return fe
			}
			// Different event for the same churn (e.g. a Write right after
			// Create) — keep draining.
		case <-deadline:
			t.Fatalf("timed out waiting for %s %s", event, relPath)
		}
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

	if err := os.WriteFile(target, []byte("v2-longer"), 0o640); err != nil {
		t.Fatalf("modify: %v", err)
	}
	// On Linux, the initial create emits CREATE+MODIFY (both at the v1 size),
	// so a stale file_modified for v1 can arrive before the v2 write's event.
	// Drain file_modified events until one reports the new size.
	wantSize := int64(len("v2-longer"))
	var modified FileEvent
	mdeadline := time.After(3 * time.Second)
	for modified.Size != wantSize {
		select {
		case fe := <-events:
			if fe.Event == "file_modified" && fe.Path == "report.md" {
				modified = fe
			}
		case <-mdeadline:
			t.Fatalf("timed out waiting for file_modified report.md size %d, last size %d", wantSize, modified.Size)
		}
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

	// Give fsnotify a beat to register the new directory watch.
	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(agentDir, "out.txt"), []byte("hi"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	fe := waitForEvent(t, events, "file_created", filepath.Join("claude-dev", "out.txt"))
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

// TestWatcher_CloseIsSafe pins the documented no-op Close contract —
// callable before, during, and after a Watch without panicking or
// killing the active watch.
func TestWatcher_CloseIsSafe(t *testing.T) {
	base := t.TempDir()
	events := make(chan FileEvent, 8)
	w := NewWatcher(base, discardLogger(), func(_ string, fe FileEvent) { events <- fe })
	w.Close() // before Watch

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-z"); err != nil {
		t.Fatalf("watch: %v", err)
	}
	w.Close() // during Watch — must not stop the loop

	if err := os.WriteFile(filepath.Join(base, "crew-z", "alive.txt"), []byte("y"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForEvent(t, events, "file_created", "alive.txt")
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
