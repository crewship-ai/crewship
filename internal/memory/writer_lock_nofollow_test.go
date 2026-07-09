//go:build !windows

package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFileLock_RejectsSymlinkPath pins the lock-file symlink fence (review
// #926): a pre-planted symlink at the lock path must be refused by the open
// (O_NOFOLLOW) instead of being followed — otherwise an attacker who can plant
// a symlink where the sentinel lives could steer the server's O_CREATE|O_RDWR
// open onto an arbitrary file.
func TestFileLock_RejectsSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, "evil.lock")
	if err := os.Symlink(victim, lockPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	lk := NewFileLock(lockPath)
	if err := lk.Lock(); err == nil {
		_ = lk.Unlock()
		t.Fatal("Lock on a symlink path must be rejected (O_NOFOLLOW), got nil error")
	}
}

// TestFileLock_ReusesRegularLockfile guards the O_EXCL trap: a regular,
// already-existing lock file must still be lockable (repeated legitimate locks
// reuse the persistent sentinel — O_NOFOLLOW alone must not break that).
func TestFileLock_ReusesRegularLockfile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "real.lock")

	lk1 := NewFileLock(lockPath)
	if err := lk1.Lock(); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := lk1.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	// Sentinel now exists as a regular file; re-locking must succeed.
	lk2 := NewFileLock(lockPath)
	if err := lk2.Lock(); err != nil {
		t.Fatalf("re-lock existing regular sentinel: %v", err)
	}
	_ = lk2.Unlock()
}
