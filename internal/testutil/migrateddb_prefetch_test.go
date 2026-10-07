package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// waitPrefetched waits until the active prefetcher has at least n copies
// ready, so a test can be sure the next MigratedDB call takes the prefetched
// path rather than racing the workers.
func waitPrefetched(t *testing.T, n int) {
	t.Helper()
	prefetchMu.Lock()
	p := activePrefetch
	prefetchMu.Unlock()
	if p == nil {
		t.Fatal("no prefetcher is active")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for len(p.ready) < n {
		if time.Now().After(deadline) {
			t.Fatalf("prefetcher produced %d ready copies in 2m, want %d", len(p.ready), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func requirePrefetchable(t *testing.T) {
	t.Helper()
	// Prefetching is disabled at GOMAXPROCS=1 by design; run these tests
	// with two procs instead of skipping them on single-CPU runners.
	if prev := runtime.GOMAXPROCS(0); prev < 2 {
		runtime.GOMAXPROCS(2)
		t.Cleanup(func() { runtime.GOMAXPROCS(prev) })
	}
}

// A prefetched copy must be indistinguishable from a synchronous one: same
// schema, same ledger, same production pragmas, and private to its test.
func TestPrefetchedMigratedDB_IsAFullPrivateCopy(t *testing.T) {
	requirePrefetchable(t)
	stop := PrefetchMigratedDBs(1, 2)
	t.Cleanup(stop)

	_ = MigratedSQLDB(t) // starts the worker (and builds the template)
	waitPrefetched(t, 2)

	a := MigratedDB(t)
	b := MigratedDB(t)
	if a.Path() == b.Path() {
		t.Fatalf("two prefetched copies share a file: %s", a.Path())
	}

	syncDB := MigratedSQLDB(t)
	stop() // the remaining calls in this test use the synchronous path again
	want, got := objectNames(t, syncDB), objectNames(t, a.DB)
	if len(got) != len(want) {
		t.Fatalf("prefetched schema has %d objects, synchronous copy has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("schema object %d differs:\nprefetched: %s\nsynchronous: %s", i, got[i], want[i])
		}
	}

	var fk int
	if err := a.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("prefetched copy foreign_keys = %d (err %v), want 1", fk, err)
	}
	var journal string
	if err := a.DB.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("prefetched copy journal_mode = %q (err %v), want wal", journal, err)
	}
	if got := a.DB.Stats().MaxOpenConnections; got != 5 {
		t.Fatalf("prefetched copy pool = %d, want the production 5", got)
	}

	if _, err := a.DB.Exec(`INSERT INTO users (id, email, full_name) VALUES ('only-in-a', 'a@example.com', 'A')`); err != nil {
		t.Fatalf("write to prefetched copy: %v", err)
	}
	var n int
	if err := b.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id = 'only-in-a'`).Scan(&n); err != nil {
		t.Fatalf("read other prefetched copy: %v", err)
	}
	if n != 0 {
		t.Fatal("a row written to one prefetched copy is visible in another")
	}
}

// A handed-out copy is cleaned up by its test exactly like a synchronous one,
// and stopping the prefetcher removes the copies nobody took.
func TestPrefetchedMigratedDB_CleanupLeavesNothingBehind(t *testing.T) {
	requirePrefetchable(t)
	stop := PrefetchMigratedDBs(1, 3)
	t.Cleanup(stop)

	var handedOut string
	t.Run("take", func(t *testing.T) {
		_ = MigratedSQLDB(t)
		waitPrefetched(t, 3)
		handedOut = filepath.Dir(MigratedDB(t).Path())
	})
	if _, err := os.Stat(handedOut); !os.IsNotExist(err) {
		t.Fatalf("handed-out copy's directory survived its test: %s (stat err %v)", handedOut, err)
	}

	stop()

	// Shutdown discards what is still queued. Exercised on a prefetcher with
	// no workers, so the queue's contents are exactly what the test put there.
	p := &prefetcher{ready: make(chan prefetchedItem, 2), stop: make(chan struct{})}
	var untaken []string
	for i := 0; i < 2; i++ {
		it, err := openPrefetched()
		if err != nil {
			t.Fatalf("open a copy: %v", err)
		}
		untaken = append(untaken, it.dir)
		p.ready <- it
	}
	p.shutdown()
	for _, dir := range untaken {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("shutdown left an untaken copy behind: %s (stat err %v)", dir, err)
		}
	}

	prefetchMu.Lock()
	still := activePrefetch
	prefetchMu.Unlock()
	if still != nil {
		t.Fatal("stop did not disable prefetching")
	}
	if _, ok := takePrefetched(); ok {
		t.Fatal("a copy was handed out after stop")
	}
}

func TestMigratedTemplateEnv_OnlyTheSameBinaryInherits(t *testing.T) {
	env := MigratedTemplateEnv(t)
	if len(env) != 2 {
		t.Fatalf("MigratedTemplateEnv = %v, want two entries", env)
	}
	template := MigratedTemplatePath(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	t.Run("same binary inherits", func(t *testing.T) {
		t.Setenv(inheritTemplateEnv, template)
		t.Setenv(inheritTemplateExeEnv, exe)
		got, ok := inheritedTemplate()
		if !ok || got != template {
			t.Fatalf("inheritedTemplate = %q, %v; want %q, true", got, ok, template)
		}
	})
	t.Run("another binary does not", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "other.test")
		if err := os.WriteFile(other, []byte("not this binary"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(inheritTemplateEnv, template)
		t.Setenv(inheritTemplateExeEnv, other)
		if got, ok := inheritedTemplate(); ok {
			t.Fatalf("inherited %q from a different executable", got)
		}
	})
	t.Run("a missing template does not", func(t *testing.T) {
		t.Setenv(inheritTemplateEnv, filepath.Join(t.TempDir(), "gone.db"))
		t.Setenv(inheritTemplateExeEnv, exe)
		if got, ok := inheritedTemplate(); ok {
			t.Fatalf("inherited missing template %q", got)
		}
	})
	t.Run("unset does not", func(t *testing.T) {
		t.Setenv(inheritTemplateEnv, "")
		t.Setenv(inheritTemplateExeEnv, "")
		if _, ok := inheritedTemplate(); ok {
			t.Fatal("inherited with no environment")
		}
	})
}

// Stopping concurrently with the first take must leave no worker running,
// and a take after stop must not hand out a copy.
func TestPrefetch_StopRacingFirstTakeStartsNoLateWorkers(t *testing.T) {
	requirePrefetchable(t)
	stop := PrefetchMigratedDBs(2, 2)
	prefetchMu.Lock()
	p := activePrefetch
	prefetchMu.Unlock()
	done := make(chan struct{})
	go func() { p.start.Do(p.startWorkers); close(done) }()
	stop()
	<-done
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if !closed {
		t.Fatal("shutdown did not mark the prefetcher closed")
	}
	p.wg.Wait()
	if _, ok := takePrefetched(); ok {
		t.Fatal("a copy was handed out after stop")
	}
}
