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
	// O_NOFOLLOW refuses to open the sentinel if its final component is a
	// symlink (open fails with ELOOP), so an attacker who can plant a symlink
	// where the lockfile lives can't steer this O_CREATE|O_RDWR open onto an
	// arbitrary file (review #926). We deliberately do NOT add O_EXCL: the
	// sentinel is a persistent on-disk file that legitimate repeated locks
	// reuse, and O_EXCL would fail every lock after the first.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
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
