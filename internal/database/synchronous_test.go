package database

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// pragmaSynchronous returns SQLite's numeric level: 0 OFF, 1 NORMAL, 2 FULL,
// 3 EXTRA.
func pragmaSynchronous(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&n); err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	return n
}

func TestOpen_DefaultsToFullSynchronous(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if got := db.Synchronous(); got != SynchronousFull {
		t.Errorf("Synchronous() = %q, want %q", got, SynchronousFull)
	}
	if got := pragmaSynchronous(t, db); got != 2 {
		t.Errorf("PRAGMA synchronous = %d, want 2 (FULL)", got)
	}
}

// The pragma is per-connection and the pool holds five. Setting it anywhere but
// the DSN would leave whichever connections were opened first on the default —
// so a durable commit would depend on which connection the request happened to
// draw. This pins that every connection in the pool reports the same level.
func TestOpen_SynchronousAppliesToEveryPooledConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Hold five connections open at once, so the pool is forced to create all
	// of them, and read the pragma on each while it is held.
	const conns = 5
	ctx := context.Background()
	var wg sync.WaitGroup
	release := make(chan struct{})
	levels := make([]int, conns)
	errs := make([]error, conns)
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := db.Conn(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			defer c.Close()
			if err := c.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&levels[i]); err != nil {
				errs[i] = err
				return
			}
			<-release
		}(i)
	}
	// Give every goroutine time to take its connection before any is returned.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	for i := 0; i < conns; i++ {
		if errs[i] != nil {
			t.Fatalf("connection %d: %v", i, errs[i])
		}
		if levels[i] != 2 {
			t.Errorf("connection %d reports synchronous = %d, want 2 (FULL)", i, levels[i])
		}
	}
}

func TestWithSynchronous_NormalIsOptInAndVisible(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), WithSynchronous(SynchronousNormal))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	if got := db.Synchronous(); got != SynchronousNormal {
		t.Errorf("Synchronous() = %q, want %q", got, SynchronousNormal)
	}
	if got := pragmaSynchronous(t, db); got != 1 {
		t.Errorf("PRAGMA synchronous = %d, want 1 (NORMAL)", got)
	}
}

func TestOpen_RejectsUnknownSynchronousAndNonPositiveBusyTimeout(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "a.db"), WithSynchronous("OFF")); err == nil {
		t.Error("OFF was accepted; a mode that does not fsync at all must be refused rather than silently honoured")
	}
	if _, err := Open(filepath.Join(dir, "b.db"), WithBusyTimeout(0)); err == nil {
		t.Error("a zero busy timeout was accepted; it would turn every lock contention into an immediate SQLITE_BUSY")
	}
}

func TestWithBusyTimeout_ReachesTheConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"), WithBusyTimeout(1500*time.Millisecond))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var ms int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if ms != 1500 {
		t.Errorf("PRAGMA busy_timeout = %d, want 1500", ms)
	}
}
