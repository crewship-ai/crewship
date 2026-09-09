package pagebuild

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrStoreFull = errors.New("Page artifact storage quota reached")

type Store struct {
	Directory string
	mu        sync.Mutex
}

func (s *Store) root(workspace string, create bool) (*os.Root, error) {
	if s == nil || s.Directory == "" || workspace == "" {
		return nil, errors.New("Page artifact storage is not configured")
	}
	if create {
		if err := os.MkdirAll(s.Directory, 0700); err != nil {
			return nil, err
		}
	}
	base, err := os.OpenRoot(s.Directory)
	if err != nil {
		return nil, err
	}
	defer base.Close()
	ns := fmt.Sprintf("%x", sha256.Sum256([]byte(workspace)))
	if create {
		if err := base.Mkdir(ns, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return base.OpenRoot(ns)
}
func (s *Store) Put(ctx context.Context, ws string, a *Artifact) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	b, digest, err := a.Encode()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.root(ws, true)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if _, err := s.Get(ctx, ws, digest); err == nil {
		return digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir, err := r.Open(".")
	if err != nil {
		return "", err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(257)
	if err != nil && err != io.EOF {
		return "", err
	}
	if len(entries) >= 256 {
		return "", ErrStoreFull
	}
	var size int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		size += info.Size()
	}
	if size+int64(len(b)) > 128<<20 {
		return "", ErrStoreFull
	}
	tmp := ".staging-" + rand.Text()
	defer r.Remove(tmp)
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := r.Rename(tmp, digest+".json"); err != nil {
		return "", err
	}
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return digest, nil
}
func (s *Store) Get(ctx context.Context, ws, digest string) (*Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(digest) {
		return nil, errors.New("invalid artifact digest")
	}
	r, err := s.root(ws, false)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(digest + ".json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxArtifactDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxArtifactDocumentBytes {
		return nil, errors.New("artifact exceeds limit")
	}
	var a Artifact
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	_, actual, err := a.Encode()
	if err != nil {
		return nil, err
	}
	if actual != digest {
		return nil, errors.New("artifact digest mismatch")
	}
	return &a, nil
}

// StoragePressure is a pre-admission signal, not an authority to remove roots.
// The caller holds the project namespace lease; retention selects SQL roots.
func (s *Store) StoragePressure(ctx context.Context, ws string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root(ws, true)
	if err != nil {
		return false, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(257)
	if err != nil && err != io.EOF {
		return false, err
	}
	var size int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		size += info.Size()
	}
	return len(entries) >= 224 || size >= 96<<20, nil
}
