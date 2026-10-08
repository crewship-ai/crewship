package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// This uses the production executor and concurrency registry, without an agent
// or provider. Capacity rejection must not consume an accepted deferred start.
func TestPendingDispatcher_CapacityRetainsThreeAcceptedStarts(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	registry := NewRunRegistry()
	executor := NewExecutor(store, NewResolver(db), nil, nil).
		WithIdempotencyStore(NewIdempotencyStore(db)).WithRunRegistry(registry)
	in := validSaveInput("deferred-contended")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"deferred-contended","agentless":true,"concurrency_key":"shared","max_concurrent":1,"steps":[{"id":"result","type":"transform","transform":{"input":"{{ inputs.value }}","expression":"."}}]}`
	p, err := store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := registry.Acquire(t.Context(), AcquireOpts{
		RunID: "holder", WorkspaceID: "ws_test", PipelineID: p.ID,
		ConcurrencyKey: "shared", MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pending := NewPendingRunStore(newPendingDB(t))
	recording := &recordPendingExecutor{executor: executor}
	d := NewPendingRunDispatcher(pending, recording, nil)
	expires := time.Now().Add(time.Hour)
	for _, id := range []string{"accepted-a", "accepted-b", "accepted-c"} {
		_, _, err := pending.Enqueue(t.Context(), PendingRun{
			ID: id, WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug,
			InputsJSON: `{"value":7}`, FireAt: time.Now().Add(-time.Minute), ExpiresAt: &expires,
		})
		if err != nil {
			t.Fatal(err)
		}
		d.fireOne(t.Context(), PendingRun{ID: id})
	}
	rows, err := pending.ListPending(t.Context(), "ws_test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("accepted starts disappeared on capacity rejection: pending=%d, want 3", len(rows))
	}
	// Backoff is eligibility, not a sleeping worker. Move the clock to release
	// it and run all three through the real executor after freeing capacity.
	if due, err := pending.DueRuns(t.Context(), time.Now(), 10); err != nil || len(due) != 0 {
		t.Fatalf("backoff ignored: due=%d err=%v", len(due), err)
	}
	release()
	later := time.Now().Add(time.Minute)
	d.now = func() time.Time { return later }
	for _, row := range rows {
		d.fireOne(t.Context(), row)
		pr, err := pending.Get(t.Context(), "ws_test", row.ID)
		if err != nil || pr == nil || pr.Status != "fired" || pr.FiredRunID == "" || pr.DispatchAttempts != 2 {
			t.Fatalf("accepted start not dispatched after release: %+v err=%v", pr, err)
		}
	}
	if len(recording.results) != 3 {
		t.Fatalf("completed results=%d, want 3", len(recording.results))
	}
	for i, result := range recording.results {
		if result.Status != "COMPLETED" || result.StepOutputs["result"] != "7" {
			t.Fatalf("wrong routine result: %+v", result)
		}
		if recording.inputs[i].IdempotencyKey != recording.inputs[i+3].IdempotencyKey {
			t.Fatal("retry changed accepted occurrence identity")
		}
	}
}

type rejectedPendingExecutor struct{ err error }

func (e rejectedPendingExecutor) Run(context.Context, RunInput) (*RunResult, error) {
	return nil, e.err
}

func TestPendingDispatcher_PermanentErrorIsVisibleFailure(t *testing.T) {
	s := enqueueDue(t, 1)
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{errors.New("pinned version unavailable")}, nil)
	d.fireOne(t.Context(), PendingRun{ID: "pa"})
	var status string
	if err := s.db.QueryRowContext(t.Context(), `SELECT status FROM pending_runs WHERE id='pa'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status=%q, want visible failed receipt", status)
	}
}

func TestPendingDispatcher_CapacityExpiresAtOriginalTTL(t *testing.T) {
	s := enqueueDue(t, 1)
	expires := time.Now().Add(time.Minute)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE pending_runs SET expires_at=?`, formatRFC3339(expires)); err != nil {
		t.Fatal(err)
	}
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{ErrConcurrencyLimitReached}, nil)
	d.fireOne(t.Context(), PendingRun{ID: "pa"})
	if _, err := s.ExpireDue(t.Context(), expires.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.db.QueryRowContext(t.Context(), `SELECT status FROM pending_runs WHERE id='pa'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("status=%q, want expired after permanent capacity collision", status)
	}
	pr, err := s.Get(t.Context(), "w", "pa")
	if err != nil || pr.LastError == "" || pr.NextAttemptAt != nil {
		t.Fatalf("expiry lacks visible reason: %+v %v", pr, err)
	}
}

