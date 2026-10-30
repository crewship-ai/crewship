package memory

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// lockedBuffer is a goroutine-safe log sink; watcher goroutines log
// concurrently with the test reading the buffer.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var errInjectedWatchLimit = errors.New("injected: no space left on device (inotify watch limit)")

// failSubdirAdds fails every Add except the root itself, which is the
// shape of an exhausted fs.inotify.max_user_watches budget on a host
// with many crews: the first registrations succeed, later ones fail.
func failSubdirAdds(root string) fsnotifyHooks {
	return fsnotifyHooks{add: func(fw *fsnotify.Watcher, path string) error {
		if path == root {
			return fw.Add(path)
		}
		return errInjectedWatchLimit
	}}
}

func failNewWatcher() fsnotifyHooks {
	return fsnotifyHooks{newWatcher: func() (*fsnotify.Watcher, error) {
		return nil, errors.New("injected: too many open files (inotify instance limit)")
	}}
}

// touchUntilEvent rewrites path on a slow cadence until the watcher
// emits an event naming it. Re-touching covers the race with the
// poll's initial snapshot (a file present in the first snapshot is
// only reported on a later mtime change).
func touchUntilEvent(t *testing.T, w *Watcher, path string) WatchEvent {
	t.Helper()
	deadline := time.After(8 * time.Second)
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	n := 0
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				t.Fatal("events channel closed before an event for the written file")
			}
			for _, p := range ev.Paths {
				if p == path {
					return ev
				}
			}
		case <-tick.C:
			n++
			ts := time.Now().Add(time.Duration(n) * time.Second)
			if err := os.WriteFile(path, []byte("v"+strings.Repeat("x", n)), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(path, ts, ts)
		case <-deadline:
			t.Fatalf("no watch event for %s within 8s", path)
		}
	}
}

