package pipeline

import (
	"log/slog"
	"testing"
	"time"
)

func TestHumanAccountLifecycleAtDeferredExecution(t *testing.T) {
	for _, scenario := range []string{"active", "suspended-empty", "suspended-queued", "suspended-resume", "deleted-queued", "suspended-legacy", "autonomous", "dry-run"} {
		t.Run(scenario, func(t *testing.T) {
			db := openFactoryTestDB(t)
			defer db.Close()
			mustExec(t, db, `INSERT INTO users(id) VALUES('human'); CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,role TEXT,capabilities TEXT,access_mode TEXT NOT NULL DEFAULT 'trusted'); INSERT INTO workspace_members(workspace_id,user_id,role,capabilities) VALUES('ws_test','human','MANAGER','[]')`)
			runner := newMockRunner()
			deps := fullExecutorDeps(t, db, runner)
			p := saveResumePipeline(t, deps.Store, "account-lifecycle", resumeLinearDSL)
			pendingDB := newPendingDB(t)
			defer pendingDB.Close()
			pending := NewPendingRunStore(pendingDB)
			actor, authority := "human", RoutineRunAuthority
			if scenario == "autonomous" {
				actor, authority = "", ""
			}
			if scenario == "suspended-legacy" {
				authority = ""
			}
			now := time.Now()
			if _, _, err := pending.Enqueue(t.Context(), PendingRun{ID: "queued", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug, FireAt: now.Add(-time.Minute), InvokingUserID: actor, InvocationAuthority: authority}); err != nil {
				t.Fatal(err)
			}
			if scenario == "suspended-empty" {
				mustExec(t, db, `UPDATE users SET suspended_at='' WHERE id='human'`)
			} else if scenario == "deleted-queued" {
				mustExec(t, db, `DELETE FROM users WHERE id='human'`)
			} else if scenario != "active" {
				mustExec(t, db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='human'`)
			}
			queued, err := pending.ClaimDue(t.Context(), "queued", now)
			if err != nil || queued == nil {
				t.Fatalf("claim: %+v %v", queued, err)
			}
			exec := NewWiredExecutor(deps)
			if scenario == "suspended-resume" {
				runs := NewRunStore(db)
				insertInFlightRun(t, runs, &RunRecord{ID: "resume-account", WorkspaceID: queued.WorkspaceID, PipelineID: queued.PipelineID, PipelineSlug: queued.PipelineSlug, Status: RunStatusRunning, Mode: ModeRun, CurrentStepID: "b", StepOutputsJSON: `{"a":"already complete"}`, InvokingUserID: queued.InvokingUserID, InvocationAuthority: queued.InvocationAuthority})
				if _, _, err := exec.WithRunStore(runs).ResumeInterruptedRuns(t.Context(), slog.Default()); err != nil {
					t.Fatal(err)
				}
				rec := waitForRunStatus(t, runs, "resume-account", RunStatusInterrupted, 5*time.Second)
				if rec.Status != RunStatusInterrupted {
					t.Fatal(rec.Status)
				}
			} else {
				mode := ModeRun
				if scenario == "dry-run" {
					mode = ModeDryRun
				}
				res, err := exec.Run(t.Context(), RunInput{PipelineID: queued.PipelineID, WorkspaceID: queued.WorkspaceID, InvokingUserID: queued.InvokingUserID, InvocationAuthority: queued.InvocationAuthority, Mode: mode})
				if scenario == "dry-run" {
					if err != nil || res == nil || res.Status != "DRY_RUN_OK" {
						t.Fatalf("dry-run compatibility: %+v %v", res, err)
					}
				} else if scenario == "active" || scenario == "autonomous" {
					if err != nil || res == nil || res.Status != "COMPLETED" {
						t.Fatalf("positive: %+v %v", res, err)
					}
				} else if err == nil {
					t.Fatalf("suspended queued actor dispatched: %+v", res)
				}
			}
			runner.mu.Lock()
			defer runner.mu.Unlock()
			want := 0
			if scenario == "active" || scenario == "autonomous" {
				want = 3
			}
			if len(runner.calls) != want {
				t.Fatalf("dispatches=%d want%d", len(runner.calls), want)
			}
		})
	}
}
