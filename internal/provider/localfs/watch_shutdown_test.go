package localfs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestWaitWatchers_BlocksUntilWatcherReleasesFDs pins the shutdown contract
// behind the #1286 flake: WaitWatchers must not return while a watcher is
// still live, and must return once its context is cancelled.
//
// Without that ordering the fsnotify watcher tears its file descriptors down
// on its own goroutine, concurrently with whoever is deleting the watched
// tree — see the comment on WaitWatchers for why that is destructive.
func TestWaitWatchers_BlocksUntilWatcherReleasesFDs(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); p.WaitWatchers() })

	if err := p.EnsureDir(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	events := make(chan provider.FileEvent, 8)
	if err := p.Watch(ctx, "w", events); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		p.WaitWatchers()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitWatchers returned while the watcher was still running")
	case <-time.After(250 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitWatchers did not return after the watcher context was cancelled")
	}
}

// TestWaitWatchers_TreeRemovableAfterWait exercises the teardown shape that
// actually failed in CI: cancel, wait, then delete the watched tree. The
// removal must not race the watcher's descriptor cleanup.
func TestWaitWatchers_TreeRemovableAfterWait(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	p, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); p.WaitWatchers() })

	if err := p.EnsureDir(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := p.Write(ctx, filepath.Join("w", "sub"+strconv.Itoa(i), "f.txt"), bytes.NewReader([]byte("x"))); err != nil {
			t.Fatal(err)
		}
	}
	events := make(chan provider.FileEvent, 256)
	go func() {
		for range events {
		}
	}()
	if err := p.Watch(ctx, "w", events); err != nil {
		t.Fatal(err)
	}

	cancel()
	p.WaitWatchers()

	if err := os.RemoveAll(filepath.Join(base, "w")); err != nil {
		t.Fatalf("remove watched tree after WaitWatchers: %v", err)
	}
}