func TestStartWatcher_SubdirAddFailure_KeepsWatcherAndPoll(t *testing.T) {
	root := t.TempDir()
	daily := filepath.Join(root, "daily")
	if err := os.MkdirAll(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	w, err := StartWatcher(context.Background(), root, WatchConfig{
		UseFsnotify:  true,
		Debounce:     20 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		hooks:        failSubdirAdds(root),
	})
	if err != nil {
		t.Fatalf("StartWatcher must tolerate a per-subdirectory Add failure, got: %v", err)
	}
	defer w.Stop()

	// The file lives in the subdirectory fsnotify could not watch, so
	// only the poll fallback can see it.
	target := filepath.Join(daily, "2026-10-30.md")
	touchUntilEvent(t, w, target)

	if !strings.Contains(logs.String(), daily) {
		t.Errorf("per-subdirectory Add failure must be logged with its path, log = %q", logs.String())
	}
}

func TestStartWatcher_FsnotifyInitFailure_FallsBackToPoll(t *testing.T) {
	root := t.TempDir()
	var logs lockedBuffer
	w, err := StartWatcher(context.Background(), root, WatchConfig{
		UseFsnotify:  true,
		Debounce:     20 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		hooks:        failNewWatcher(),
	})
	if err != nil {
		t.Fatalf("StartWatcher must fall back to polling when fsnotify cannot start, got: %v", err)
	}
	defer w.Stop()

	touchUntilEvent(t, w, filepath.Join(root, "AGENT.md"))

	if !strings.Contains(logs.String(), "poll") {
		t.Errorf("fallback to poll-only must be logged, log = %q", logs.String())
	}
}

func TestStartWatcher_RootAddFailure_FallsBackToPoll(t *testing.T) {
	root := t.TempDir()
	w, err := StartWatcher(context.Background(), root, WatchConfig{
		UseFsnotify:  true,
		Debounce:     20 * time.Millisecond,
		PollInterval: 50 * time.Millisecond,
		Logger:       quietLogger(),
		hooks: fsnotifyHooks{add: func(*fsnotify.Watcher, string) error {
			return errInjectedWatchLimit
		}},
	})
	if err != nil {
		t.Fatalf("StartWatcher must fall back to polling when even the root cannot be watched, got: %v", err)
	}
	defer w.Stop()

	touchUntilEvent(t, w, filepath.Join(root, "AGENT.md"))
}

func TestStartWatcher_FsnotifyOnlyInitFailure_StillErrors(t *testing.T) {
	// With polling explicitly disabled there is no fallback; the caller
	// must learn that nothing is watching.
	_, err := StartWatcher(context.Background(), t.TempDir(), WatchConfig{
		UseFsnotify: true,
		Logger:      quietLogger(),
		hooks:       failNewWatcher(),
	})
	if err == nil {
		t.Fatal("fsnotify-only watcher with a failing fsnotify must return an error")
	}
}

func TestAuditWatcher_ZeroPollFallback_DefaultsTo30s(t *testing.T) {
	// The documented contract of AuditWatcherConfig.PollFallbackInterval
	// is "Zero -> 30 s default". Production wiring leaves it zero, so
	// without the default the poll safety net never runs.
	wc := auditWatchConfig(AuditWatcherConfig{}, quietLogger())
	if wc.PollInterval != 30*time.Second {
		t.Fatalf("PollInterval = %v, want 30s default", wc.PollInterval)
	}
	if !wc.UseFsnotify {
		t.Fatal("audit watcher must keep fsnotify as the fast path")
	}
	wc = auditWatchConfig(AuditWatcherConfig{PollFallbackInterval: 50 * time.Millisecond}, quietLogger())
	if wc.PollInterval != 50*time.Millisecond {
		t.Fatalf("explicit PollFallbackInterval not forwarded: %v", wc.PollInterval)
	}
}

func TestAuditWatcher_FsnotifyFailure_PollStillRecordsVersion(t *testing.T) {
	base, db, j, scr := auditTestRig(t)
	if err := os.MkdirAll(filepath.Join(base, "crews"), 0o755); err != nil {
		t.Fatal(err)
	}
	memDir := filepath.Join(base, "crews", "crew_audit", "agents", "martin", ".memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var logs lockedBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartAuditWatcher(ctx, db, j, AuditWatcherConfig{
		BasePath:             base,
		BlobRoot:             filepath.Join(base, "versions"),
		Scrubber:             scr,
		DebounceInterval:     20 * time.Millisecond,
		PollFallbackInterval: 50 * time.Millisecond,
		watchHooks:           failNewWatcher(),
	}, logger)

	full := filepath.Join(memDir, "AGENT.md")
	if err := os.WriteFile(full, []byte("direct write while fsnotify is down\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	lastTouch := time.Now()
	var rows int
	for time.Now().Before(deadline) {
		_ = db.QueryRow(`SELECT COUNT(*) FROM memory_versions WHERE path = 'agent:martin/AGENT.md'`).Scan(&rows)
		if rows > 0 {
			break
		}
		if time.Since(lastTouch) > 300*time.Millisecond {
			now := time.Now()
			_ = os.Chtimes(full, now, now)
			lastTouch = now
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rows == 0 {
		t.Fatalf("no memory_versions row: fsnotify failure stopped the poll fallback; log = %q", logs.String())
	}
	if strings.Contains(logs.String(), "audit disabled this boot") {
		t.Errorf("audit must not be disabled when only fsnotify failed; log = %q", logs.String())
	}
}

// A partial watch is safe only while a poll fallback actually runs. In the
// fsnotify-only mode it must fail closed and release the kernel watcher.
func TestStartWatcher_AddFailureModesAndCleanup(t *testing.T) {
	for _, failRoot := range []bool{false, true} {
		for _, polling := range []bool{false, true} {
			name := "subdir"
			if failRoot {
				name = "root"
			}
			if polling {
				name += "/poll"
			} else {
				name += "/fsnotify-only"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				daily := filepath.Join(root, "daily")
				if err := os.Mkdir(daily, 0o755); err != nil {
					t.Fatal(err)
				}
				var fw *fsnotify.Watcher
				var logs lockedBuffer
				cfg := WatchConfig{
					UseFsnotify: true,
					Debounce:    20 * time.Millisecond,
					Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
					hooks: fsnotifyHooks{
						newWatcher: func() (*fsnotify.Watcher, error) {
							var err error
							fw, err = fsnotify.NewWatcher()
							return fw, err
						},
						add: func(w *fsnotify.Watcher, path string) error {
							if failRoot || path == daily {
								return errInjectedWatchLimit
							}
							return w.Add(path)
						},
					},
				}
				if polling {
					cfg.PollInterval = 50 * time.Millisecond
				}
				w, err := StartWatcher(context.Background(), root, cfg)
				if w != nil {
					t.Cleanup(w.Stop)
				}
				if !polling {
					if !errors.Is(err, errInjectedWatchLimit) || w != nil {
						t.Fatalf("fsnotify-only must reject incomplete coverage: watcher=%v, err=%v", w, err)
					}
					if strings.Contains(logs.String(), "poll fallback") {
						t.Fatalf("claimed polling when disabled: %s", logs.String())
					}
				} else {
					if err != nil || w == nil {
						t.Fatalf("poll fallback must survive Add failure: %v", err)
					}
					touchUntilEvent(t, w, filepath.Join(daily, "AGENT.md"))
					if !strings.Contains(logs.String(), "poll fallback") {
						t.Fatalf("missing fallback warning: %s", logs.String())
					}
					w.Stop()
				}
				if fw == nil {
					t.Fatal("fsnotify creation unexpectedly failed")
				}
				if err := fw.Add(root); !errors.Is(err, fsnotify.ErrClosed) {
					t.Fatalf("kernel watcher not closed: Add returned %v", err)
				}
			})
		}
	}
}
