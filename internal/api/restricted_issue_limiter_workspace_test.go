package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestSharedSearchWorkspaceCapacityLeavesGuestSearchAvailable(t *testing.T) {
	fixture := testutil.MigratedDB(t)
	token := seedSearchBenchmark(t, fixture.DB, 1, 1, 1)
	router, err := NewRouter(fixture.DB, "synthetic-workspace-isolation-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	search := func() int {
		req := httptest.NewRequest("GET", "/api/v1/workspaces/search-bench-ws/restricted-issues?q=common", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", "search-bench-ws")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := search(); got != 200 {
		t.Fatalf("Guest fixture calibration status %d", got)
	}
	for _, audiences := range []int{1, 15} {
		t.Run(fmt.Sprintf("audiences_per_account_%d", audiences), func(t *testing.T) {
			// A new limiter starts the experiment after the independent HTTP calibration.
			s := newSharedSearchLimiter()
			router.sharedSearch = s
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			refused := false
			for user := 0; user <= s.maxAccounts && !refused; user++ {
				for project := 0; project < audiences; project++ {
					if ok, _ := s.take(t.Context(), fmt.Sprintf("busy-user-%d", user), "busy-workspace", fmt.Sprintf("project-%d", project)); !ok {
						refused = true
						break
					}
				}
			}
			if !refused {
				t.Fatal("workspace capacity was not finite")
			}
			if got := search(); got != 200 {
				t.Fatalf("workspace A exhausted capacity; Guest workspace B search status %d, want 200", got)
			}
		})
	}
}

func TestSharedSearchSaturationDoesNotResetActiveBurst(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if ok, _ := s.take(t.Context(), "active", "busy", "p"); !ok {
			t.Fatal("initial burst denied")
		}
	}
	account := s.accounts[sharedSearchIdentity("active", "busy")]
	for i := 0; i <= s.maxAccounts; i++ {
		s.take(t.Context(), fmt.Sprintf("other-%d", i), "busy", "p")
	}
	if ok, _ := s.take(t.Context(), "active", "busy", "p"); ok {
		t.Fatal("capacity reset the active burst")
	}
	if s.accounts[sharedSearchIdentity("active", "busy")] != account {
		t.Fatal("active account was replaced")
	}
}

func BenchmarkSharedSearchSaturatedCapacity(b *testing.B) {
	for _, size := range []int{16, 256, 4096} {
		b.Run(fmt.Sprintf("accounts%d", size), func(b *testing.B) {
			s := newSharedSearchLimiter()
			s.maxAccounts = size
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return now }
			for i := 0; i < size; i++ {
				if ok, _ := s.take(context.Background(), fmt.Sprintf("u%d", i), fmt.Sprintf("ws%d", i/128), "p"); !ok {
					b.Fatal("fill failed")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if ok, _ := s.take(context.Background(), "overflow", "overflow-workspace", "p"); ok {
					b.Fatal("global capacity bypassed")
				}
			}
		})
	}
}

func TestSharedSearchWorkspaceQuotaBookkeepingAndIdleNodes(t *testing.T) {
	s := newSharedSearchLimiter()
	s.maxAccounts = 4
	s.maxTotal = 6
	s.maxWorkspaceAccounts = 2
	s.maxWorkspaceAudiences = 3
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	take := func(user, workspace, project string, want bool) {
		t.Helper()
		ok, _ := s.take(t.Context(), user, workspace, project)
		if ok != want {
			t.Fatalf("%s/%s/%s allowed %t want %t", user, workspace, project, ok, want)
		}
	}
	check := func() {
		t.Helper()
		accounts, audiences := 0, 0
		for _, ws := range s.workspaces {
			accounts += ws.accounts
			audiences += ws.audiences
			if ws.accounts > s.maxWorkspaceAccounts || ws.audiences > s.maxWorkspaceAudiences {
				t.Fatal("workspace quota exceeded")
			}
		}
		if accounts != len(s.accounts) || audiences != s.total || s.accountIdle.Len() != accounts || s.audienceIdle.Len() != audiences || accounts > s.maxAccounts || audiences > s.maxTotal || len(s.workspaces) > accounts {
			t.Fatal("counter or idle-node cardinality drift")
		}
		for _, a := range s.accounts {
			if a.highWater > 2*len(a.audiences) {
				t.Fatal("expired map retained disproportionate backing storage")
			}
		}
	}
	take("a", "A", "p1", true)
	take("a", "A", "p2", true)
	take("b", "A", "p1", true)
	before := s.accounts[sharedSearchIdentity("b", "A")].aggregate.TokensAt(now)
	take("b", "A", "p2", false)
	take("c", "A", "p1", false)
	if s.accounts[sharedSearchIdentity("b", "A")].aggregate.TokensAt(now) != before {
		t.Fatal("workspace capacity denial spent aggregate budget")
	}
	take("a", "B", "p1", true)
	take("b", "B", "p1", true)
	take("c", "C", "p1", false)
	check()
	active := s.accounts[sharedSearchIdentity("a", "A")]
	now = now.Add(sharedSearchIdle - time.Second)
	for i := 0; i < 5; i++ {
		take("a", "A", "p1", true)
	}
	now = now.Add(time.Second)
	take("c", "C", "p1", true)
	if len(s.accounts) != 2 || s.total != 2 || len(s.workspaces) != 2 || s.workspaces[sharedSearchIdentity("", "B")] != nil {
		t.Fatal("expired account/audience/workspace capacity not released")
	}
	if ok, wait := s.take(t.Context(), "a", "A", "p1"); ok || wait != 2*time.Second || s.accounts[sharedSearchIdentity("a", "A")] != active {
		t.Fatalf("expiry reset active budget: allowed %t wait %s", ok, wait)
	}
	take("fresh", "B", "p1", true) // Expiry returns the workspace quota itself.
	for i := 0; i < 200; i++ {
		now = now.Add(10 * time.Second)
		take("a", "A", "p1", true)
		s.take(t.Context(), fmt.Sprintf("churn-%d", i), "C", "p")
		check()
	}
	if s.accounts[sharedSearchIdentity("a", "A")] != active {
		t.Fatal("active account replaced during churn")
	}
}

func TestSharedSearchConcurrentWorkspaceCapacity(t *testing.T) {
	s := newSharedSearchLimiter()
	s.maxAccounts = 8
	s.maxWorkspaceAccounts = 4
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	var admitted [2]atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		for user := 0; user < 32; user++ {
			wg.Add(1)
			go func(w, user int) {
				defer wg.Done()
				if ok, _ := s.take(t.Context(), fmt.Sprintf("u%d", user), fmt.Sprintf("ws%d", w), "p"); ok {
					admitted[w].Add(1)
				}
			}(w, user)
		}
	}
	wg.Wait()
	if admitted[0].Load() != 4 || admitted[1].Load() != 4 || len(s.accounts) != 8 || s.accountIdle.Len() != 8 || s.audienceIdle.Len() != 8 {
		t.Fatalf("concurrent quotas: %d/%d", admitted[0].Load(), admitted[1].Load())
	}
}
