package pages

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Compact runs under a shared workspace lease and an exclusive Git lease.
// Published artifact reads and build completion remain available, while new
// checkpoint objects cannot be pruned between their write and their ref pin.
func (s *ProjectStore) Compact(ctx context.Context, ws string) error {
	release, err := s.Lease(ctx, ws, false)
	if err != nil {
		return err
	}
	defer release()
	releaseGit, err := s.GitLease(ctx, ws, true)
	if err != nil {
		return err
	}
	defer releaseGit()
	root, err := s.root(ws, false)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open("git")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	repos, err := dir.ReadDir(4096)
	if err != nil && err != io.EOF {
		return err
	}
	if len(repos) >= 4096 {
		return errors.New("too many Page repositories")
	}
	for _, repo := range repos {
		name := repo.Name()
		if !repo.IsDir() || len(name) != 64 || strings.Trim(name, "0123456789abcdef") != "" {
			return errors.New("unexpected Page repository entry")
		}
		path := filepath.Join(root.Name(), "git", name)
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err = runProjectGit(bounded, path, nil, "-c", "pack.threads=1", "-c", "pack.windowMemory=16m", "repack", "-Ad", "--depth=10", "--window=10")
		if err == nil {
			_, err = runProjectGit(bounded, path, nil, "prune", "--expire=now")
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
