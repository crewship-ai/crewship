package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func fillSearchAccountBurst(t testing.TB, s *sharedSearchLimiter, user, workspace string) {
	t.Helper()
	for i := 0; i < 15; i++ {
		if ok, _ := s.take(context.Background(), user, workspace, fmt.Sprintf("initial-%d", i)); !ok {
			t.Fatalf("positive burst calibration denied request %d", i)
		}
	}
}

func TestSharedSearchRateDenialDoesNotRetainNewAudiences(t *testing.T) {
	s := newSharedSearchLimiter()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	fillSearchAccountBurst(t, s, "busy", "workspace")
	account := s.accounts[sharedSearchIdentity("busy", "workspace")]
	for i := 0; i < 300; i++ {
		if ok, _ := s.take(t.Context(), "busy", "workspace", fmt.Sprintf("denied-%d", i)); ok {
			t.Fatal("exhausted aggregate admitted a request")
		}
	}
	if len(account.audiences) != 15 || s.total != 15 || account.workspace.audiences != 15 || s.audienceIdle.Len() != 15 {
		t.Fatalf("denied requests retained audience capacity: account=%d total=%d workspace=%d idle=%d; want 15 each", len(account.audiences), s.total, account.workspace.audiences, s.audienceIdle.Len())
	}
	now = now.Add(3 * time.Second)
	if ok, _ := s.take(t.Context(), "busy", "workspace", "accepted-after-refill"); !ok {
		t.Fatal("denied reservations spent the refill budget")
	}
}

func TestSharedSearchEightDeniedFloodsLeaveSameWorkspaceGuestAvailable(t *testing.T) {
	fixture := testutil.MigratedDB(t)
	token := seedSearchBenchmark(t, fixture.DB, 1, 1, 1)
	router, err := NewRouter(fixture.DB, "synthetic-denied-search-secret-20261019", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	search := func() int {
		req := httptest.NewRequest("GET", "/api/v1/workspaces/search-bench-ws/restricted-issues?q=common", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr.Code
	}
	if status := search(); status != 200 {
		t.Fatalf("positive Guest router calibration HTTP%d", status)
	}
	s := newSharedSearchLimiter()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	router.sharedSearch = s
	// Populate authenticated limiter state directly; the final independent
	// Guest request still traverses the real router, grants and search query.
	for i := 0; i < 8; i++ {
		user := fmt.Sprintf("busy-%d", i)
		fillSearchAccountBurst(t, s, user, "search-bench-ws")
		for j := 0; j < 300; j++ {
			if ok, _ := s.take(t.Context(), user, "search-bench-ws", fmt.Sprintf("denied-%d", j)); ok {
				t.Fatal("denied flood exceeded aggregate burst")
			}
		}
	}
	if status := search(); status != 200 {
		t.Fatalf("eight rate-denied accounts blocked another Guest in the same workspace: HTTP%d", status)
	}
}

func BenchmarkSharedSearchDeniedNewAudience(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		s := newSharedSearchLimiter()
		now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
		s.now = func() time.Time { return now }
		fillSearchAccountBurst(b, s, "busy", "workspace")
		b.StartTimer()
		if ok, _ := s.take(context.Background(), "busy", "workspace", "new-audience"); ok {
			b.Fatal("exhausted account admitted a new audience")
		}
	}
}
