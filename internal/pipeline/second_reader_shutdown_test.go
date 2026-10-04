package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// armedBlockingPreflight blocks only once armed, modelling any ctx-aware
// pre-runDSL admission work (preflight reads, pipeline load, quiet window).
type armedBlockingPreflight struct {
	armed     atomic.Bool
	entered   chan struct{}
	sanitized bool
}

func (p *armedBlockingPreflight) Check(ctx context.Context, _ PreflightRequest) error {
	if !p.armed.Load() {
		return nil
	}
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	if p.sanitized {
		return fmt.Errorf("%w: credentials could not be checked: %s", ErrRunPreflightBlocked, ctx.Err().Error())
	}
	return ctx.Err()
}

// Docs promise: "parked waits and expirations awaiting a slot remain
// recoverable at the next boot". A shutdown that lands while the sweeper's
// resume is still in Run admission falls into runResumedRunWithRetry's default
// branch and terminalizes the run as interrupted.
func TestCorrectedReader_ShutdownDuringAdmissionKeepsRunRecoverable(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	pf := &armedBlockingPreflight{entered: make(chan struct{}, 1)}
	deps.Preflight = pf
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "shutdown-admission", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=?`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	pf.armed.Store(true)
	stop := StartEventWaitSweeper(ctx, db, exec, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	select {
	case <-pf.entered:
	case <-time.After(2 * time.Second):
		stop()
		t.Fatal("sweeper never reached admission")
	}
	stop() // graceful shutdown
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var wait string
	_ = db.QueryRow(`SELECT status FROM pipeline_signal_waits WHERE run_id=?`, res.RunID).Scan(&wait)
	if rec.Status != RunStatusWaiting {
		t.Fatalf("shutdown during resume admission terminalized parked run: status=%s err=%q wait=%s", rec.Status, rec.ErrorMessage, wait)
	}
}

func TestEventWaitDeadline_CancelledPlanReadKeepsRunRecoverable(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "cancelled-plan-read", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if plan := exec.buildEventResumePlan(cancelled, rec, logger); plan != nil {
		t.Fatal("cancelled plan read unexpectedly succeeded")
	}
	rec, err = deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting {
		t.Fatalf("cancelled plan read terminalized run: %+v %v", rec, err)
	}
	// The same phase must still interrupt genuinely corrupt state under a live
	// context; lifecycle cancellation must not turn this into a fail-open gate.
	invalid := *rec
	invalid.Mode = ModeDryRun
	if plan := exec.buildEventResumePlan(ctx, &invalid, logger); plan != nil {
		t.Fatal("invalid live plan accepted")
	}
	rec, err = deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusInterrupted {
		t.Fatalf("genuine invalid plan did not interrupt: %+v %v", rec, err)
	}
}

func TestEventWaitDeadline_SanitizedPreflightCancellationKeepsRunRecoverable(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	deps := fullExecutorDeps(t, db, newMockRunner())
	deps.RunVerdict = nil
	pf := &armedBlockingPreflight{entered: make(chan struct{}, 1), sanitized: true}
	deps.Preflight = pf
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "sanitized-shutdown", eventWaitDSL)
	ctx := context.Background()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil || res.Status != "WAITING" {
		t.Fatalf("park: %+v %v", res, err)
	}
	if _, err := db.Exec(`UPDATE pipeline_signal_waits SET timeout_at=?`, tsformat.Format(time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	pf.armed.Store(true)
	stop := StartEventWaitSweeper(ctx, db, exec, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)
	defer stop()
	select {
	case <-pf.entered:
	case <-time.After(time.Second):
		t.Fatal("sweeper never reached sanitized preflight")
	}
	stop()
	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil || rec.Status != RunStatusWaiting {
		t.Fatalf("sanitized shutdown error terminalized run: %+v %v", rec, err)
	}
}
