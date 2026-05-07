package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunRegistry_Acquire_NoConcurrencyKey(t *testing.T) {
	r := NewRunRegistry()
	ctx, release, err := r.Acquire(context.Background(), AcquireOpts{
		RunID:       "run_a",
		WorkspaceID: "ws_test",
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	if ctx.Err() != nil {
		t.Errorf("expected fresh ctx, got err %v", ctx.Err())
	}
	if got := r.Count("ws_test", ""); got != 1 {
		t.Errorf("expected count 1, got %d", got)
	}
}

func TestRunRegistry_Acquire_ConcurrencyLimit(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	_, release1, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_a",
		WorkspaceID:    "ws_test",
		ConcurrencyKey: "tenant_42",
		MaxConcurrent:  2,
	})
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	defer release1()

	_, release2, err := r.Acquire(ctx, AcquireOpts{
		RunID:          "run_b",
		WorkspaceID:    "ws_test",
		ConcurrencyKey: "tenant_42",
		MaxConcurrent:  2,
	})
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	defer release2()

	// Third should hit the limit
	_, _, err = r.Acquire(ctx, AcquireOpts{
		RunID:          "run_c",
		WorkspaceID:    "ws_test",
		ConcurrencyKey: "tenant_42",
		MaxConcurrent:  2,
	})
	if !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Errorf("expected ErrConcurrencyLimitReached, got %v", err)
	}
}

func TestRunRegistry_Acquire_DifferentKeysIndependent(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	for i, key := range []string{"tenant_a", "tenant_b", "tenant_c"} {
		_, release, err := r.Acquire(ctx, AcquireOpts{
			RunID:          "run_" + string(rune('a'+i)),
			WorkspaceID:    "ws_test",
			ConcurrencyKey: key,
			MaxConcurrent:  1,
		})
		if err != nil {
			t.Fatalf("acquire %s: %v", key, err)
		}
		defer release()
	}
}

func TestRunRegistry_Acquire_DefaultMaxOne(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	_, release, err := r.Acquire(ctx, AcquireOpts{
		RunID: "run_a", WorkspaceID: "ws_test", ConcurrencyKey: "k",
		// MaxConcurrent omitted → must default to 1 when key is set
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	defer release()
	_, _, err = r.Acquire(ctx, AcquireOpts{
		RunID: "run_b", WorkspaceID: "ws_test", ConcurrencyKey: "k",
	})
	if !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Errorf("expected limit on second run with default max=1, got %v", err)
	}
}

func TestRunRegistry_Cancel_TripsContext(t *testing.T) {
	r := NewRunRegistry()
	ctx, release, err := r.Acquire(context.Background(), AcquireOpts{
		RunID: "run_x", WorkspaceID: "ws_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := r.Cancel("run_x"); err != nil {
		t.Errorf("cancel: %v", err)
	}
	select {
	case <-ctx.Done():
		// good
	case <-time.After(time.Second):
		t.Errorf("ctx.Done() did not fire after Cancel")
	}
	if !r.IsCancelRequested("run_x") {
		t.Errorf("expected cancel_requested=true after Cancel")
	}
}

func TestRunRegistry_Cancel_UnknownRun(t *testing.T) {
	r := NewRunRegistry()
	if err := r.Cancel("nonexistent"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("expected ErrRunNotFound, got %v", err)
	}
}

func TestRunRegistry_Release_FreesSlot(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	_, release, err := r.Acquire(ctx, AcquireOpts{
		RunID: "run_a", WorkspaceID: "ws_test", ConcurrencyKey: "k", MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Acquire 2 should hit limit
	_, _, err = r.Acquire(ctx, AcquireOpts{
		RunID: "run_b", WorkspaceID: "ws_test", ConcurrencyKey: "k", MaxConcurrent: 1,
	})
	if !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Fatalf("expected limit pre-release, got %v", err)
	}

	release()

	// After release, a new acquire should succeed
	_, release2, err := r.Acquire(ctx, AcquireOpts{
		RunID: "run_c", WorkspaceID: "ws_test", ConcurrencyKey: "k", MaxConcurrent: 1,
	})
	if err != nil {
		t.Errorf("acquire after release should succeed, got %v", err)
	}
	defer release2()
}

func TestRunRegistry_Active_ScopedByWorkspace(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	_, rel1, _ := r.Acquire(ctx, AcquireOpts{RunID: "a", WorkspaceID: "ws_1"})
	_, rel2, _ := r.Acquire(ctx, AcquireOpts{RunID: "b", WorkspaceID: "ws_2"})
	defer rel1()
	defer rel2()

	got := r.Active("ws_1")
	if len(got) != 1 || got[0].RunID != "a" {
		t.Errorf("expected only ws_1 run, got %+v", got)
	}
	all := r.Active("")
	if len(all) != 2 {
		t.Errorf("expected 2 across workspaces, got %d", len(all))
	}
}

func TestRunRegistry_Concurrent_NoDoubleAcquire(t *testing.T) {
	r := NewRunRegistry()
	ctx := context.Background()
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	successCount := 0
	var mu sync.Mutex
	releases := make([]func(), 0, N)

	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			_, release, err := r.Acquire(ctx, AcquireOpts{
				RunID:          "run_" + intToString(idx),
				WorkspaceID:    "ws_test",
				ConcurrencyKey: "shared",
				MaxConcurrent:  3,
			})
			if err == nil {
				mu.Lock()
				successCount++
				releases = append(releases, release)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	for _, r := range releases {
		r()
	}
	if successCount != 3 {
		t.Errorf("expected exactly 3 successful acquires under max=3, got %d", successCount)
	}
}

// intToString avoids fmt.Sprintf to keep the test allocation-light
// when the concurrent benchmark scales.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
