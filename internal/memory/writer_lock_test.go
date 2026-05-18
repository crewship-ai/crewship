//go:build !windows

package memory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// writer.go (NewFileLock) + writer_lock_unix.go (Lock / Unlock).
//
// FileLock is the public surface external callers (consolidator's
// appendRules / snapshotPins) use to serialise O_APPEND writes that
// don't need WriteFile's full atomic-replace dance. Construction is
// pure (no I/O); Lock + Unlock manage the flock(2) sentinel file.
//
// The contract that matters:
//   1. Construction does NO disk I/O (NewFileLock just sets path)
//   2. Lock creates the sentinel on first call (0600 perms)
//   3. Unlock is idempotent — calling on an unlocked FileLock is no-op
//   4. flock is per-fd, so the sentinel file persists across runs
//      but the lock state doesn't (no "stuck lock after crash")
//   5. Lock on a different FileLock instance against the SAME path
//      blocks until the holder Unlocks — the cross-process serialisation
//      this whole helper exists to provide
// ---------------------------------------------------------------------------

func TestNewFileLock_NoIO_OnConstruction(t *testing.T) {
	// Source comment: "Construction does no I/O". Pin so a future
	// refactor that pre-opened the sentinel in NewFileLock doesn't
	// silently introduce a "construction can fail" failure mode for
	// every caller that doesn't currently check the result.
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "subdir-that-does-not-exist", "lockfile.lock")

	lk := NewFileLock(sentinel)
	if lk == nil {
		t.Fatal("NewFileLock returned nil")
	}
	// Sentinel must NOT exist after construction — only after Lock.
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("sentinel created at construction time (err=%v); want IsNotExist", err)
	}
}

func TestNewFileLock_StoresPathVerbatim(t *testing.T) {
	// The path is what the eventual Lock will OpenFile on. A regression
	// that normalized or trimmed the path would change the sentinel
	// location silently — multiple consumers would each get their own
	// "lock" against different files and serialisation would break.
	lk := NewFileLock("/tmp/literal-path-with/special-chars.lock")
	if lk.path != "/tmp/literal-path-with/special-chars.lock" {
		t.Errorf("path = %q, want verbatim input", lk.path)
	}
	if lk.f != nil {
		t.Errorf("f = %v, want nil (no fd allocated at construction)", lk.f)
	}
}

func TestFileLock_LockUnlock_RoundTrip_CreatesSentinel(t *testing.T) {
	// Happy path. Lock on a nonexistent sentinel must create it (0600)
	// and acquire the flock. Unlock releases + closes the fd. The
	// sentinel file persists on disk afterwards (per-fd semantics).
	dir := t.TempDir()
	path := filepath.Join(dir, "lockfile")

	lk := NewFileLock(path)
	if err := lk.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// Sentinel must now exist.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("sentinel missing after Lock: %v", err)
	}
	// 0600 perms (umask permitting — the lower bits can be stripped
	// but the executable bit MUST NOT appear; this is a regression
	// signal for an accidental 0700 / 0755 mode).
	if mode := info.Mode().Perm(); mode&0o111 != 0 {
		t.Errorf("sentinel mode = %o, has executable bits (regression to a wider mode)", mode)
	}

	if err := lk.Unlock(); err != nil {
		t.Errorf("Unlock: %v", err)
	}
	// Sentinel file persists after Unlock — per-fd semantics.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("sentinel disappeared after Unlock: %v (lockfile should persist)", err)
	}
}

func TestFileLock_Unlock_BeforeLock_NoOp(t *testing.T) {
	// Source: "Idempotent — calling Unlock on an unlocked writeLock is
	// a no-op." Defensive: a deferred Unlock that runs against a
	// FileLock whose Lock failed must not crash.
	lk := NewFileLock(filepath.Join(t.TempDir(), "unused"))
	if err := lk.Unlock(); err != nil {
		t.Errorf("Unlock on unlocked FileLock: err = %v, want nil", err)
	}
}

func TestFileLock_DoubleUnlock_NoOp(t *testing.T) {
	// Same idempotency contract: Unlock after a successful Lock+Unlock
	// must also no-op. l.f is cleared on first Unlock; second sees nil
	// and returns. Pin to catch a regression that re-derefenced l.f.
	lk := NewFileLock(filepath.Join(t.TempDir(), "lockfile"))
	if err := lk.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := lk.Unlock(); err != nil {
		t.Errorf("first Unlock: %v", err)
	}
	if err := lk.Unlock(); err != nil {
		t.Errorf("second Unlock: %v (idempotency broken)", err)
	}
}

func TestFileLock_Lock_UnwritableParent_WrapsError(t *testing.T) {
	// Lock(O_CREATE) fails when the parent directory doesn't exist.
	// The wrap "open lockfile: %w" is the operator triage signal.
	lk := NewFileLock(filepath.Join(t.TempDir(), "missing-subdir", "lockfile"))
	err := lk.Lock()
	if err == nil {
		t.Fatal("expected error on missing parent dir")
	}
	if !strings.Contains(err.Error(), "open lockfile") {
		t.Errorf("err = %v, want \"open lockfile\" prefix (triage signal)", err)
	}
	// On failure, no fd should be retained.
	if lk.f != nil {
		t.Errorf("lk.f = %v after failed Lock; want nil (no fd leak)", lk.f)
	}
}

