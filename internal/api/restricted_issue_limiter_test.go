package api

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedSearchAtomicBudgetsAndRecovery(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	take := func(project string, want bool) {
		t.Helper()
		got, _ := s.take(context.Background(), "user", "ws", project)
		if got != want {
			t.Fatalf("project%s allowed%t want%t", project, got, want)
		}
	}
	for i := 0; i < 5; i++ {
		take("a", true)
	}
	for i := 0; i < 20; i++ {
		take("a", false)
	} // Denied project reservations must not consume aggregate.
	for _, p := range []string{"b", "c"} {
		for i := 0; i < 5; i++ {
			take(p, true)
		}
	}
	take("d", false)
	if ok, wait := s.take(context.Background(), "user", "ws", "d"); ok || wait != 3*time.Second {
		t.Fatalf("aggregate recovery: %t %s", ok, wait)
	}
	now = now.Add(3 * time.Second)
	take("d", true)
	take("a", false)
	now = now.Add(3 * time.Second)
	take("a", true)
	// User/workspace keys are independent, regardless of the shared network IP.
	for _, key := range []struct{ user, workspace string }{{"other", "ws"}, {"user", "other"}} {
		if ok, _ := s.take(context.Background(), key.user, key.workspace, "a"); !ok {
			t.Fatal("unrelated identity throttled")
		}
	}
}

func TestSharedSearchConcurrentBurst(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Now()
	s.now = func() time.Time { return now }
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := s.take(context.Background(), "user", "ws", ""); ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 5 {
		t.Fatalf("concurrent burst=%d", admitted.Load())
	}
	if len(s.accounts) != 1 || len(s.accounts[sharedSearchIdentity("user", "ws")].audiences) != 1 {
		t.Fatal("one all-project sentinel required")
	}
}

func TestSharedSearchBoundedMemoryAndIdleRecovery(t *testing.T) {
	s := newSharedSearchLimiter()
	s.maxAccounts = 2
	s.maxAudiences = 2
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for _, p := range []string{"a", "b"} {
		if ok, _ := s.take(context.Background(), "user", "ws", p); !ok {
			t.Fatal("initial audience denied")
		}
	}
	if ok, wait := s.take(context.Background(), "user", "ws", "c"); ok || wait != time.Minute {
		t.Fatal("capacity overflow must be generic transient refusal")
	}
	if ok, _ := s.take(context.Background(), "other", "ws", ""); !ok {
		t.Fatal("second account denied")
	}
	if ok, _ := s.take(context.Background(), "third", "ws", ""); ok {
		t.Fatal("active account evicted to reset capacity")
	}
	if len(s.accounts) != 2 || len(s.accounts[sharedSearchIdentity("user", "ws")].audiences) != 2 {
		t.Fatal("cardinality grew")
	}
	now = now.Add(sharedSearchIdle)
	if ok, _ := s.take(context.Background(), "third", "ws", ""); !ok {
		t.Fatal("idle capacity did not recover")
	}
}

func TestSharedSearchSupportsEveryValidProjectAndSentinel(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < 256; i++ {
		if ok, _ := s.take(context.Background(), "user", "ws", fmt.Sprintf("project%d", i)); !ok {
			t.Fatalf("valid project%d denied", i)
		}
		now = now.Add(3 * time.Second)
	}
	if ok, _ := s.take(context.Background(), "user", "ws", ""); !ok {
		t.Fatal("all-project sentinel denied")
	}
	if got := len(s.accounts[sharedSearchIdentity("user", "ws")].audiences); got != 257 {
		t.Fatalf("audiences=%d want257", got)
	}
}

func TestSharedSearchExpiryRespectsSlowRateOverride(t *testing.T) {
	if got := sharedSearchExpiry(1, 1); got != 15*time.Minute {
		t.Fatalf("slow override expiry=%s", got)
	}
	if got := sharedSearchExpiry(20, 20); got != sharedSearchIdle {
		t.Fatalf("default expiry=%s", got)
	}
}

func TestSharedSearchNormalPollingAndExplicitSearches(t *testing.T) {
	s := newSharedSearchLimiter()
	start := time.Now()
	now := start
	s.now = func() time.Time { return now }
	// Four15s polls and three explicit searches per minute, for five minutes.
	for minute := 0; minute < 5; minute++ {
		for _, second := range []int{0, 2, 4, 6, 15, 30, 45} {
			now = start.Add(time.Duration(minute*60+second) * time.Second)
			if ok, _ := s.take(t.Context(), "user", "ws", ""); !ok {
				t.Fatalf("ordinary polling/search throttled at minute%d second%d", minute, second)
			}
		}
	}
}
func TestSharedSearchTypedKeysAndCancellation(t *testing.T) {
	if sharedSearchIdentity("a", "bc") == sharedSearchIdentity("ab", "c") {
		t.Fatal("tuple identity concatenation collision")
	}
	s := newSharedSearchLimiter()
	now := time.Now()
	s.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if ok, _ := s.take(t.Context(), "user", "ws", ""); !ok {
			t.Fatal("sentinel burst denied")
		}
	}
	if ok, _ := s.take(t.Context(), "user", "ws", "all"); !ok {
		t.Fatal("legacy project ID all shares sentinel budget")
	}
	if len(s.accounts[sharedSearchIdentity("user", "ws")].audiences) != 2 {
		t.Fatal("audience kinds collapsed")
	}
	s = newSharedSearchLimiter()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool, 1)
	s.mu.Lock()
	go func() { ok, _ := s.take(ctx, "user", "ws", ""); done <- ok }()
	cancel()
	s.mu.Unlock()
	if <-done || len(s.accounts) != 0 {
		t.Fatal("canceled waiter consumed budget or allocated a key")
	}
}
