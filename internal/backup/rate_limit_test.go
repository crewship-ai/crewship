package backup

import (
	"sync"
	"testing"
	"time"
)

// Note on parallelism: these tests share the package-level
// instanceBackupLimiter, so they run serially. Each test calls
// ResetInstanceBackupLimiter() up-front to erase any state from a
// previous test (or the calling process).
func withFreshLimiter(t *testing.T) {
	t.Helper()
	ResetInstanceBackupLimiter()
	t.Cleanup(ResetInstanceBackupLimiter)
}

// Missing userID must fail-closed: the limiter is the last backstop against a
// bypass of authentication, so an empty identity is denied and the client is
// asked to back off for a full window (not hammered immediately).
func TestAllowInstanceBackup_EmptyUserDeniesWithFullBackoff(t *testing.T) {
	withFreshLimiter(t)

	ok, retry := AllowInstanceBackup("")
	if ok {
		t.Fatal("empty userID must not be allowed")
	}
	if retry != defaultInstanceBackupWindow {
		t.Errorf("retryAfter: got %v, want %v", retry, defaultInstanceBackupWindow)
	}
}

func TestAllowInstanceBackup_FirstCallAllowed(t *testing.T) {
	withFreshLimiter(t)

	ok, retry := AllowInstanceBackup("user-1")
	if !ok {
		t.Errorf("first call should be allowed; got ok=%v retry=%v", ok, retry)
	}
	if retry != 0 {
		t.Errorf("retryAfter on success: got %v, want 0", retry)
	}
}

// The second call within the sliding window hits the per-user limit (1 by
// default). The returned retry-after must be positive and bounded by the
// configured window so callers have a sane Retry-After header to surface.
func TestAllowInstanceBackup_SecondCallWithinWindowIsDenied(t *testing.T) {
	withFreshLimiter(t)

	if ok, _ := AllowInstanceBackup("user-1"); !ok {
		t.Fatal("first call should have succeeded")
	}
	ok, retry := AllowInstanceBackup("user-1")
	if ok {
		t.Fatal("second call within window must be denied")
	}
	if retry <= 0 || retry > defaultInstanceBackupWindow {
		t.Errorf("retryAfter out of bounds: got %v, want (0, %v]", retry, defaultInstanceBackupWindow)
	}
}

// The limiter keys its map on userID — each admin must be tracked
// independently so one user's rehearsal doesn't starve another.
func TestAllowInstanceBackup_PerUserIsolation(t *testing.T) {
	withFreshLimiter(t)

	if ok, _ := AllowInstanceBackup("alice"); !ok {
		t.Fatal("alice's first call should have succeeded")
	}
	// bob is a different user, so he gets his own fresh window.
	if ok, _ := AllowInstanceBackup("bob"); !ok {
		t.Fatal("bob's first call should have succeeded independently of alice")
	}
	// alice's second call still denied.
	if ok, _ := AllowInstanceBackup("alice"); ok {
		t.Error("alice's second call should have been denied")
	}
	// bob's second call denied.
	if ok, _ := AllowInstanceBackup("bob"); ok {
		t.Error("bob's second call should have been denied")
	}
}

// After the sliding window ends, a fresh call is allowed again. The limiter
// doesn't expose a clock hook, so we mutate the internal recorded slice to
// stamp a "past" entry and trigger the pruning path on the next call.
// This is testing-package white-box access into package state.
func TestAllowInstanceBackup_WindowElapseAllowsAgain(t *testing.T) {
	withFreshLimiter(t)

	// Seed a recorded entry that is already outside the sliding window —
	// the next call should treat it as stale, prune it, and allow a new one.
	instanceBackupLimiter.mu.Lock()
	instanceBackupLimiter.recorded["user-old"] = []time.Time{
		time.Now().Add(-(defaultInstanceBackupWindow + time.Minute)),
	}
	instanceBackupLimiter.mu.Unlock()

	ok, retry := AllowInstanceBackup("user-old")
	if !ok {
		t.Errorf("stale entry should have been pruned; got deny retry=%v", retry)
	}

	// After allowing, exactly one fresh entry remains (the old one was pruned).
	instanceBackupLimiter.mu.Lock()
	count := len(instanceBackupLimiter.recorded["user-old"])
	instanceBackupLimiter.mu.Unlock()
	if count != 1 {
		t.Errorf("pruning failed: got %d entries, want 1", count)
	}
}

// RetryAfter should shrink as time progresses toward the oldest entry's
// expiry. Since we can't advance wall-clock time, seed a near-expired entry
// and verify the returned duration is small — it should be no more than the
// fudge factor we plant.
func TestAllowInstanceBackup_RetryAfterReflectsOldestEntry(t *testing.T) {
	withFreshLimiter(t)

	// Seed an entry that's MOST of the window old; retry-after should be
	// roughly the remaining 5 s slice.
	remaining := 5 * time.Second
	fakeOldest := time.Now().Add(-(defaultInstanceBackupWindow - remaining))
	instanceBackupLimiter.mu.Lock()
	instanceBackupLimiter.recorded["u"] = []time.Time{fakeOldest}
	instanceBackupLimiter.mu.Unlock()

	ok, retry := AllowInstanceBackup("u")
	if ok {
		t.Fatal("expected deny; limiter should still see the seeded entry")
	}
	// The function recomputes its own `now`, so retry ≈ remaining with a bit of wobble.
	if retry <= 0 || retry > remaining+200*time.Millisecond {
		t.Errorf("retryAfter: got %v, want ~%v", retry, remaining)
	}
}

// ResetInstanceBackupLimiter wipes the per-user map — without it a
// previously-hit user would stay blocked for the full window across tests
// and in production the admin could use it as an operational reset.
func TestResetInstanceBackupLimiter_WipesState(t *testing.T) {
	withFreshLimiter(t)

	if ok, _ := AllowInstanceBackup("user-x"); !ok {
		t.Fatal("first call should have succeeded")
	}
	if ok, _ := AllowInstanceBackup("user-x"); ok {
		t.Fatal("second call before reset should be denied")
	}

	ResetInstanceBackupLimiter()

	if ok, _ := AllowInstanceBackup("user-x"); !ok {
		t.Error("after Reset, user-x should be allowed again")
	}
}

// The limiter serialises access via a mutex; this test fires N concurrent
// calls from the same userID and confirms EXACTLY limit of them succeed.
// A lost update from a bad mutex would surface as >limit successes.
func TestAllowInstanceBackup_ConcurrentCallsRespectLimit(t *testing.T) {
	withFreshLimiter(t)

	const callers = 50
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := AllowInstanceBackup("hammer"); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != defaultInstanceBackupLimit {
		t.Errorf("allowed count: got %d, want exactly %d", allowed, defaultInstanceBackupLimit)
	}
}
