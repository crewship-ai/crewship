package fileserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type FileEvent struct {
	Event     string    `json:"event"`
	Path      string    `json:"path"`
	Agent     string    `json:"agent"`
	Size      int64     `json:"size"`
	Timestamp time.Time `json:"timestamp"`
}

type EventHandler func(crewID string, event FileEvent)

type Watcher struct {
	basePath string
	logger   *slog.Logger
	handler  EventHandler
}

func NewWatcher(basePath string, logger *slog.Logger, handler EventHandler) *Watcher {
	return &Watcher{
		basePath: basePath,
		logger:   logger,
		handler:  handler,
	}
}

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

	go func() {
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
