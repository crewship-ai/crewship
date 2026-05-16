//go:build windows

package memory

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Lock acquires an exclusive lock on the sentinel file via
// LockFileEx. The Unix flock(2) semantics map to Windows
// LockFileEx with LOCKFILE_EXCLUSIVE_LOCK over the entire file
// range. Blocking by default — no LOCKFILE_FAIL_IMMEDIATELY flag.
func (l *writeLock) Lock() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open lockfile: %w", err)
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, &ol,
	); err != nil {
		_ = f.Close()
		return fmt.Errorf("LockFileEx: %w", err)
	}
	l.f = f
	return nil
}

// Unlock releases the lock and closes the underlying fd. Idempotent.
func (l *writeLock) Unlock() error {
	if l.f == nil {
		return nil
	}
	var ol windows.Overlapped
	var firstErr error
	if err := windows.UnlockFileEx(
		windows.Handle(l.f.Fd()),
		0, 1, 0, &ol,
	); err != nil {
		firstErr = err
	}
	if err := l.f.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.f = nil
	return firstErr
}
