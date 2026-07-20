package fileserver

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

// FileEvent represents a filesystem change in a crew's output directory.
type FileEvent struct {
	Event     string    `json:"event"`
	Path      string    `json:"path"`
	Agent     string    `json:"agent"`
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
}

// EventHandler is called when a filesystem change is detected in a watched directory.
type EventHandler func(crewID string, event FileEvent)

// Watcher monitors crew output directories for file changes and invokes
// the handler callback on each event.
type Watcher struct {
	basePath string
	logger   *slog.Logger
	handler  EventHandler
	watches  sync.WaitGroup
}

// closeTimeout bounds Close so a handler wedged on a full channel cannot hang
// shutdown forever; the descriptors leak until process exit in that case, which
// is strictly better than never returning.
const closeTimeout = 5 * time.Second

// NewWatcher creates a Watcher that monitors directories under basePath.
func NewWatcher(basePath string, logger *slog.Logger, handler EventHandler) *Watcher {
	return &Watcher{
		basePath: basePath,
		logger:   logger,
		handler:  handler,
	}
}

// Close blocks until every goroutine started by Watch has observed its context
// cancellation and released the underlying fsnotify watcher. Cancel the Watch
// contexts first, or Close waits out closeTimeout for nothing.
//
// Waiting is the point: callers tear the watched directories down right after
// shutdown, and on the kqueue backends (macOS, *BSD) fsnotify holds one
// descriptor per watched file and closes them all inside Watcher.Close. If a
// flood of NOTE_DELETE arrives while that is running, fsnotify v1.10.1 can
// close the same descriptor twice (its readEvents→remove path drops the
// watches lock between the map lookup and the unix.Close that Close is doing
// concurrently). The second close lands on whatever descriptor the process has
// since opened for that number — in CI that was the directory fd of an
// unrelated parallel test's os.RemoveAll, which then failed with EBADF. See
// #1286.
func (w *Watcher) Close() {
	done := make(chan struct{})
	go func() {
		w.watches.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeTimeout):
		w.logger.Warn("file watcher shutdown timed out", "timeout", closeTimeout)
	}
}

// Watch starts watching the output directory for a crew, invoking the handler
// on file changes. The watcher runs until ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context, crewID string) error {
	if crewID == "" || filepath.IsAbs(crewID) || strings.Contains(crewID, "..") {
		return fmt.Errorf("invalid crew ID: %q", crewID)
	}

	outputDir := filepath.Join(w.basePath, filepath.Clean(crewID))
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new fsnotify watcher: %w", err)
	}

	if err := addAllDirs(watcher, outputDir); err != nil {
		watcher.Close()
		return fmt.Errorf("watch output dir: %w", err)
	}

	w.watches.Add(1)
	go func() {
		// Done last: Close must not release until the descriptors are actually
		// gone, so watcher.Close has to run first.
		defer w.watches.Done()
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				fe := w.toFileEvent(event, outputDir)
				if fe != nil && w.handler != nil {
					w.handler(crewID, *fe)
				}
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				w.logger.Warn("fsnotify error", "error", err)
			case <-ctx.Done():
				return
			}
		}
	}()

	w.logger.Info("watching output directory", "crew_id", crewID, "path", outputDir)
	return nil
}

func (w *Watcher) toFileEvent(event fsnotify.Event, baseDir string) *FileEvent {
	rel, err := filepath.Rel(baseDir, event.Name)
	if err != nil {
		return nil
	}

	var opStr string
	switch {
	case event.Has(fsnotify.Create):
		opStr = "file_created"
	case event.Has(fsnotify.Write):
		opStr = "file_modified"
	case event.Has(fsnotify.Remove):
		opStr = "file_deleted"
	case event.Has(fsnotify.Rename):
		opStr = "file_deleted"
	default:
		return nil
	}

	var size int64
	if info, err := os.Stat(event.Name); err == nil {
		size = info.Size()
	}

	return &FileEvent{
		Event:     opStr,
		Path:      rel,
		Agent:     extractAgentSlug(rel),
		Size:      size,
		Timestamp: time.Now().UTC(),
	}
}

func extractAgentSlug(relPath string) string {
	parts := strings.SplitN(relPath, string(filepath.Separator), 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func addAllDirs(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}
