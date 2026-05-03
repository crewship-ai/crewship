//go:build !windows

package backup

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// fileLock is an OS-level advisory lock on a sentinel file next to the
// keyring (path + ".lock"). On POSIX it uses flock(2) — concurrent
// processes block on Lock() until the holder calls Unlock(). The lock
// file persists across runs which is harmless: flock state is per-fd,
// not per-inode-on-disk, so a stale empty file does not "stay locked".
type fileLock struct {
	path string
	f    *os.File
}

// newFileLock prepares a lock anchored at path + ".lock". The sentinel
// is created on first Lock() — newFileLock itself does no I/O so that
// constructing a Keyring stays cheap and side-effect-free.
func newFileLock(path string) *fileLock {
	return &fileLock{path: path + ".lock"}
}

// Lock acquires an exclusive advisory lock on the sentinel file,
// creating it if needed. Blocks until the lock is granted.
func (l *fileLock) Lock() error {
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

// Unlock releases the advisory lock and closes the underlying fd. Idempotent.
func (l *fileLock) Unlock() error {
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
	if firstErr != nil {
		return fmt.Errorf("unlock: %w", firstErr)
	}
	return nil
}
