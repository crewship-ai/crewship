package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// #1424 — the DAG wave loop had no ctx.Err() check between waves. On a
// cancellation that lands after one wave completes but before the next is
// scheduled, the next wave's goroutines short-circuit on dagCtx.Err()
// WITHOUT recording completion or firstErr, so the ready set never shrinks
// and the loop respawns forever at full CPU — Run() never returns and the
// deferred release() never frees the concurrency slot.
//
// The fixed loop must return promptly with a terminal status and free the
// slot.
func TestExecutor_DAG_CancelBetweenWaves(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	registry := NewRunRegistry()

	const runID = "run_dag_cancel"
	var bCalls int32
	runner := runnerFunc(func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		switch req.AgentSlug {
		case "agent_a":
			// Wave 1 completes; the cancel lands just as the run finishes
			// this step, so the next wave sees a cancelled context.
			_ = registry.Cancel(runID)
			return AgentStepResult{Output: "a-ok", CostUSD: 0.001}, nil
		case "agent_b":
			atomic.AddInt32(&bCalls, 1)
			return AgentStepResult{Output: "b-ok", CostUSD: 0.001}, nil
		}
		return AgentStepResult{Output: "x"}, nil
	})
	exec := NewExecutor(store, NewResolver(db), runner, nil).WithRunRegistry(registry)
	ctx := context.Background()

	in := validSaveInput("dagcancel")
	// Two waves: a (wave 1), b needs a (wave 2).
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"dagcancel","steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"agent_a","prompt":"go"},` +
		`{"id":"b","type":"agent_run","agent_slug":"agent_b","prompt":"go","needs":["a"]}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	type outcome struct {
		res *RunResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, rerr := exec.Run(ctx, RunInput{
			PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: runID,
		})
		done <- outcome{res, rerr}
	}()

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("run: %v", o.err)
		}
		if o.res.Status != "CANCELLED" {
			t.Errorf("expected CANCELLED, got %q (err=%q)", o.res.Status, o.res.ErrorMessage)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return within 5s — DAG hot-looped on between-wave cancel")
	}

	// Wave 2 must not have executed — its goroutines short-circuit on the
	// cancelled context.
	if n := atomic.LoadInt32(&bCalls); n != 0 {
		t.Errorf("expected agent_b to never run, got %d calls", n)
	}
	// The concurrency slot must be freed (deferred release ran).
	if registry.IsLive(runID) {
		t.Errorf("concurrency slot not released after cancelled DAG run")
	}
}
