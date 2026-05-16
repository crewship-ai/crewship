package memory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchConfig parameterises the memory directory watcher. The
// debounce window coalesces bursts of writes from the consolidator
// or the agent's editor into a single reindex pass; the poll
// interval is the cross-host fallback for environments where
// fsnotify is unreliable (Docker Desktop bind-mounts on macOS /
// Windows, WSL2 9p mounts).
//
// Killswitch: callers honouring CREWSHIP_MEMORY_WATCHER=0 should set
// UseFsnotify=false AND PollInterval=0 to disable the watcher
// entirely.
type WatchConfig struct {
	// Debounce is the no-activity window after which the accumulated
	// set of changed paths is emitted as one event. Defaults to 1.5s.
	Debounce time.Duration
	// PollInterval is the mtime-poll cadence. 0 disables polling
	// (rely on fsnotify only). Defaults to 30s.
	PollInterval time.Duration
	// UseFsnotify toggles the kernel-level watcher. Defaults to true.
	// Set false on Docker Desktop bind-mounts or WSL where events
	// may not arrive.
	UseFsnotify bool
	// Logger receives warn/error lines. nil → slog.Default.
	Logger *slog.Logger
}

// WatchEvent carries the deduped set of paths that changed in a
// debounce window. Reindexers can treat it as "something under root
// moved; rebuild your view" without per-path branching.
type WatchEvent struct {
	Paths []string
	TS    time.Time
}

// Watcher is the running watcher state. Construct via StartWatcher.
type Watcher struct {
	cfg       WatchConfig
	root      string
	events    chan WatchEvent
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once

	// debounceMu guards pendingPaths + debounceTimer. The dispatcher
	// goroutine batches incoming raw events into pendingPaths until
	// the debounce timer fires.
	debounceMu     sync.Mutex
	pendingPaths   map[string]struct{}
	debounceTimer  *time.Timer
	debounceActive bool
}

// StartWatcher launches a memory-directory watcher rooted at `root`.
// Returns immediately; events arrive on Events(). Stop() must be
// called to release resources.
//
// Returns an error if `root` does not exist, or if fsnotify could not
// be initialised when UseFsnotify is true. The poll-only mode does
// not require root to exist at construction — but the symmetry with
// fsnotify (which does) keeps callers honest about provisioning the
// directory upfront.
func StartWatcher(ctx context.Context, root string, cfg WatchConfig) (*Watcher, error) {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 1500 * time.Millisecond
	}
	if cfg.PollInterval < 0 {
		cfg.PollInterval = 0
	} else if cfg.PollInterval == 0 && !cfg.UseFsnotify {
		// At least one detection mechanism must be active.
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat watch root: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("watch root %q is not a directory", root)
	}

	w := &Watcher{
		cfg:          cfg,
		root:         root,
		events:       make(chan WatchEvent, 4),
		stop:         make(chan struct{}),
		pendingPaths: make(map[string]struct{}),
	}

	if cfg.UseFsnotify {
		fw, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, fmt.Errorf("fsnotify.NewWatcher: %w", err)
		}
		if err := addRecursiveWatch(fw, root); err != nil {
			_ = fw.Close()
			return nil, fmt.Errorf("add recursive watch: %w", err)
		}
		w.wg.Add(1)
		go w.runFsnotify(fw)
	}

	if cfg.PollInterval > 0 {
		w.wg.Add(1)
		go w.runPoll()
	}

	return w, nil
}

// Events returns the channel events are emitted on. The channel is
// closed by Stop.
func (w *Watcher) Events() <-chan WatchEvent {
	return w.events
}

// Stop drains the goroutines and closes the event channel. Idempotent.
func (w *Watcher) Stop() {
	w.closeOnce.Do(func() {
		close(w.stop)
		w.wg.Wait()
		w.debounceMu.Lock()
		if w.debounceTimer != nil {
			w.debounceTimer.Stop()
		}
		w.debounceMu.Unlock()
		close(w.events)
	})
}

