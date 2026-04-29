package localfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/fsnotify/fsnotify"
)

var _ provider.StorageProvider = (*Provider)(nil)

// Provider implements StorageProvider using the local filesystem. All paths
// are resolved relative to basePath with symlink-aware path traversal protection.
type Provider struct {
	basePath string
}

// New creates a local filesystem Provider rooted at basePath, creating the
// directory if it does not exist.
func New(basePath string) (*Provider, error) {
	if err := os.MkdirAll(basePath, 0750); err != nil {
		return nil, fmt.Errorf("create base dir %s: %w", basePath, err)
	}
	return &Provider{basePath: basePath}, nil
}

func (p *Provider) resolve(path string) (string, error) {
	full := filepath.Join(p.basePath, filepath.Clean(path))
	rel, err := filepath.Rel(p.basePath, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path traversal detected: %s", path)
	}
	// V-09: Resolve symlinks and re-check containment to prevent symlink escape
	realBase, baseErr := filepath.EvalSymlinks(p.basePath)
	if baseErr != nil {
		return "", fmt.Errorf("resolve base path: %w", baseErr)
	}
	// Only check symlinks if the path exists (new files won't resolve)
	if realFull, evalErr := filepath.EvalSymlinks(full); evalErr == nil {
		if !strings.HasPrefix(realFull, realBase+string(os.PathSeparator)) && realFull != realBase {
			return "", fmt.Errorf("path traversal detected (symlink): %s", path)
		}
		return realFull, nil
	}
	return full, nil
}

// Read opens the file at the given path for reading.
func (p *Provider) Read(_ context.Context, path string) (io.ReadCloser, error) {
	full, err := p.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}

// Write creates or overwrites the file at path with content from r.
//
// On a shared bind-mount where files may have been created by another
// uid (e.g. the agent container at uid 1001 while crewshipd runs as
// uid 1000), os.Create can fail with EACCES on an existing file even
// though the calling process has group-write via the bind-mount setgid
// + group-shared layout. Retry path:
//  1. Best-effort chmod 0664 — opens up the file if we own it OR if
//     it's group-writable already (no-op in those cases).
//  2. If create still fails with EACCES, try unlink + create — works
//     when the parent dir is writable for our uid/gid.
func (p *Provider) Write(_ context.Context, path string, r io.Reader) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0775); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	// Best-effort: relax mode on the existing file before re-opening
	// it for write. Ignore failures (file may not exist yet, or we
	// may not own it — os.Create will report the real problem).
	_ = os.Chmod(full, 0664)
	f, err := os.Create(full)
	if err != nil && os.IsPermission(err) {
		// Last-resort: unlink and recreate. Works when the parent dir
		// is group-writable to us. Files we recreate this way drop
		// previous ownership; the entrypoint sets umask 0002 so
		// future writes from agent-side land at 0664 instead of 0644.
		if rmErr := os.Remove(full); rmErr == nil {
			f, err = os.Create(full)
		}
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// List returns file entries in the given directory (non-recursive).
func (p *Provider) List(_ context.Context, dir string) ([]provider.FileInfo, error) {
	full, err := p.resolve(dir)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var files []provider.FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(p.basePath, filepath.Join(full, e.Name()))
		files = append(files, provider.FileInfo{
			Path:    rel,
			Name:    e.Name(),
			Size:    info.Size(),
			IsDir:   e.IsDir(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

// ListRecursive walks the directory tree and returns all file entries.
func (p *Provider) ListRecursive(_ context.Context, dir string) ([]provider.FileInfo, error) {
	full, err := p.resolve(dir)
	if err != nil {
		return nil, err
	}

	var files []provider.FileInfo
	err = filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == full {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(p.basePath, path)
		if relErr != nil {
			return nil
		}
		files = append(files, provider.FileInfo{
			Path:    rel,
			Name:    d.Name(),
			Size:    info.Size(),
			IsDir:   d.IsDir(),
			ModTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk dir %s: %w", dir, err)
	}
	return files, nil
}

// Delete removes the file or directory at path (recursively if a directory).
func (p *Provider) Delete(_ context.Context, path string) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

// Exists reports whether a file or directory exists at the given path.
func (p *Provider) Exists(_ context.Context, path string) (bool, error) {
	full, err := p.resolve(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(full)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// EnsureDir creates the directory at path if it does not already exist.
func (p *Provider) EnsureDir(_ context.Context, path string) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0750)
}

// Watch starts watching the directory tree for filesystem changes, sending
// events to the provided channel until ctx is cancelled.
func (p *Provider) Watch(ctx context.Context, dir string, events chan<- provider.FileEvent) error {
	full, err := p.resolve(dir)
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	if err := addRecursive(watcher, full); err != nil {
		watcher.Close()
		return fmt.Errorf("watch %s: %w", dir, err)
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				fe := p.toFileEvent(event, full)
				if fe != nil {
					select {
					case events <- *fe:
					case <-ctx.Done():
						return
					}
				}
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = watcher.Add(event.Name)
					}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

func (p *Provider) toFileEvent(event fsnotify.Event, baseDir string) *provider.FileEvent {
	rel, err := filepath.Rel(baseDir, event.Name)
	if err != nil {
		return nil
	}

	var op string
	switch {
	case event.Has(fsnotify.Create):
		op = "create"
	case event.Has(fsnotify.Write):
		op = "write"
	case event.Has(fsnotify.Remove):
		op = "remove"
	case event.Has(fsnotify.Rename):
		op = "rename"
	default:
		return nil
	}

	var size int64
	if info, err := os.Stat(event.Name); err == nil {
		size = info.Size()
	}

	agent := extractAgent(rel)

	return &provider.FileEvent{
		Op:    op,
		Path:  rel,
		Agent: agent,
		Size:  size,
	}
}

func extractAgent(relPath string) string {
	parts := strings.SplitN(relPath, string(filepath.Separator), 2)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func addRecursive(w *fsnotify.Watcher, dir string) error {
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
