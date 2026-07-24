package pipeline

import (
	"context"
	"testing"
	"time"
)

// #1428 (2.8) — DAG step outputs were flushed only at the wave boundary, so a
// hard kill mid-wave lost every already-completed step in that wave and
// replayed them on resume. With the incremental flush, a step that completes
// is persisted immediately — visible in the run row WHILE a slower peer in the
// same wave is still running — so resume can skip it.
func TestExecutor_DAG_IncrementalFlush_PersistsCompletedStepMidWave(t *testing.T) {
	db := openExecutorGateDB(t)
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	store := NewStore(db)
	runStore := NewRunStore(db)

	release := make(chan struct{})
	runner := runnerFunc(func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		if req.AgentSlug == "agent_b" {
			// Slow peer: stay in-flight until the test releases it, keeping
			// wave 1 open so the wave-boundary flush cannot have run yet.
			<-release
		}
		return AgentStepResult{Output: "out:" + req.AgentSlug, CostUSD: 0.001}, nil
	})
	exec := NewExecutor(store, NewResolver(db), runner, nil).WithRunStore(runStore)
	ctx := context.Background()

	in := validSaveInput("dag-flush")
	// wave 1 = {a, b} independent; a finishes fast, b blocks. Step c needs
	// both, which forces DAG mode (the `needs:` edge) — without it the routine
	// would run the linear loop, which already flushes per step.
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"dag-flush","steps":[` +
		`{"id":"a","type":"agent_run","agent_slug":"agent_a","prompt":"go"},` +
		`{"id":"b","type":"agent_run","agent_slug":"agent_b","prompt":"go"},` +
		`{"id":"c","type":"agent_run","agent_slug":"agent_c","prompt":"go","needs":["a","b"]}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	const runID = "run_dag_flush"
	go func() {
		_, _ = exec.Run(ctx, RunInput{
			PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: runID,
		})
	}()

	// Poll for step a's output persisted WHILE b is still blocked (wave open).
	// Step outputs live in the normalized pipeline_run_step_outputs table
	// (#1411) — read them through GetStepOutputs, the same source resume and
	// the API use; the legacy step_outputs_json blob is no longer written on
	// the hot path.
	deadline := time.Now().Add(3 * time.Second)
	sawA := false
	for time.Now().Before(deadline) {
		m, gerr := runStore.GetStepOutputs(ctx, runID)
		if gerr == nil {
			if _, ok := m["a"]; ok {
				sawA = true
				// b must still be blocked — its output not yet present.
				if _, bDone := m["b"]; bDone {
					t.Fatal("agent_b finished before release; test can't prove mid-wave flush")
				}
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawA {
		t.Fatal("step a's output was not persisted mid-wave — wave-boundary-only flush regressed")
	}

	// Let b finish and the run complete.
	close(release)
	waitForRunStatus(t, runStore, runID, RunStatusCompleted, 3*time.Second)
	finalOuts, gerr := runStore.GetStepOutputs(ctx, runID)
	if gerr != nil {
		t.Fatalf("get final step outputs: %v", gerr)
	}
	if _, ok := finalOuts["b"]; !ok {
		t.Errorf("final outputs missing b: %v", finalOuts)
	}
}
