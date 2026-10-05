package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Regression #2903: use a real persisted, top-level run, not runWaitStep
// directly (the blocking/no-store path already honored the timeout).
func TestEventWaitDeadline_PersistedTimeout(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	hooks := &recordingCodeRunner{}
	deps.CodeRunner = hooks
	exec := NewWiredExecutor(deps)
	stop := StartEventWaitSweeper(context.Background(), db, exec, nil, nil, 10*time.Millisecond)
	defer stop()
	p := saveResumePipeline(t, deps.Store, "event-timeout", `{"dsl_version":"1.0","name":"event-timeout","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"},"timeout_seconds":1,"on_fail":"abort"}],"hooks":{"on_failure":{"id":"of","type":"code","code":{"runtime":"cel","code":"true"}}}}`)
	res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: result=%+v err=%v", res, err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rec, err := deps.RunStore.Get(context.Background(), res.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status == RunStatusFailed && atomic.LoadInt32(&hooks.calls) == 1 {
			if !strings.Contains(rec.ErrorMessage, "timed out") || rec.FailedAtStep != "gate" {
				t.Fatalf("wrong failure: %+v", rec)
			}
			if n := atomic.LoadInt32(&hooks.calls); n != 1 {
				t.Fatalf("on_failure hook ran %d times, want 1", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := deps.RunStore.Get(context.Background(), res.RunID)
	t.Fatalf("persisted top-level event wait ignored timeout: status=%s error=%q on_failure_calls=%d", rec.Status, rec.ErrorMessage, atomic.LoadInt32(&hooks.calls))
}

func TestEventWaitDeadline_OfflineExpiryDoesNotRenew(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	original := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-offline", strings.ReplaceAll(eventWaitDSL, "3600", "1"))
	res, err := original.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	// Nothing processes waits during downtime.
	time.Sleep(1100 * time.Millisecond)
	restarted := NewWiredExecutor(deps)
	rec, err := deps.RunStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	plan, reason := restarted.buildResumePlan(context.Background(), rec)
	if plan == nil {
		t.Fatal(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	restarted.runResumedRun(ctx, plan, slog.New(slog.NewTextHandler(io.Discard, nil)))
	final, err := deps.RunStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != RunStatusFailed || !strings.Contains(final.ErrorMessage, "timed out") {
		t.Fatalf("offline deadline renewed: status=%s error=%q", final.Status, final.ErrorMessage)
	}
}

// Boot through a fresh executor on a reopened, fully migrated on-disk DB.
// Exercise pending recovery and delivered-before-deadline precedence.
func TestEventWaitDeadline_ReopenAndBoot(t *testing.T) {
	for _, delivery := range []string{"pending", "delivered", "consumed"} {
		t.Run(delivery, func(t *testing.T) {
			db := testutil.MigratedDB(t)
			path := db.Path()
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws_test','Test','test')`); err != nil {
				t.Fatal(err)
			}
			store := NewStore(db.DB)
			in := validSaveInput("deadline-reopen")
			in.Author = AuthorMeta{Via: AuthoredViaUser}
			in.DefinitionJSON = strings.ReplaceAll(eventWaitDSL, "3600", "1")
			p, err := store.Save(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			makeExec := func(sqlDB *sql.DB) *Executor {
				return NewExecutor(NewStore(sqlDB), NewResolver(sqlDB), newMockRunner(), nil).
					WithRunStore(NewRunStore(sqlDB)).WithRunRegistry(NewRunRegistry()).
					WithSignalRegistry(NewSignalRegistry()).WithSignalWaitStore(NewSQLSignalWaitStore(sqlDB))
			}
			exec := makeExec(db.DB)
			res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil || res.Status != "WAITING" {
				t.Fatalf("park: %+v %v", res, err)
			}
			if delivery != "pending" {
				if ok, err := NewSQLSignalWaitStore(db.DB).Deliver(context.Background(), res.RunID, "approve", "payload"); err != nil || !ok {
					t.Fatalf("deliver: %v %v", ok, err)
				}
			}
			if delivery == "consumed" {
				if _, ok, err := NewSQLSignalWaitStore(db.DB).ConsumeDelivered(context.Background(), res.RunID, "gate"); err != nil || !ok {
					t.Fatalf("consume crash window: %v %v", ok, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1100 * time.Millisecond)
			reopened, err := database.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restarted := makeExec(reopened.DB)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n, interrupted, err := restarted.ResumeInterruptedRuns(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil || n != 1 || interrupted != 0 {
				t.Fatalf("boot: resumed=%d interrupted=%d err=%v", n, interrupted, err)
			}
			want := RunStatusFailed
			if delivery != "pending" {
				want = RunStatusCompleted
			}
			final := waitForRunStatus(t, restarted.runStore, res.RunID, want, time.Second)
			if delivery != "pending" {
				out, err := restarted.runStore.GetStepOutputs(ctx, res.RunID)
				if err != nil || out["gate"] != "payload" {
					t.Fatalf("delivered payload lost: %v %v", out, err)
				}
			} else if !strings.Contains(final.ErrorMessage, "timed out") {
				t.Fatalf("wrong failure: %+v", final)
			}
		})
	}
}

func TestEventWaitDeadline_ReparkKeepsOriginalDeadline(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-repark", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	var original string
	if err := db.QueryRow(`SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=?`, res.RunID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rec, err := deps.RunStore.Get(ctx, res.RunID)
		if err != nil {
			t.Fatal(err)
		}
		plan, reason := exec.buildResumePlan(ctx, rec)
		if plan == nil {
			t.Fatal(reason)
		}
		exec.runResumedRun(ctx, plan, slog.New(slog.NewTextHandler(io.Discard, nil)))
		rec, err = deps.RunStore.Get(ctx, res.RunID)
		if err != nil || rec.Status != RunStatusWaiting || deps.Runs.IsLive(res.RunID) {
			t.Fatalf("resume did not re-park: %+v %v", rec, err)
		}
	}
	var after string
	if err := db.QueryRow(`SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=?`, res.RunID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if original != after {
		t.Fatalf("deadline renewed: %s -> %s", original, after)
	}
}

func TestEventWaitDeadline_DuplicateResumeAndCancel(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint("cancelled=", cancelled), func(t *testing.T) {
			db := testutil.MigratedSQLDB(t)
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws_test','Test','test')`); err != nil {
				t.Fatal(err)
			}
			store := NewStore(db)
			in := validSaveInput("event-duplicate")
			in.Author = AuthorMeta{Via: AuthoredViaUser}
			in.DefinitionJSON = `{"dsl_version":"1.0","name":"event-duplicate","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"},"timeout_seconds":1,"on_fail":"abort"}],"hooks":{"on_failure":{"id":"of","type":"code","code":{"runtime":"cel","code":"true"}}}}`
			p, err := store.Save(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			hooks := &recordingCodeRunner{}
			exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).WithRunStore(NewRunStore(db)).WithRunRegistry(NewRunRegistry()).WithSignalRegistry(NewSignalRegistry()).WithSignalWaitStore(NewSQLSignalWaitStore(db)).WithCodeRunner(hooks)
			ctx := context.Background()
			res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil || res.Status != "WAITING" {
				t.Fatalf("park: %+v %v", res, err)
			}
			rec, err := exec.runStore.Get(ctx, res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			stalePlan, reason := exec.buildResumePlan(ctx, rec)
			if stalePlan == nil {
				t.Fatal(reason)
			}
			if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id=?`, tsformat.Format(time.Now().Add(-time.Minute)), res.RunID); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				if err := exec.runStore.MarkTerminal(ctx, MarkTerminalInput{RunID: res.RunID, Status: RunStatusCancelled}); err != nil {
					t.Fatal(err)
				}
				if status, _ := waitStatus(t, db, res.RunID); status != "cancelled" {
					t.Fatalf("subscription status=%s", status)
				}
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					exec.ResumeEventRun(ctx, res.RunID, slog.New(slog.NewTextHandler(io.Discard, nil)))
				}()
			}
			wg.Wait()
			// A resume queued before cancellation/completion must not replay hooks.
			exec.runResumedRun(ctx, stalePlan, slog.New(slog.NewTextHandler(io.Discard, nil)))
			final, err := exec.runStore.Get(ctx, res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			want, wantHooks := RunStatusFailed, int32(1)
			if cancelled {
				want, wantHooks = RunStatusCancelled, 0
			}
			if final.Status != want || atomic.LoadInt32(&hooks.calls) != wantHooks {
				t.Fatalf("status=%s hooks=%d want %s/%d", final.Status, atomic.LoadInt32(&hooks.calls), want, wantHooks)
			}
			if ok, err := exec.signalWaits.Deliver(ctx, res.RunID, "approve", "late"); ok || err != nil {
				t.Fatalf("terminal delivery=%v %v", ok, err)
			}
			if ids, err := exec.signalWaits.DeliverTopic(ctx, "ws_test", "approve", "late"); len(ids) != 0 || err != nil {
				t.Fatalf("terminal topic delivery=%v %v", ids, err)
			}
		})
	}
}

type eventWaitGate struct{ enabled atomic.Bool }

func (g *eventWaitGate) IsLeader() bool { return g.enabled.Load() }

func TestEventWaitDeadline_SweeperLeaderAndStop(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-leader", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id=?`, tsformat.Format(time.Now().Add(-time.Minute)), res.RunID); err != nil {
		t.Fatal(err)
	}
	gate := &eventWaitGate{}
	stop := StartEventWaitSweeper(ctx, db, exec, gate, nil, 10*time.Millisecond)
	defer stop()
	time.Sleep(30 * time.Millisecond)
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting {
		t.Fatalf("nonleader resumed: %+v %v", rec, err)
	}
	gate.enabled.Store(true)
	waitForRunStatus(t, deps.RunStore, res.RunID, RunStatusFailed, time.Second)
	stop()
	stop() // join is idempotent
}

func TestEventWaitDeadline_StopJoinsResumeWaitingForRelease(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-stop", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := exec.signalWaits.Deliver(ctx, res.RunID, "approve", "payload"); err != nil || !ok {
		t.Fatalf("deliver=%v %v", ok, err)
	}
	_, release, err := deps.Runs.Acquire(ctx, AcquireOpts{RunID: res.RunID, WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stop := StartEventWaitSweeper(ctx, db, exec, nil, nil, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not join release-wait worker")
	}
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting {
		t.Fatalf("shutdown changed parked run: %+v %v", rec, err)
	}
	if status, _ := waitStatus(t, db, res.RunID); status != "delivered" {
		t.Fatalf("shutdown lost delivered signal: %s", status)
	}
}

func TestEventWaitDeadline_TimeoutUsesRetryStepPolicy(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	var sleeps atomic.Int32
	exec.sleepFn = func(context.Context, time.Duration) bool { sleeps.Add(1); return true }
	p := saveResumePipeline(t, deps.Store, "event-retry", `{"dsl_version":"1.0","name":"event-retry","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"},"timeout_seconds":1,"on_fail":"retry_step"}]}`)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	if sleeps.Load() != 0 {
		t.Fatal("suspension was retried")
	}
	var before string
	if err := db.QueryRow(`SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=?`, res.RunID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Timeout has elapsed during downtime. Normal resume must pass the error
	// through retry_step's three attempts without re-arming another interval.
	if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=? WHERE run_id=?`, tsformat.Format(time.Now().Add(-time.Minute)), res.RunID); err != nil {
		t.Fatal(err)
	}
	exec.ResumeEventRun(ctx, res.RunID, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusFailed || !strings.Contains(rec.ErrorMessage, "timed out") {
		t.Fatalf("timeout: %+v %v", rec, err)
	}
	if sleeps.Load() != 2 {
		t.Fatalf("retry_step sleeps=%d, want two retries", sleeps.Load())
	}
	var after string
	if err := db.QueryRow(`SELECT timeout_at FROM pipeline_signal_waits WHERE run_id=?`, res.RunID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("test failed to elapse deadline")
	}
	parsed, err := time.Parse(time.RFC3339Nano, after)
	if err != nil || !parsed.Before(time.Now()) {
		t.Fatalf("retry renewed timeout_at=%s err=%v", after, err)
	}
}

type eventWaitBlockingCodeRunner struct{ started chan struct{} }

func (r *eventWaitBlockingCodeRunner) RunCode(ctx context.Context, _ CodeRunRequest) (CodeRunResult, error) {
	close(r.started)
	<-ctx.Done()
	return CodeRunResult{}, ctx.Err()
}

func TestEventWaitDeadline_StopJoinsRunningResume(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	runner := &eventWaitBlockingCodeRunner{started: make(chan struct{})}
	deps.CodeRunner = runner
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-stop-running", `{"dsl_version":"1.0","name":"event-stop-running","steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"}},{"id":"after","type":"code","code":{"runtime":"cel","code":"true"}}]}`)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	if ok, err := exec.signalWaits.Deliver(ctx, res.RunID, "approve", "payload"); err != nil || !ok {
		t.Fatalf("deliver=%v %v", ok, err)
	}
	stop := StartEventWaitSweeper(ctx, db, exec, nil, nil, 10*time.Millisecond)
	defer stop()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("resume never entered downstream step")
	}
	stop()
	final, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || final.Status != RunStatusCancelled || deps.Runs.IsLive(res.RunID) {
		t.Fatalf("shutdown did not join running resume: %+v %v", final, err)
	}
}

// Opus counterexample: two resumers hold the same E1 plan while admission is
// busy; A executes a side effect and parks on E2 before B gets its slot.
func TestEventWaitDeadline_ConcurrentBusyResumersDoNotReplaySideEffect(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	effects := &recordingCodeRunner{}
	deps.CodeRunner = effects
	a, b := NewWiredExecutor(deps), NewWiredExecutor(deps)
	a.WithResumeRetryBackoff(time.Millisecond, time.Millisecond)
	b.WithResumeRetryBackoff(time.Millisecond, time.Millisecond)
	p := saveResumePipeline(t, deps.Store, "stale-event-plan", `{"dsl_version":"1.0","name":"stale-event-plan","concurrency_key":"event-gate","max_concurrent":1,"steps":[{"id":"e1","type":"wait","wait":{"kind":"event","event_type":"first"}},{"id":"effect","type":"code","code":{"runtime":"cel","code":"true"}},{"id":"e2","type":"wait","wait":{"kind":"event","event_type":"second"}}]}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := a.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	_, release, err := deps.Runs.Acquire(ctx, AcquireOpts{RunID: "blocker", WorkspaceID: "ws_test", ConcurrencyKey: "event-gate", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if ok, err := a.signalWaits.Deliver(ctx, res.RunID, "first", "payload"); err != nil || !ok {
		t.Fatalf("deliver: %v %v", ok, err)
	}
	queuedA, queuedB := make(chan struct{}), make(chan struct{})
	retryA, retryB := make(chan struct{}), make(chan struct{})
	a.onResumeSlotBusy = func(string) {
		close(queuedA)
		select {
		case <-retryA:
		case <-ctx.Done():
		}
	}
	b.onResumeSlotBusy = func(string) {
		close(queuedB)
		select {
		case <-retryB:
		case <-ctx.Done():
		}
	}
	doneA, doneB := make(chan struct{}), make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { defer close(doneA); a.ResumeEventRun(ctx, res.RunID, logger) }()
	go func() { defer close(doneB); b.ResumeEventRun(ctx, res.RunID, logger) }()
	// Both plans are now built from E1 with no restored side-effect output.
	for _, queued := range []<-chan struct{}{queuedA, queuedB} {
		select {
		case <-queued:
		case <-time.After(time.Second):
			t.Fatal("resumer did not wait on busy gate")
		}
	}
	release()
	close(retryA)
	select {
	case <-doneA:
	case <-time.After(time.Second):
		t.Fatal("first resumer did not finish")
	}
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting || rec.CurrentStepID != "e2" {
		t.Fatalf("first resume: %+v %v", rec, err)
	}
	close(retryB)
	select {
	case <-doneB:
	case <-time.After(time.Second):
		t.Fatal("second resumer did not finish")
	}
	if count := atomic.LoadInt32(&effects.calls); count != 1 {
		t.Fatalf("stale busy-gate resume replayed side effect %d times, want 1", count)
	}
	rec, err = deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting || rec.CurrentStepID != "e2" {
		t.Fatalf("second resume: %+v %v", rec, err)
	}
}

// Busy concurrency keys must not occupy every worker and starve an unrelated
// expiry. Deterministic IDs put all eight blocked runs before the free run.
// A preflight barrier fills every admission permit before any attempt returns;
// the concurrency blocker stays held until after every assertion.
func TestEventWaitDeadline_BusySlotsDoNotStarveOtherExpiry(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	gated := saveResumePipeline(t, deps.Store, "busy-expiries", `{"dsl_version":"1.0","name":"busy-expiries","concurrency_key":"busy-events","max_concurrent":1,"steps":[{"id":"gate","type":"wait","wait":{"kind":"event","event_type":"approve"}}]}`)
	free := saveResumePipeline(t, deps.Store, "free-expiry", eventWaitDSL)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		pipelineID := gated.ID
		if i == 8 {
			pipelineID = free.ID
		}
		res, err := exec.Run(ctx, RunInput{PipelineID: pipelineID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: fmt.Sprintf("fair-%02d", i)})
		if err != nil || res.Status != "WAITING" {
			t.Fatalf("park %d: %+v %v", i, res, err)
		}
	}
	_, release, err := deps.Runs.Acquire(ctx, AcquireOpts{RunID: "blocker", WorkspaceID: "ws_test", ConcurrencyKey: "busy-events", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=?`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	// Start the watchdog only after fixture setup. This is a liveness bound,
	// not a latency requirement: race instrumentation and the fixture's single
	// SQLite connection can delay the eight plans and their admission reads.
	sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pf := &fairnessAdmissionPreflight{entered: make(chan string, 8), release: make(chan struct{})}
	exec.WithRunPreflight(pf)
	stop := StartEventWaitSweeper(sweepCtx, db, exec, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	defer stop()
	for i := 0; i < 8; i++ {
		select {
		case pipelineID := <-pf.entered:
			if pipelineID != gated.ID {
				t.Fatalf("unrelated run entered before eight busy admissions: %s", pipelineID)
			}
		case <-sweepCtx.Done():
			t.Fatalf("only %d of eight busy admissions reached preflight: %v", i, sweepCtx.Err())
		}
	}
	close(pf.release)

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		rec, err := deps.RunStore.Get(sweepCtx, "fair-08")
		if err != nil {
			t.Fatalf("read unrelated expiry: %v", err)
		}
		if rec.Status == RunStatusFailed {
			if !strings.Contains(rec.ErrorMessage, "timed out") {
				t.Fatalf("unrelated run failed without expiring: %+v", rec)
			}
			for i := 0; i < 8; i++ {
				blocked, err := deps.RunStore.Get(sweepCtx, fmt.Sprintf("fair-%02d", i))
				if err != nil || blocked.Status != RunStatusWaiting {
					t.Fatalf("blocked run %d: %+v %v", i, blocked, err)
				}
			}
			return
		}
		select {
		case <-ticker.C:
		case <-sweepCtx.Done():
			t.Fatalf("eight busy slots starved an unrelated event expiry (last status=%s): %v", rec.Status, sweepCtx.Err())
		}
	}
}

// fairnessAdmissionPreflight lets the test fill the sweeper's eight permits
// without depending on goroutine scheduling speed. Once released it is a no-op;
// the registry's real concurrency gate still declines every busy run.
type fairnessAdmissionPreflight struct {
	entered chan string
	release chan struct{}
}

func (p *fairnessAdmissionPreflight) Check(ctx context.Context, req PreflightRequest) error {
	select {
	case <-p.release:
		return nil
	default:
	}
	select {
	case p.entered <- req.PipelineID:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestEventWaitDeadline_SpuriousResumeDoesNotEnterPendingWait(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "spurious-event", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	// A spurious pending wake must not emit a resume or update the run.
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before := rec.UpdatedAt
	exec.ResumeEventRun(ctx, res.RunID, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec, err = deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting || rec.UpdatedAt != before {
		t.Fatalf("spurious resume changed pending run: %+v %v", rec, err)
	}
}

// An API-owned original lifetime can still hold its registry slot after
// MarkWaiting. Sweep admission must yield instead of waiting for release.
func TestEventWaitDeadline_LiveOriginalsDoNotStarveOtherExpiry(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "live-originals", eventWaitDSL)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: fmt.Sprintf("live-%02d", i)})
		if err != nil || res.Status != "WAITING" {
			t.Fatalf("park %d: %+v %v", i, res, err)
		}
		if i < 8 {
			_, release, err := deps.Runs.Acquire(ctx, AcquireOpts{RunID: res.RunID, WorkspaceID: "ws_test"})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
		}
	}
	if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=?`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	stop := StartEventWaitSweeper(ctx, db, exec, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	defer stop()
	waitForRunStatus(t, deps.RunStore, "live-08", RunStatusFailed, time.Second)
	for i := 0; i < 8; i++ {
		rec, err := deps.RunStore.Get(ctx, fmt.Sprintf("live-%02d", i))
		if err != nil || rec.Status != RunStatusWaiting {
			t.Fatalf("live original changed: %+v %v", rec, err)
		}
	}
}