// runFsnotify pulls from the kernel watcher and forwards .md events
// into the debouncer. Errors from the watcher are logged but not
// propagated — the watcher is best-effort; the poll fallback covers
// missed events.
func (w *Watcher) runFsnotify(fw *fsnotify.Watcher) {
	defer w.wg.Done()
	defer func() { _ = fw.Close() }()
	for {
		select {
		case <-w.stop:
			return
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			if !isMarkdownEvent(ev) {
				continue
			}
			w.note(ev.Name)
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			w.cfg.Logger.Warn("memory watcher fsnotify error", "error", err, "root", w.root)
		}
	}
}

// runPoll walks the watch root every PollInterval, comparing each
// `.md` file's mtime against the previous pass; any mtime change
// (or new file) is funnelled into the debouncer.
func (w *Watcher) runPoll() {
	defer w.wg.Done()
	snapshot := w.snapshot()
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			next := w.snapshot()
			for path, ts := range next {
				if prev, ok := snapshot[path]; !ok || !prev.Equal(ts) {
					w.note(path)
				}
			}
			snapshot = next
		}
	}
}

// snapshot walks the watch root and records each `.md` file's mtime.
// Failures on individual files are logged at debug level (via the
// configured logger at warn for surface-level errors) and skipped —
// a missing file is "removed", which the next snapshot pass will
// detect by its absence on the next walk. We intentionally don't
// emit OpRemoved today because consolidator/agent code treats reindex
// as the canonical action and missing files fall out of the FTS
// index on rebuild.
func (w *Watcher) snapshot() map[string]time.Time {
	m := make(map[string]time.Time, 16)
	filepath.Walk(w.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".md") {
			return nil
		}
		m[path] = info.ModTime()
		return nil
	})
	return m
}

// note accumulates path into the debounce batch and resets the timer.
// Concurrent callers (fsnotify + poll) coalesce into the same batch
// because debounceMu guards the shared state.
func (w *Watcher) note(path string) {
	w.debounceMu.Lock()
	defer w.debounceMu.Unlock()
	w.pendingPaths[path] = struct{}{}
	if w.debounceTimer == nil {
		w.debounceTimer = time.AfterFunc(w.cfg.Debounce, w.flush)
		w.debounceActive = true
		return
	}
	if !w.debounceActive {
		w.debounceTimer.Reset(w.cfg.Debounce)
		w.debounceActive = true
	} else {
		// Reset the timer; AfterFunc.Reset returns whether the timer
		// had been stopped or expired — we don't care, the goal is
		// "extend the window by Debounce".
		w.debounceTimer.Reset(w.cfg.Debounce)
	}
}

// flush is called when the debounce window expires with no new
// activity. It captures the accumulated paths and emits one
// WatchEvent. If Stop fires concurrently the send is dropped via
// the select on w.stop so we never block on a closed channel.
func (w *Watcher) flush() {
	w.debounceMu.Lock()
	paths := make([]string, 0, len(w.pendingPaths))
	for p := range w.pendingPaths {
		paths = append(paths, p)
	}
	w.pendingPaths = make(map[string]struct{})
	w.debounceActive = false
	w.debounceMu.Unlock()

	if len(paths) == 0 {
		return
	}
	ev := WatchEvent{Paths: paths, TS: time.Now()}
	select {
	case w.events <- ev:
	case <-w.stop:
	}
}

// isMarkdownEvent returns true if the event names a .md path AND the
// op is one we want to react to (Create/Write/Rename). Chmod events
// alone are noise. Remove events are skipped on the fsnotify path —
// the next reindex pass will drop the chunks from the FTS index
// when the file is absent.
func isMarkdownEvent(ev fsnotify.Event) bool {
	if !strings.HasSuffix(ev.Name, ".md") {
		return false
	}
	if ev.Op&fsnotify.Chmod != 0 && ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
		return false
	}
	return true
}

// addRecursiveWatch registers `root` and every existing subdirectory
// with the fsnotify watcher. fsnotify does not recurse by default —
// missing this would mean writes into `.memory/daily/` go undetected.
// Failures on individual subdirs are not fatal; logged via the
// caller's error returned at the surface.
func addRecursiveWatch(fw *fsnotify.Watcher, root string) error {
	var firstErr error
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		if addErr := fw.Add(path); addErr != nil && firstErr == nil {
			firstErr = addErr
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return firstErr
}
