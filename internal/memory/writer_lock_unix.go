//go:build !windows

package memory

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Lock acquires an exclusive advisory lock on the sentinel file. The
// sentinel is created on first call; subsequent calls re-open it. The
// flock(2) state lives per-fd, so a persistent on-disk lockfile does
// not "stay locked" across runs.
func (l *writeLock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lockfile: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return fmt.Errorf("flock: %w", err)
	}
	l.f = f
	return nil
}

// Unlock releases the advisory lock and closes the underlying fd.
// Idempotent — calling Unlock on an unlocked writeLock is a no-op.
func (l *writeLock) Unlock() error {
	if l.f == nil {
		return nil
	}
	var firstErr error
	if err := unix.Flock(int(l.f.Fd()), unix.LOCK_UN); err != nil {
		firstErr = err
	}
	if err := l.f.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.f = nil
	return firstErr
}