// Wraps the actual executor without fabricating its results.
type recordPendingExecutor struct {
	executor runExecutor
	inputs   []RunInput
	results  []*RunResult
}

func (e *recordPendingExecutor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	e.inputs = append(e.inputs, in)
	res, err := e.executor.Run(ctx, in)
	if res != nil {
		e.results = append(e.results, res)
	}
	return res, err
}

func TestPendingDispatcher_NoTTLHasFiniteCapacityAttempts(t *testing.T) {
	s := enqueueDue(t, 1)
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{ErrConcurrencyLimitReached}, nil)
	now := time.Now()
	d.now = func() time.Time { return now }
	for i := 0; i < maxPendingAttemptsWithoutTTL; i++ {
		d.fireOne(t.Context(), PendingRun{ID: "pa"})
		now = now.Add(2 * time.Minute)
	}
	pr, err := s.Get(t.Context(), "w", "pa")
	if err != nil || pr.Status != "failed" || pr.DispatchAttempts != maxPendingAttemptsWithoutTTL || pr.LastError == "" || pr.NextAttemptAt != nil {
		t.Fatalf("unbounded capacity retry: %+v err=%v", pr, err)
	}
}

func TestPendingDispatcher_RetryKeepsPinAndSeparateDebouncePayload(t *testing.T) {
	s := NewPendingRunStore(newPendingDB(t))
	now := time.Now()
	pin := 3
	expires := now.Add(time.Hour)
	_, _, err := s.Enqueue(t.Context(), PendingRun{ID: "old", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", FireAt: now.Add(-time.Minute), ExpiresAt: &expires, DebounceKey: "key", PinnedVersion: &pin, InputsJSON: `{"value":1}`, Priority: 9})
	if err != nil {
		t.Fatal(err)
	}
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{ErrConcurrencyLimitReached}, nil)
	d.fireOne(t.Context(), PendingRun{ID: "old"})
	id, merged, err := s.Enqueue(t.Context(), PendingRun{ID: "new", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", FireAt: now.Add(time.Minute), DebounceKey: "key", InputsJSON: `{"value":2}`})
	if err != nil || merged || id != "new" {
		t.Fatalf("new trigger merged into attempted start: %s %v %v", id, merged, err)
	}
	pr, err := s.Get(t.Context(), "w", "old")
	if err != nil || pr.Status != "pending" || pr.LastError == "" || pr.NextAttemptAt == nil || pr.PinnedVersion == nil || *pr.PinnedVersion != 3 || pr.InputsJSON != `{"value":1}` || pr.Priority != 9 || !pr.ExpiresAt.Equal(expires) {
		t.Fatalf("retry changed accepted payload: %+v err=%v", pr, err)
	}
	if ok, err := s.Cancel(t.Context(), "w", "old"); err != nil || !ok {
		t.Fatalf("retry not cancellable: %v %v", ok, err)
	}
	d.now = func() time.Time { return now.Add(2 * time.Hour) }
	d.fireOne(t.Context(), PendingRun{ID: "old"})
	pr, _ = s.Get(t.Context(), "w", "old")
	if pr.Status != "cancelled" || pr.DispatchAttempts != 1 {
		t.Fatalf("cancelled retry was claimed: %+v", pr)
	}
}

func TestPendingDispatch_OldClaimCannotOverwriteRearmedOccurrence(t *testing.T) {
	s := enqueueDue(t, 1)
	old, err := s.ClaimDue(t.Context(), "pa", time.Now())
	if err != nil || old == nil {
		t.Fatalf("claim: %v %v", old, err)
	}
	// Model the one-time author's new occurrence, then claim its first attempt.
	at := time.Now().Add(-time.Second)
	_, err = s.db.ExecContext(t.Context(), `UPDATE pending_runs SET status='pending',fire_at=?,dispatch_attempts=0 WHERE id='pa'`, formatRFC3339(at))
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.ClaimDue(t.Context(), "pa", time.Now())
	if err != nil || current == nil {
		t.Fatalf("claim: %v %v", current, err)
	}
	if err := s.SetFiredRunID(t.Context(), *old, "stale-run"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDispatchError(t.Context(), *old, "failed", "stale", nil); err != nil {
		t.Fatal(err)
	}
	pr, _ := s.Get(t.Context(), "w", "pa")
	if pr.Status != "fired" || pr.FiredRunID != "" || pr.LastError != "" {
		t.Fatalf("late completion overwrote new occurrence: %+v", pr)
	}
}

func TestPendingDispatchFailureWriteWaitsOutsideQuietWindow(t *testing.T) {
	s := enqueueDue(t, 1)
	pr, err := s.ClaimDue(t.Context(), "pa", time.Now())
	if err != nil || pr == nil {
		t.Fatalf("claim: %v %v", pr, err)
	}
	win, err := quickBegin(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer win.Release()
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{ErrConcurrencyLimitReached}, nil)
	done := make(chan struct{})
	var started atomic.Bool
	go func() {
		started.Store(true)
		d.recordDispatchError(t.Context(), *pr, ErrConcurrencyLimitReached)
		close(done)
	}()
	if !waitFor(t, time.Second, started.Load) {
		t.Fatal("writer not started")
	}
	select {
	case <-done:
		t.Fatal("failure write crossed held quiet window")
	case <-time.After(20 * time.Millisecond):
	}
	row, _ := s.Get(t.Context(), "w", "pa")
	if row.Status != "fired" {
		t.Fatalf("write occurred while held: %+v", row)
	}
	win.Release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("write did not resume after release")
	}
	row, _ = s.Get(t.Context(), "w", "pa")
	if row.Status != "pending" {
		t.Fatalf("retry missing after release: %+v", row)
	}
}

func TestPendingDispatcher_BackoffFreesWorkerForOtherStarts(t *testing.T) {
	s := enqueueDue(t, 2)
	d := NewPendingRunDispatcher(s, rejectedPendingExecutor{ErrConcurrencyLimitReached}, nil)
	d.maxConcurrency = 1
	d.sem = make(chan struct{}, 1)
	d.sweep(t.Context())
	d.wg.Wait()
	for _, id := range []string{"pa", "pb"} {
		pr, err := s.Get(t.Context(), "w", id)
		if err != nil || pr.Status != "pending" || pr.DispatchAttempts != 1 {
			t.Fatalf("worker retained across backoff: %+v %v", pr, err)
		}
	}
	if len(d.sem) != 0 {
		t.Fatal("backoff holds worker slot")
	}
}

func TestPendingDispatcher_ReservationCleanupFailureIsNotRetried(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	registry := NewRunRegistry()
	executor := NewExecutor(store, NewResolver(db), nil, nil).
		WithIdempotencyStore(NewIdempotencyStore(db)).WithRunRegistry(registry)
	in := validSaveInput("cleanup-failure")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"cleanup-failure","agentless":true,"concurrency_key":"shared","max_concurrent":1,"steps":[{"id":"result","type":"transform","transform":{"expression":"."}}]}`
	p, err := store.Save(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_reservation_delete BEFORE DELETE ON pipeline_run_idempotency BEGIN SELECT RAISE(FAIL,'delete unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	_, release, err := registry.Acquire(t.Context(), AcquireOpts{RunID: "holder", WorkspaceID: "ws_test", PipelineID: p.ID, ConcurrencyKey: "shared", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	s := NewPendingRunStore(newPendingDB(t))
	if _, _, err := s.Enqueue(t.Context(), PendingRun{ID: "receipt", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug, FireAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	d := NewPendingRunDispatcher(s, executor, nil)
	d.fireOne(t.Context(), PendingRun{ID: "receipt"})
	pr, err := s.Get(t.Context(), "ws_test", "receipt")
	if err != nil || pr.Status != "failed" || pr.NextAttemptAt != nil || pr.FiredRunID != "" || pr.LastError == "" {
		t.Fatalf("reservation failure retried into phantom dedupe: %+v %v", pr, err)
	}
}

func TestPendingDispatcher_MissingPinnedVersionIsVisibleFailure(t *testing.T) {
	db := openVersioningTestDB(t)
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("missing-pin"))
	if err != nil {
		t.Fatal(err)
	}
	s := NewPendingRunStore(newPendingDB(t))
	if _, _, err := s.Enqueue(t.Context(), PendingRun{ID: "receipt", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug, FireAt: time.Now().Add(-time.Minute), PinnedVersion: intPtr(999)}); err != nil {
		t.Fatal(err)
	}
	d := NewPendingRunDispatcher(s, NewExecutor(store, NewResolver(db), nil, nil), nil)
	d.fireOne(t.Context(), PendingRun{ID: "receipt"})
	pr, err := s.Get(t.Context(), "ws_test", "receipt")
	if err != nil || pr.Status != "failed" || pr.LastError != "The accepted recipe version is no longer available." || pr.FiredRunID != "" || pr.NextAttemptAt != nil {
		t.Fatalf("missing version disappeared: %+v %v", pr, err)
	}
}
