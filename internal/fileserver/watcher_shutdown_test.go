package fileserver

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestClose_BlocksUntilWatchGoroutinesExit pins the shutdown contract behind
// the #1286 flake: Close must not return while a watcher is still live, so a
// caller that deletes the watched tree afterwards cannot race fsnotify's
// descriptor teardown. See the comment on Close for why that race is
// destructive beyond this package.
func TestClose_BlocksUntilWatchGoroutinesExit(t *testing.T) {
	base := t.TempDir()
	w := NewWatcher(base, discardLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-shutdown"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	done := make(chan struct{})
	go func() {
		w.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Close returned while the watcher was still running")
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the watcher context was cancelled")
	}
}

// TestClose_TreeRemovableAfterClose exercises the teardown shape that failed
// in CI: cancel, Close, then delete the watched tree.
func TestClose_TreeRemovableAfterClose(t *testing.T) {
	base := t.TempDir()
	w := NewWatcher(base, discardLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	crewDir := filepath.Join(base, "crew-teardown")
	for i := 0; i < 20; i++ {
		sub := filepath.Join(crewDir, "sub"+strconv.Itoa(i))
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "f.txt"), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Watch(ctx, "crew-teardown"); err != nil {
		t.Fatalf("watch: %v", err)
	}

	cancel()
	w.Close()

	if err := os.RemoveAll(crewDir); err != nil {
		t.Fatalf("remove watched tree after Close: %v", err)
	}
}
