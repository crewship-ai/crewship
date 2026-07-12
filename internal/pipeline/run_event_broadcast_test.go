package pipeline

import (
	"context"
	"testing"
)

// Review findings on the pipeline.run.failed toast (#983):
//
//  1. The broadcast payload carried no status, so the frontend's
//     CANCELLED suppression could never fire — a user cancel trips
//     ctx.Err() in the step loop, which broadcast run.failed with
//     "context canceled" BEFORE the outer Run() relabels the run
//     CANCELLED → false "Routine failed" toast.
//  2. Run-level failed/completed emits were unconditional while
//     emitRunStarted is gated on depth==0 && !dry-run — a failing
//     call_pipeline child toasted once per nesting level, and a
//     dry-run draft validation toasted workspace-wide.
//
// These tests lock the fix: status derived from ctx cancellation, and
// run-level BROADCASTS gated to top-level real runs (journal entries
// stay for every run — observability is not the noise problem).

func newEmitCtxForBroadcastTest(ws *captureWS, em *captureEmitter, depth int, dryRun bool) *pipelineEmitContext {
	return &pipelineEmitContext{
		emitter:      em,
		ws:           ws,
		workspaceID:  "ws1",
		pipelineID:   "pln_1",
		pipelineSlug: "report",
		runID:        "run_1",
		depth:        depth,
		dryRun:       dryRun,
	}
}

func TestEmitRunFailed_StatusFailed(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	c := newEmitCtxForBroadcastTest(ws, &captureEmitter{}, 0, false)

	c.emitRunFailed(context.Background(), "s1", "boom")

	if len(ws.events) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(ws.events))
	}
	if got := ws.events[0].payload["status"]; got != "FAILED" {
		t.Errorf("status = %v, want FAILED", got)
	}
}

func TestEmitRunFailed_CancelledStatusOnCanceledCtx(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	c := newEmitCtxForBroadcastTest(ws, &captureEmitter{}, 0, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // user cancel: the step loop emits with the already-canceled run ctx
	c.emitRunFailed(ctx, "s1", "context canceled")

	if len(ws.events) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(ws.events))
	}
	if got := ws.events[0].payload["status"]; got != "CANCELLED" {
		t.Errorf("status = %v, want CANCELLED (frontend suppresses the failure toast on cancel)", got)
	}
}

func TestEmitRunFailed_NestedRunDoesNotBroadcast(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	em := &captureEmitter{}
	c := newEmitCtxForBroadcastTest(ws, em, 1, false) // call_pipeline child

	c.emitRunFailed(context.Background(), "s1", "boom")

	if len(ws.events) != 0 {
		t.Errorf("nested run must not broadcast run.failed (one toast per nesting level); got %v", ws.types())
	}
	if len(em.entries) != 1 {
		t.Errorf("journal entry must still be written for nested runs, got %d", len(em.entries))
	}
}

func TestEmitRunLevel_DryRunDoesNotBroadcast(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	em := &captureEmitter{}
	c := newEmitCtxForBroadcastTest(ws, em, 0, true) // draft validation

	c.emitRunFailed(context.Background(), "s1", "boom")
	c.emitRunCompleted(context.Background(), 1200, 0.5)

	if len(ws.events) != 0 {
		t.Errorf("dry-run must not broadcast run-level events (workspace-wide toast for a draft test); got %v", ws.types())
	}
	if len(em.entries) != 2 {
		t.Errorf("journal entries must still be written, got %d", len(em.entries))
	}
}

func TestEmitRunCompleted_TopLevelStillBroadcasts(t *testing.T) {
	t.Parallel()
	ws := &captureWS{}
	c := newEmitCtxForBroadcastTest(ws, &captureEmitter{}, 0, false)

	c.emitRunCompleted(context.Background(), 1200, 0.5)

	if got := ws.types(); len(got) != 1 || got[0] != "pipeline.run.completed" {
		t.Errorf("top-level real run must broadcast completion, got %v", got)
	}
}
