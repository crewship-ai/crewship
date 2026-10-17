package api

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSharedSearchGlobalAudienceCapacity(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Now()
	s.now = func() time.Time { return now }
	for user := 0; user < 4096; user++ {
		for project := 0; project < 8; project++ {
			if ok, _ := s.take(context.Background(), fmt.Sprintf("user%d", user), "ws", fmt.Sprintf("project%d", project)); !ok {
				t.Fatalf("fill user%d project%d refused", user, project)
			}
		}
	}
	if ok, wait := s.take(context.Background(), "user0", "ws", "overflow"); ok || wait != time.Minute {
		t.Fatalf("global audience overflow allowed%t wait%s; want false/1m", ok, wait)
	}
	if ok, _ := s.take(context.Background(), "user0", "ws", "project0"); !ok {
		t.Fatal("global capacity blocked existing audience")
	}
}

func TestSharedSearchGlobalCleanupAndNoDeniedCharge(t *testing.T) {
	s := newSharedSearchLimiter()
	s.maxAccounts, s.maxAudiences, s.maxTotal = 3, 3, 2
	now := time.Now()
	s.now = func() time.Time { return now }
	check := func() {
		t.Helper()
		total := 0
		for _, a := range s.accounts {
			total += len(a.audiences)
		}
		if total != s.total || total > s.maxTotal {
			t.Fatalf("counter%d actual%d max%d", s.total, total, s.maxTotal)
		}
	}
	take := func(user, project string, want bool) {
		t.Helper()
		ok, _ := s.take(t.Context(), user, "ws", project)
		if ok != want {
			t.Fatalf("%s/%s allowed%t want%t", user, project, ok, want)
		}
		check()
	}
	take("a", "p1", true)
	take("b", "p2", true)
	a := s.accounts[sharedSearchIdentity("a", "ws")]
	before := a.aggregate.TokensAt(now)
	take("a", "p3", false)
	take("c", "p3", false)
	if len(s.accounts) != 2 || a.aggregate.TokensAt(now) != before {
		t.Fatal("capacity denial allocated account or spent aggregate")
	}
	take("a", "p1", true)
	now = now.Add(4 * time.Minute)
	take("b", "p2", true)
	now = now.Add(time.Minute)
	take("b", "p3", true) // Expired account a releases its audience.
	now = now.Add(time.Minute)
	take("b", "p2", true)
	now = now.Add(3 * time.Minute)
	take("b", "p4", false) // Recent denied request keeps the account active, not its old buckets.
	now = now.Add(2 * time.Minute)
	take("b", "p4", true) // Both old buckets expire inside active account b.
	if s.total != 1 {
		t.Fatal("partial account cleanup retained expired audiences")
	}
	now = now.Add(4 * time.Minute)
	take("b", "p5", true)
	now = now.Add(time.Minute)
	take("b", "p4", true) // Own bucket expiry updates counter exactly once.
	now = now.Add(5 * time.Minute)
	take("c", "p6", true)
	if len(s.accounts) != 1 || s.total != 1 {
		t.Fatal("whole-account cleanup did not release all audiences")
	}
}

func TestSharedSearchCompactionPreservesLiveBudget(t *testing.T) {
	s := newSharedSearchLimiter()
	s.maxTotal = 2
	now := time.Now()
	s.now = func() time.Time { return now }
	if ok, _ := s.take(t.Context(), "u", "ws", "expired"); !ok {
		t.Fatal("initial audience denied")
	}
	now = now.Add(5*time.Minute - time.Second)
	for i := 0; i < 5; i++ {
		if ok, _ := s.take(t.Context(), "u", "ws", "live"); !ok {
			t.Fatal("live burst denied")
		}
	}
	now = now.Add(time.Second)
	if ok, _ := s.take(t.Context(), "u", "ws", "new"); !ok {
		t.Fatal("expired capacity not reclaimed")
	}
	if ok, wait := s.take(t.Context(), "u", "ws", "live"); ok || wait != 2*time.Second {
		t.Fatalf("compaction reset live budget: allowed%t wait%s", ok, wait)
	}
	if s.total != 2 {
		t.Fatalf("counter%d want2", s.total)
	}
}
