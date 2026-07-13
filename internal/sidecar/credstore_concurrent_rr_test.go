package sidecar

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestCredStoreSelectConcurrentRoundRobinEven locks the #1081 hot-path change:
// Select must round-robin the top priority tier using an atomic counter under a
// READ lock, so N concurrent Selects neither race (run with -race) nor lose an
// increment. With an atomic counter over 3 equal-priority creds and a multiple
// of 3 total calls, the distribution is EXACTLY even — a non-atomic counter
// would drop increments and skew it.
func TestCredStoreSelectConcurrentRoundRobinEven(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "t1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "t2"},
		{ID: "c3", Provider: ProviderAnthropic, Token: "t3"},
	})

	const n = 3000
	var counts sync.Map // id -> *int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := cs.Select(ProviderAnthropic)
			if c == nil {
				t.Errorf("Select returned nil")
				return
			}
			v, _ := counts.LoadOrStore(c.ID, new(int64))
			atomic.AddInt64(v.(*int64), 1)
		}()
	}
	wg.Wait()

	got := map[string]int64{}
	counts.Range(func(k, v any) bool {
		got[k.(string)] = atomic.LoadInt64(v.(*int64))
		return true
	})
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct creds selected, got %d (%v)", len(got), got)
	}
	for id, c := range got {
		if c != n/3 {
			t.Errorf("cred %s selected %d times, want exactly %d (even round-robin)", id, c, n/3)
		}
	}
}
