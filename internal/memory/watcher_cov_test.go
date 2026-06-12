package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// waitForWatchEvent pulls one event off the channel with a deadline so
// a regression hangs the test for seconds, not forever.
func waitForWatchEvent(t *testing.T, ch <-chan WatchEvent) WatchEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("events channel closed before expected event")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return WatchEvent{}
	}
}

func TestStartWatcher_DefaultsApplied(t *testing.T) {
	root := t.TempDir()
	// Zero Debounce + zero PollInterval + poll-only mode + nil Logger +
	// nil ctx — the "at least one detection mechanism" rule must force
	// the 30s poll fallback.
	w, err := StartWatcher(nil, root, WatchConfig{Debounce: 0, PollInterval: 0, UseFsnotify: false, Logger: nil})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer w.Stop()
	if w.cfg.Debounce != 1500*time.Millisecond {
		t.Errorf("Debounce default = %v, want 1.5s", w.cfg.Debounce)
	}
	if w.cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s fallback when fsnotify is off", w.cfg.PollInterval)
	}
	if w.cfg.Logger == nil {
		t.Errorf("Logger default not applied")
	}
	if w.ctx == nil {
		t.Errorf("nil ctx must be replaced with Background")
	}
}

func TestStartWatcher_NegativePollIntervalNormalisedToZero(t *testing.T) {
	root := t.TempDir()
	w, err := StartWatcher(context.Background(), root, WatchConfig{UseFsnotify: true, PollInterval: -5 * time.Second})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer w.Stop()
	if w.cfg.PollInterval != 0 {
		t.Errorf("PollInterval = %v, want 0 (polling disabled, fsnotify active)", w.cfg.PollInterval)
	}
}

func TestStartWatcher_RootIsFile_Errors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "flat")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StartWatcher(context.Background(), file, WatchConfig{UseFsnotify: false, PollInterval: time.Second})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("err = %v, want not-a-directory", err)
	}
}

func TestAddRecursiveWatch_AddFailure_SurfacesFirstError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daily"), 0o755); err != nil {
		t.Fatal(err)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	_ = fw.Close() // every subsequent Add fails deterministically
	if err := addRecursiveWatch(fw, root); err == nil {
		t.Fatal("expected firstErr from Add on a closed fsnotify watcher")
	}
}

func TestStartWatcher_ContextCancel_GoroutinesExitAndStopIsClean(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := StartWatcher(ctx, root, WatchConfig{UseFsnotify: true, Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	cancel()
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after ctx cancel")
	}
	if _, ok := <-w.Events(); ok {
		t.Errorf("events channel must be closed after Stop")
	}
}

func TestWatcher_Fsnotify_TwoSeparateBursts_TwoEvents(t *testing.T) {
	root := t.TempDir()
	w, err := StartWatcher(context.Background(), root, WatchConfig{UseFsnotify: true, Debounce: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("StartWatcher: %v", err)
	}
	defer w.Stop()

	first := filepath.Join(root, "one.md")
	if err := os.WriteFile(first, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev1 := waitForWatchEvent(t, w.Events())
	if len(ev1.Paths) == 0 || !strings.HasSuffix(ev1.Paths[0], "one.md") {
		t.Errorf("first event paths = %v, want one.md", ev1.Paths)
	}

	// Second burst after the first flush — exercises the timer-reset
	// path where debounceActive has been cleared by the prior flush.
	second := filepath.Join(root, "two.md")
	if err := os.WriteFile(second, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev2 := waitForWatchEvent(t, w.Events())
	found := false
	for _, p := range ev2.Paths {
		if strings.HasSuffix(p, "two.md") {
			found = true
		}
	}
	if !found {
		t.Errorf("second event paths = %v, want two.md", ev2.Paths)
	}
	if ev2.TS.IsZero() {
		t.Errorf("event timestamp not set")
	}
}

func TestWatcher_Snapshot_SkipsNonMarkdown(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skip.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Unreadable subdir: the walk error on its readdir pass must be
	// swallowed, not abort the snapshot.
	dark := filepath.Join(root, "dark")
	if err := os.MkdirAll(dark, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dark, 0o755) })
	w := &Watcher{root: root}
	snap := w.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot entries = %d, want 1 (.md only)", len(snap))
	}
	for p := range snap {
		if !strings.HasSuffix(p, "keep.md") {
			t.Errorf("unexpected snapshot path %q", p)
		}
	}
}

func TestIsMarkdownEvent_Table(t *testing.T) {
	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{name: "md write", ev: fsnotify.Event{Name: "/x/a.md", Op: fsnotify.Write}, want: true},
		{name: "md create", ev: fsnotify.Event{Name: "/x/a.md", Op: fsnotify.Create}, want: true},
		{name: "md rename", ev: fsnotify.Event{Name: "/x/a.md", Op: fsnotify.Rename}, want: true},
		{name: "md chmod only is noise", ev: fsnotify.Event{Name: "/x/a.md", Op: fsnotify.Chmod}, want: false},
		{name: "md chmod+write passes", ev: fsnotify.Event{Name: "/x/a.md", Op: fsnotify.Chmod | fsnotify.Write}, want: true},
		{name: "non-md", ev: fsnotify.Event{Name: "/x/a.txt", Op: fsnotify.Write}, want: false},
	}
	for _, tc := range cases {
		if got := isMarkdownEvent(tc.ev); got != tc.want {
			t.Errorf("%s: isMarkdownEvent(%v) = %v, want %v", tc.name, tc.ev, got, tc.want)
		}
	}
}

func TestWatcher_Flush_EmptyBatchIsNoOp(t *testing.T) {
	w := &Watcher{
		events:       make(chan WatchEvent, 1),
		stop:         make(chan struct{}),
		pendingPaths: make(map[string]struct{}),
	}
	w.flush() // nothing pending → nothing emitted
	select {
	case ev := <-w.events:
		t.Fatalf("unexpected event %+v from empty flush", ev)
	default:
	}
}

func TestWatcher_Flush_AfterStopSignal_DropsSend(t *testing.T) {
	w := &Watcher{
		events:       make(chan WatchEvent), // unbuffered, no reader → send never ready
		stop:         make(chan struct{}),
		pendingPaths: map[string]struct{}{"/x/a.md": {}},
	}
	close(w.stop)
	done := make(chan struct{})
	go func() { w.flush(); close(done) }()
	select {
	case <-done: // must fall through the stop branch, not block forever
	case <-time.After(3 * time.Second):
		t.Fatal("flush blocked on a send that Stop should have released")
	}
	if len(w.pendingPaths) != 0 {
		t.Errorf("pendingPaths must be drained even when the send is dropped")
	}
}

func TestAddRecursiveWatch_SkipsFilesWatchesSubdirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "daily"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENT.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unreadable subdir: its readdir failure flows into the walk
	// callback's err branch, which must be non-fatal.
	dark := filepath.Join(root, "dark")
	if err := os.MkdirAll(dark, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dark, 0o755) })
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	if err := addRecursiveWatch(fw, root); err != nil {
		t.Fatalf("addRecursiveWatch: %v", err)
	}
	watched := fw.WatchList()
	var hasRoot, hasDaily bool
	for _, p := range watched {
		if strings.HasSuffix(p, "AGENT.md") {
			t.Errorf("regular file must not be watched: %v", watched)
		}
		if p == root {
			hasRoot = true
		}
		if strings.HasSuffix(p, "daily") {
			hasDaily = true
		}
	}
	if !hasRoot || !hasDaily {
		t.Errorf("watch list = %v, want root + daily both registered", watched)
	}
}
