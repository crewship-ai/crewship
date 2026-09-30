package pipeline

// The backup's quiet window versus a routine run that is starting.
//
// A run the pipeline scheduler or the pending-run dispatcher starts is not a
// writer the barrier drains: it steps out of the gate (or leaves it after the
// claim) and the backup's busy probe counts it instead. The probe reads
// pipeline_runs.status = 'running', so a run between its claim and that row
// was invisible to it — the window could close, drain, re-check (zero), hold,
// and the run would then insert its row and execute steps inside the copy.
//
// These tests stop a run in exactly that gap (a blocking preflight, or a fake
// executor that has been entered but has not started) and open a window with
// the production probe's view of the database. The window must not hold, and
// a run with no admission must not start until the window is released.
// Every ordering is a channel; the only durations are how long a Begin that
// can never hold keeps retrying before it reports busy.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

// runningRowsProbe is the busy probe as production sees routine runs: rows
// whose status reached 'running'.
func runningRowsProbe(db *sql.DB) quiesce.BusyFunc {
	return func(ctx context.Context) (int, string, error) {
		if db == nil {
			return 0, "", nil
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_runs WHERE status = 'running'`).Scan(&n); err != nil {
			return 0, "", err
		}
		return n, fmt.Sprintf("%d routine run(s) running", n), nil
	}
}

// quickBegin opens a window on the default controller (the one every
// scheduler and dispatcher admits through) with a busy wait short enough
// that a window which can never hold gives up at once.
func quickBegin(t *testing.T, busy quiesce.BusyFunc) (*quiesce.Window, error) {
	t.Helper()
	return quiesce.Default().Begin(context.Background(), quiesce.Options{
		BusyWait: 30 * time.Millisecond, Poll: time.Millisecond,
		HoldCap: time.Minute, DrainTimeout: time.Minute,
		Busy: busy, Reason: "test",
	})
}

// blockingPreflight parks Run after admission and before the run row exists.
type blockingPreflight struct {
	entered chan struct{}
	release chan struct{}
}

func (p *blockingPreflight) Check(ctx context.Context, _ PreflightRequest) error {
	p.entered <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// startProbeExecutor models a run between the dispatcher's claim and its row
// reaching running: entered says Run was called, release lets it start, and
// heldAtStart records whether the copy was running when it did.
type startProbeExecutor struct {
	entered     chan struct{}
	release     chan struct{}
	heldAtStart chan bool
}

func (e *startProbeExecutor) Run(ctx context.Context, in RunInput) (*RunResult, error) {
	e.entered <- struct{}{}
	<-e.release
	e.heldAtStart <- quiesce.WritesHeld()
	return &RunResult{RunID: "run_" + in.TriggeredByID, Status: "COMPLETED"}, nil
}

func TestQuietWindowDoesNotHoldOverAStartingPendingRun(t *testing.T) {
	s := enqueueDue(t, 1)
	exec := &startProbeExecutor{entered: make(chan struct{}), release: make(chan struct{}), heldAtStart: make(chan bool, 1)}
	d := NewPendingRunDispatcher(s, exec, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.fireOne(context.Background(), PendingRun{ID: "pa"})
	}()
	<-exec.entered // claimed; the run has not reached running

	w, err := quickBegin(t, runningRowsProbe(nil))
	if err == nil {
		close(exec.release)
		held := <-exec.heldAtStart
		w.Release()
		<-done
		t.Fatalf("the window held over a claimed run that had not reached running (run then started with writes held=%v)", held)
	}
	if !errors.Is(err, quiesce.ErrBusy) {
		t.Fatalf("Begin: %v, want ErrBusy", err)
	}
	close(exec.release)
	if <-exec.heldAtStart {
		t.Fatal("the run started while writes were held")
	}
	<-done
}

// A pending row the dispatcher reaches while a window is open stays due: it
// is not claimed, and the run does not start.
func TestPendingRunStaysDueWhileAWindowIsOpen(t *testing.T) {
	s := enqueueDue(t, 1)
	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	w, err := quickBegin(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.fireOne(context.Background(), PendingRun{ID: "pa"})
	w.Release()
	if len(exec.seen) != 0 {
		t.Fatalf("a run started while the window was held: %d", len(exec.seen))
	}
	var status string
	if err := s.db.QueryRowContext(context.Background(), `SELECT status FROM pending_runs WHERE id = 'pa'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("pending row status = %q after a refused start, want pending", status)
	}
	d.fireOne(context.Background(), PendingRun{ID: "pa"})
	if len(exec.seen) != 1 {
		t.Fatalf("the row did not fire after release: %d runs", len(exec.seen))
	}
}

func TestQuietWindowDoesNotHoldOverAStartingScheduledRun(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_q", "q", fmt.Sprintf(oneStepAgentDefFmt, "q", "q_agent"))
	seedDueScheduleRow(t, db, "psched_q", "pipe_q")

	runner := newDispatchBlockingRunner()
	pf := &blockingPreflight{entered: make(chan struct{}), release: make(chan struct{})}
	pipelineStore := NewStore(db)
	exec := NewExecutor(pipelineStore, NewResolver(db), runner, nil).WithRunPreflight(pf)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := NewPipelineScheduler(NewScheduleStore(db), pipelineStore, exec, logger)

	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.tick(context.Background())
	}()
	<-pf.entered // the fire stepped out of the gate; no run row yet

	w, err := quickBegin(t, runningRowsProbe(db))
	if err == nil {
		close(pf.release)
		<-runner.started // the run's first step executes inside the held window
		held := quiesce.WritesHeld()
		w.Release()
		<-done
		t.Fatalf("the window held over a scheduled run that had not reached running (its step ran with writes held=%v)", held)
	}
	if !errors.Is(err, quiesce.ErrBusy) {
		t.Fatalf("Begin: %v, want ErrBusy", err)
	}
	close(pf.release)
	<-runner.started
	<-done
}

// A run started with no admission of its own (a resume, a background
// caller) waits out an open window before it writes anything.
func TestExecutorRunWaitsOutAnOpenWindow(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipelineDef(t, db, "pipe_w", "w", fmt.Sprintf(oneStepAgentDefFmt, "w", "w_agent"))
	runner := newDispatchBlockingRunner()
	pf := &blockingPreflight{entered: make(chan struct{}, 1), release: make(chan struct{})}
	close(pf.release)
	exec := NewExecutor(NewStore(db), NewResolver(db), runner, nil).WithRunPreflight(pf)

	w, err := quickBegin(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := exec.Run(context.Background(), RunInput{PipelineID: "pipe_w", WorkspaceID: "ws_test", Mode: ModeRun})
		done <- err
	}()
	// Negative check: the run must not get as far as its preflight while
	// the window holds. The wait only bounds how long we look.
	select {
	case <-pf.entered:
		w.Release()
		t.Fatal("a run with no admission started while the window was held")
	case <-time.After(50 * time.Millisecond):
	}
	w.Release()
	<-pf.entered
	<-runner.started
	if err := <-done; err != nil {
		t.Fatalf("run after release: %v", err)
	}
}
