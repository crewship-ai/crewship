//go:build darwin

package writerlease

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinLeaseDoesNotBlockSQLiteLockRegion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owner, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	conn, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// SQLite uses pending/reserved/shared byte locks near offset 1 GiB.
	region := unix.Flock_t{Type: unix.F_WRLCK, Whence: int16(os.SEEK_SET), Start: 1 << 30, Len: 512}
	if err = unix.FcntlFlock(conn.Fd(), unix.F_SETLK, &region); err != nil {
		t.Fatalf("writer lease collides with SQLite byte locks: %v", err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	competitor, err := Acquire(path)
	if competitor != nil {
		competitor.Close()
	}
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("SQLite descriptor close released ownership: %v", err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != 0 {
		t.Fatalf("beyond-EOF lock extended database: %d", stat.Size())
	}
}
