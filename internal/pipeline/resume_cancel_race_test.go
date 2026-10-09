package pipeline

import (
	"context"
	"log/slog"
	"testing"
)

// #2910: a cancel that reaches a resumed lifetime after it took the run's
// registry entry — the "resume wins" side of the cancel/resume race — must
// stop that lifetime before its next side-effecting step and land a
// CANCELLED row, not let it run on or record it as interrupted.
func TestResumeCancelledAfterRegistryEntryRunsNoFurtherStep(t *testing.T) {
	tests := []struct {
		name string
		// where the cancel reaches the resumed lifetime
		beforeStatusRead bool
		// another lifetime moved the run on before this one re-read it
		stalePlan bool
	}{
		{name: "before the resume re-reads the persisted status", beforeStatusRead: true},
		{name: "before the re-read finds the plan superseded", beforeStatusRead: true, stalePlan: true},
		{name: "after admission, before the next step"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openResumeTestDB(t)
			defer db.Close()
			ctx := context.Background()
			store, runs := NewStore(db), NewRunStore(db)
			wp := NewSQLWaitpointStore(db)
			defer wp.Close()
			registry := NewRunRegistry()
			p := saveResumePipeline(t, store, "appr-cancel-race", asyncApprovalLinearDSL)
			exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
				WithRunStore(runs).WithWaitpointStore(wp).WithRunRegistry(registry)

			res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != "WAITING" {
				t.Fatalf("status %q, want WAITING", res.Status)
			}
			if err := wp.CompleteApproval(ctx, "ws_test", res.WaitpointToken, true, "u_admin", "ok"); err != nil {
				t.Fatal(err)
			}
			rec, err := runs.Get(ctx, res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			plan, reason := exec.buildResumePlan(ctx, rec)
			if plan == nil {
				t.Fatalf("resume plan nil: %s", reason)
			}
			plan.reason = resumeReasonApproval

			// The cancel API found the run live again (its parked-run fence
			// lost to this resume) and cancelled the resumed lifetime.
			cancelled := false
			cancelLive := func() {
				cancelled = true
				if err := registry.Cancel(res.RunID); err != nil {
					t.Errorf("cancel resumed lifetime: %v", err)
				}
			}
			var onAdmitted func()
			if tc.beforeStatusRead {
				exec.afterResumeRegistryAcquire = func(id string) {
					if tc.stalePlan {
						if _, err := db.Exec(`UPDATE pipeline_runs SET current_step_id = 'later' WHERE id = ?`, id); err != nil {
							t.Error(err)
						}
					}
					cancelLive()
				}
			} else {
				onAdmitted = cancelLive
			}
			exec.runResumedRunWithRetry(ctx, plan, slog.Default(), true, onAdmitted)
			if !cancelled {
				t.Fatal("the cancel point was never reached")
			}

			rec, err = runs.Get(ctx, res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != RunStatusCancelled {
				t.Errorf("run status %q (%s), want cancelled", rec.Status, rec.ErrorMessage)
			}
			outputs, err := runs.GetStepOutputs(ctx, res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, ran := outputs["done"]; ran {
				t.Error("the step after the approval ran although the resumed lifetime was cancelled")
			}
		})
	}
}
