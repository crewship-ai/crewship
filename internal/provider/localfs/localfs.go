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

type Provider struct {
	basePath string
}

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
	return full, nil
}

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

func (p *Provider) Write(_ context.Context, path string, r io.Reader) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0750); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

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

func (p *Provider) Delete(_ context.Context, path string) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

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

func (p *Provider) EnsureDir(_ context.Context, path string) error {
	full, err := p.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0750)
}

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
