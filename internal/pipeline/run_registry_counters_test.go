package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// keyCountsLen reads the admission-counter map size under the
// registry lock. A leaked counter permanently blocks a key, so the
// tests below assert the map drains to empty — not just that Count()
// reports zero.
func keyCountsLen(r *RunRegistry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keyCounts)
}

func keyCount(r *RunRegistry, workspaceID, concurrencyKey string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keyCounts[concurrencyCountKey(workspaceID, concurrencyKey)]
}

// TestRunRegistry_KeyCounts_DrainToEmpty hammers Acquire/Release from
// many goroutines across many distinct keys and asserts the counter
// map is fully drained afterwards. Any release path that forgets to
// decrement leaves a residual entry here.
func TestRunRegistry_KeyCounts_DrainToEmpty(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()

	const workers = 32
	const perWorker = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				opts := AcquireOpts{
					RunID:          fmt.Sprintf("run_%d_%d", w, i),
					WorkspaceID:    fmt.Sprintf("ws_%d", w%4),
					ConcurrencyKey: fmt.Sprintf("key_%d", i%8),
					MaxConcurrent:  4,
				}
				_, release, err := r.Acquire(ctx, opts)
				if err != nil {
					if !errors.Is(err, ErrConcurrencyLimitReached) {
						t.Errorf("unexpected acquire error: %v", err)
					}
					// Refused acquires must not have reserved a slot;
					// calling the returned no-op release is still safe.
					release()
					continue
				}
				// Double release must not double-decrement.
				release()
				release()
			}
		}(w)
	}
	wg.Wait()

	if got := len(r.Active("")); got != 0 {
		t.Errorf("expected no live runs, got %d", got)
	}
	if got := keyCountsLen(r); got != 0 {
		t.Errorf("expected drained counter map, got %d entries", got)
	}
}

// TestRunRegistry_KeyCounts_LimitBoundary pins the admission edge: the
// Nth acquire succeeds, the N+1th is refused, and exactly one release
// re-opens exactly one slot.
func TestRunRegistry_KeyCounts_LimitBoundary(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	const max = 3

	releases := make([]func(), 0, max)
	for i := 0; i < max; i++ {
		_, release, err := r.Acquire(ctx, AcquireOpts{
			RunID:          fmt.Sprintf("run_%d", i),
			WorkspaceID:    "ws_a",
			ConcurrencyKey: "k",
			MaxConcurrent:  max,
		})
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	if got := keyCount(r, "ws_a", "k"); got != max {
		t.Fatalf("expected counter %d, got %d", max, got)
	}

	// N+1 is refused.
	if _, _, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_overflow",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  max,
	}); !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Fatalf("expected ErrConcurrencyLimitReached, got %v", err)
	}
	// A refused acquire must not have bumped the counter.
	if got := keyCount(r, "ws_a", "k"); got != max {
		t.Fatalf("refused acquire mutated counter: %d", got)
	}

	// Same workspace, different key is unaffected.
	_, releaseOther, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_other_key",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k2",
		MaxConcurrent:  1,
	})
	if err != nil {
		t.Fatalf("acquire other key: %v", err)
	}
	// Same key, different workspace is unaffected too.
	_, releaseOtherWS, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_other_ws",
		WorkspaceID:    "ws_b",
		ConcurrencyKey: "k",
		MaxConcurrent:  1,
	})
	if err != nil {
		t.Fatalf("acquire other workspace: %v", err)
	}

	// One release re-opens exactly one slot.
	releases[0]()
	_, releaseRefill, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_refill",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  max,
	})
	if err != nil {
		t.Fatalf("expected the freed slot to be reusable, got %v", err)
	}
	if _, _, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_overflow2",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  max,
	}); !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Fatalf("expected the key to be full again, got %v", err)
	}

	releaseRefill()
	releases[1]()
	releases[2]()
	releaseOther()
	releaseOtherWS()

	if got := keyCountsLen(r); got != 0 {
		t.Errorf("expected drained counter map, got %d entries", got)
	}
}

// TestRunRegistry_KeyCounts_DuplicateRunID guards the counter against
// the rejected-duplicate path: a refused Acquire must leave both the
// run map and the counter untouched.
func TestRunRegistry_KeyCounts_DuplicateRunID(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()

	_, release, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_dup",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  5,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, _, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_dup",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  5,
	}); !errors.Is(err, ErrDuplicateRunID) {
		t.Fatalf("expected ErrDuplicateRunID, got %v", err)
	}
	if got := keyCount(r, "ws_a", "k"); got != 1 {
		t.Fatalf("duplicate acquire mutated counter: %d", got)
	}
	release()
	if got := keyCountsLen(r); got != 0 {
		t.Errorf("expected drained counter map, got %d entries", got)
	}
}

// TestRunRegistry_KeyCounts_StaleReleaseAfterReuse covers the case
// where a run id is released and then re-acquired: the first
// release's closure must not evict (and decrement for) the new run.
func TestRunRegistry_KeyCounts_StaleReleaseAfterReuse(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	opts := AcquireOpts{
		RunID:          "run_reused",
		WorkspaceID:    "ws_a",
		ConcurrencyKey: "k",
		MaxConcurrent:  2,
	}

	_, release1, err := r.Acquire(ctx, opts)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	release1()

	runCtx2, release2, err := r.Acquire(ctx, opts)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	release1() // stale: must be a no-op

	if !r.IsLive("run_reused") {
		t.Fatal("stale release evicted the live run")
	}
	if runCtx2.Err() != nil {
		t.Fatalf("stale release cancelled the live run: %v", runCtx2.Err())
	}
	if got := keyCount(r, "ws_a", "k"); got != 1 {
		t.Fatalf("stale release corrupted counter: %d", got)
	}

	release2()
	if got := keyCountsLen(r); got != 0 {
		t.Errorf("expected drained counter map, got %d entries", got)
	}
}

// TestRunRegistry_KeyCounts_EmptyKeyUntracked keeps ungated runs out
// of the counter map — they are tracked for cancel + Active() only.
func TestRunRegistry_KeyCounts_EmptyKeyUntracked(t *testing.T) {
	r := NewRunRegistry()
	_, release, err := r.Acquire(context.Background(), AcquireOpts{
		RunID:       "run_ungated",
		WorkspaceID: "ws_a",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := keyCountsLen(r); got != 0 {
		t.Errorf("ungated run entered the counter map: %d entries", got)
	}
	// Count with an empty key still means "all runs in the workspace".
	if got := r.Count("ws_a", ""); got != 1 {
		t.Errorf("expected count 1, got %d", got)
	}
	release()
}