func TestFileLock_LockAfterUnlock_ReacquireWorks(t *testing.T) {
	// Lock → Unlock → Lock must succeed: the second Lock opens a new
	// fd (since Unlock cleared lk.f) and re-acquires the flock. A
	// regression that left a stale fd would fail the second Lock at
	// the OpenFile step with EBADF or similar.
	lk := NewFileLock(filepath.Join(t.TempDir(), "reacquire"))
	if err := lk.Lock(); err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	if err := lk.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := lk.Lock(); err != nil {
		t.Errorf("re-Lock after Unlock: %v (must reacquire cleanly)", err)
	}
	if err := lk.Unlock(); err != nil {
		t.Errorf("cleanup Unlock: %v", err)
	}
}

func TestFileLock_TwoInstances_SamePath_Serialise(t *testing.T) {
	// The whole reason FileLock exists. Two FileLock instances against
	// the same sentinel: instance B's Lock must block until instance A
	// Unlocks. Use a goroutine + timing window to verify.
	//
	// Note: flock(2) at the OS level serialises across PROCESSES; in
	// a single process, two distinct fds opened by separate Lock calls
	// still get separate flock entries and the OS treats them as
	// independent waiters. This works for the test even without
	// process boundaries.
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.lock")

	lkA := NewFileLock(path)
	lkB := NewFileLock(path)

	if err := lkA.Lock(); err != nil {
		t.Fatalf("A.Lock: %v", err)
	}
	bAcquired := make(chan struct{})
	go func() {
		if err := lkB.Lock(); err != nil {
			t.Errorf("B.Lock: %v", err)
		}
		close(bAcquired)
	}()

	// B should be blocked because A holds the lock.
	select {
	case <-bAcquired:
		t.Fatal("B.Lock returned while A still held the lock — flock serialisation broken")
	case <-time.After(100 * time.Millisecond):
		// expected: B is blocked
	}

	// Release A; B should now acquire promptly.
	if err := lkA.Unlock(); err != nil {
		t.Fatalf("A.Unlock: %v", err)
	}
	select {
	case <-bAcquired:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("B.Lock did not acquire within 2s of A.Unlock — release path broken")
	}
	if err := lkB.Unlock(); err != nil {
		t.Errorf("B cleanup Unlock: %v", err)
	}
}

func TestFileLock_DifferentPaths_DoNotBlock(t *testing.T) {
	// Negative-case companion to the previous test: two FileLock
	// instances against DIFFERENT sentinels must NOT serialise. Pin
	// so a refactor that accidentally introduced a process-global
	// lock would surface here.
	dir := t.TempDir()
	lkA := NewFileLock(filepath.Join(dir, "a.lock"))
	lkB := NewFileLock(filepath.Join(dir, "b.lock"))

	if err := lkA.Lock(); err != nil {
		t.Fatalf("A.Lock: %v", err)
	}
	defer lkA.Unlock()

	done := make(chan error, 1)
	go func() { done <- lkB.Lock() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("B.Lock against unrelated path: %v", err)
		}
		if err := lkB.Unlock(); err != nil {
			t.Errorf("B cleanup: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("B.Lock against unrelated path blocked; locks must be scoped per-sentinel")
	}
}

func TestFileLock_ConcurrentHolders_Serialise(t *testing.T) {
	// Sanity check that the lock actually serialises N concurrent
	// writers — each acquires Lock, runs a read-modify-write on a
	// shared FILE under the lock, releases. Final file content equals
	// the worker count; if the lock didn't serialise, lost-update
	// races would leave the count below N.
	//
	// We deliberately use a FILE (not a Go variable) for the shared
	// counter so the Go race detector doesn't flag the access — it
	// has no model of OS-level flock as a happens-before edge.
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "counter.lock")
	counterPath := filepath.Join(dir, "counter.txt")
	if err := os.WriteFile(counterPath, []byte("0"), 0o600); err != nil {
		t.Fatalf("seed counter: %v", err)
	}

	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			lk := NewFileLock(lockPath)
			if err := lk.Lock(); err != nil {
				t.Errorf("Lock: %v", err)
				return
			}
			// Critical section: read-modify-write the counter file.
			data, err := os.ReadFile(counterPath)
			if err != nil {
				t.Errorf("read counter: %v", err)
				_ = lk.Unlock()
				return
			}
			n := 0
			for _, b := range data {
				if b < '0' || b > '9' {
					break
				}
				n = n*10 + int(b-'0')
			}
			// Widen the race window — without flock another goroutine
			// would read the same `n` and lose an increment.
			time.Sleep(1 * time.Millisecond)
			next := []byte{}
			for v := n + 1; v > 0; v /= 10 {
				next = append([]byte{byte('0' + v%10)}, next...)
			}
			if err := os.WriteFile(counterPath, next, 0o600); err != nil {
				t.Errorf("write counter: %v", err)
			}
			if err := lk.Unlock(); err != nil {
				t.Errorf("Unlock: %v", err)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	final := 0
	for _, b := range data {
		if b < '0' || b > '9' {
			break
		}
		final = final*10 + int(b-'0')
	}
	if final != workers {
		t.Errorf("counter = %d after %d serialised writes, want %d (some updates were lost — lock did not serialise)",
			final, workers, workers)
	}
}
