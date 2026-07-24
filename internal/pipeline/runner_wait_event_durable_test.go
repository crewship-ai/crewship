package pipeline

// Durable wait:event signals (#1409 item 3).
//
// Before this, a wait:event step blocked the run's goroutine on an
// in-memory-only SignalRegistry channel: the run never even parked
// (status stayed 'running'), and a signal delivered while the process
// was down (or before the step registered) was lost forever — the run
// would just time out. These tests pin the fixed contract, mirroring
// wait:approval's pipeline_waitpoints durability:
//
//   - a top-level foreground run parks (status=waiting) at a wait:event
//     step and persists a pipeline_signal_waits row (status=pending) —
//     durable arm, not just an in-memory registration.
//   - a signal DELIVERED while nothing is registered in memory (the
//     exact restart-survival property: this is what the signal endpoint
//     durably records BEFORE it even tries an in-process wake) is not
//     lost — a later resume finds it and completes the run with that
//     payload as the step's output.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

const eventWaitDSL = `{
  "dsl_version": "1.0",
  "name": "event-wait",
  "steps": [
    {"id": "gate", "type": "wait", "wait": {"kind": "event", "event_type": "approve"}, "timeout_seconds": 3600}
  ]
}`

func TestExecutor_WaitEventStep_ParksWithDurableArm(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	deps := fullExecutorDeps(t, db, newMockRunner())
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-wait-parks", eventWaitDSL)

	res, err := exec.Run(ctx, RunInput{
		PipelineID:  p.ID,
		WorkspaceID: "ws_test",
		Mode:        ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("status = %q, want WAITING (a top-level wait:event must PARK, not block in-process)", res.Status)
	}

	rec, err := deps.RunStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if rec.Status != "waiting" {
		t.Errorf("persisted run status = %q, want waiting", rec.Status)
	}
	if rec.CurrentStepID != "gate" {
		t.Errorf("current_step_id = %q, want gate", rec.CurrentStepID)
	}

	var status, eventType string
	if err := db.QueryRow(`SELECT status, event_type FROM pipeline_signal_waits WHERE run_id = ? AND step_id = 'gate'`, res.RunID).
		Scan(&status, &eventType); err != nil {
		t.Fatalf("query signal wait row: %v", err)
	}
	if status != "pending" {
		t.Errorf("signal wait status = %q, want pending", status)
	}
	if eventType != "approve" {
		t.Errorf("signal wait event_type = %q, want approve", eventType)
	}
}

func TestExecutor_WaitEventStep_SurvivesRestart_DeliveredWhileDown(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()

	deps := fullExecutorDeps(t, db, newMockRunner())
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "event-wait-restart", eventWaitDSL)

	res, err := exec.Run(ctx, RunInput{
		PipelineID:  p.ID,
		WorkspaceID: "ws_test",
		Mode:        ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("status = %q, want WAITING", res.Status)
	}

	// Simulate "the process was down": deliver the signal durably WITHOUT
	// any live in-memory registration at all (no Register call, no
	// SignalRegistry.Signal — the exact scenario that silently dropped a
	// signal before #1409). A brand-new SignalWaitStore instance against
	// the same DB stands in for "the delivery arrived from a different
	// process/request than the one that will resume it".
	waitStore := NewSQLSignalWaitStore(db)
	armed, err := waitStore.Deliver(ctx, res.RunID, "approve", `{"approved":true}`)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if !armed {
		t.Fatalf("deliver reported armed=false; expected a pending wait for run %s", res.RunID)
	}

	// Resume — the in-process analogue of what a fresh process's boot
	// scan (ResumeInterruptedRuns) would do after finding this row
	// waiting; ResumeAfterSignal is what the signal HTTP handler calls
	// after Deliver commits.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec.ResumeAfterSignal(res.RunID, logger)

	// Resume runs on its own goroutine; poll for the terminal state.
	deadline := time.Now().Add(3 * time.Second)
	var final *RunRecord
	for time.Now().Before(deadline) {
		final, err = deps.RunStore.Get(ctx, res.RunID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if final.Status == "completed" || final.Status == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final.Status != "completed" {
		t.Fatalf("final run status = %q (error=%q), want completed", final.Status, final.ErrorMessage)
	}
	var outputs map[string]string
	if err := json.Unmarshal([]byte(final.StepOutputsJSON), &outputs); err != nil {
		t.Fatalf("unmarshal step outputs: %v", err)
	}
	if outputs["gate"] != `{"approved":true}` {
		t.Errorf("gate step output = %q, want the delivered payload", outputs["gate"])
	}

	// The wait row must be marked consumed, not left delivered forever —
	// otherwise a hypothetical second resume attempt could double-consume it.
	var rowStatus string
	if err := db.QueryRow(`SELECT status FROM pipeline_signal_waits WHERE run_id = ? AND step_id = 'gate'`, res.RunID).Scan(&rowStatus); err != nil {
		t.Fatalf("query signal wait row: %v", err)
	}
	if rowStatus != "consumed" {
		t.Errorf("signal wait status = %q, want consumed", rowStatus)
	}
}
