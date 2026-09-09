package pages

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

var ErrProjectStoreFull = errors.New("Page source storage quota reached")

// ProjectStore stores immutable source snapshots outside SQLite and outside
// crew mounts. The directory is operator configuration, never request input.
// One store instance per server serializes quota admission. Multi-process writers
// sharing this directory are unsupported, like the current SQLite deployment.
type ProjectStore struct {
	Directory string
	locks     sync.Map // workspace -> *sync.Mutex
}

func (s *ProjectStore) root(workspace string, create bool) (*os.Root, error) {
	if s == nil || s.Directory == "" || workspace == "" {
		return nil, errors.New("Page project storage is not configured")
	}
	if create {
		if err := os.MkdirAll(s.Directory, 0700); err != nil {
			return nil, err
		}
	}
	r, err := os.OpenRoot(s.Directory)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	ns := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	if create {
		if err := r.Mkdir(ns, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return r.OpenRoot(ns)
}

func (s *ProjectStore) Put(ctx context.Context, workspace string, p *SourceProject) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, err := p.MarshalYAMLSource()
	if err != nil {
		return "", err
	}
	digest, err := p.Digest()
	if err != nil {
		return "", err
	}
	if s == nil {
		return "", errors.New("Page project storage is not configured")
	}
	mu := s.workspaceMutex(workspace)
	mu.Lock()
	defer mu.Unlock()
	r, err := s.root(workspace, true)
	if err != nil {
		return "", err
	}
	defer r.Close()
	name := digest + ".yaml"
	if existing, err := s.Get(ctx, workspace, digest); err == nil && existing != nil {
		return digest, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	// Bound both bytes and inode count, including crash leftovers. No automatic
	// deletion: a snapshot may still be referenced by a draft or backup.
	d, err := r.Open(".")
	if err != nil {
		return "", err
	}
	entries, readErr := d.ReadDir(1024)
	d.Close()
	if readErr != nil && readErr != io.EOF {
		return "", readErr
	}
	if len(entries) >= 1024 {
		return "", ErrProjectStoreFull
	}
	var total int64
	count := 0
	for _, entry := range entries {
		if (entry.Name() == ".maintenance.lock" || entry.Name() == ".git-maintenance.lock") || (entry.Name() == "git" && entry.IsDir()) {
			continue
		}
		count++
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		total += info.Size()
	}
	if count >= 256 || total+int64(len(b)) > 128<<20 {
		return "", ErrProjectStoreFull
	}
	tmp := fmt.Sprintf(".staging-%x", rand.Text())
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	defer r.Remove(tmp)
	_, writeErr := f.Write(b)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.Rename(tmp, name); err != nil {
		return "", err
	}
	dir, err := r.Open(".")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *ProjectStore) Get(ctx context.Context, workspace, digest string) (*SourceProject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return nil, errors.New("invalid source digest")
	}
	r, err := s.root(workspace, false)
	if err != nil {
		return nil, fmt.Errorf("open Page source namespace: %w", err)
	}
	defer r.Close()
	f, err := r.Open(digest + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("open Page source %s: %w", digest, err)
	}
	defer f.Close()
	p, err := ParseSourceProject(f)
	if err != nil {
		return nil, err
	}
	actual, err := p.Digest()
	if err != nil {
		return nil, err
	}
	if actual != digest {
		return nil, errors.New("Page source digest mismatch")
	}
	return p, nil
}

func (s *ProjectStore) workspaceMutex(ws string) *sync.Mutex {
	value, _ := s.locks.LoadOrStore(ws, &sync.Mutex{})
	return value.(*sync.Mutex)
}
