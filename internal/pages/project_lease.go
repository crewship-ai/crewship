package pages

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// Lease coordinates retention/restore with readers, writers and backup in other
// processes too. Shared leases protect immutable file -> SQL handoffs; an
// exclusive lease is required before deleting files or restoring a namespace.
func (s *ProjectStore) Lease(ctx context.Context, ws string, exclusive bool) (func(), error) {
	return s.fileLease(ctx, ws, ".maintenance.lock", exclusive, 30*time.Second)
}

// GitLease excludes object writers from compaction without blocking artifact readers.
// Acquire the ordinary workspace lease first. Restore remains exclusive over both.
func (s *ProjectStore) GitLease(ctx context.Context, ws string, exclusive bool) (func(), error) {
	return s.fileLease(ctx, ws, ".git-maintenance.lock", exclusive, 2*time.Minute)
}

func (s *ProjectStore) fileLease(ctx context.Context, ws, name string, exclusive bool, timeout time.Duration) (func(), error) {
	root, err := s.root(ws, true)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	root.Close()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		acquired, err := tryProjectFileLock(file, exclusive)
		if err != nil {
			file.Close()
			return nil, err
		}
		if acquired {
			var once sync.Once
			return func() { once.Do(func() { file.Close() }) }, nil
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, fmt.Errorf("Page maintenance is busy: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
