package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Start a real suspended run so this exercises the snapshot writer together
// with recovery, rather than assuming that the run row contains a version pin.
func TestResumeCapturedRecipeAfterHeadChanges(t *testing.T) {
	for _, cause := range []string{"restart", "approval"} {
		t.Run(cause, func(t *testing.T) {
			db := openResumeTestDB(t)
			t.Cleanup(func() { db.Close() })
			installExecutionSchema(t, db)
			ctx := context.Background()
			store, runs := NewStore(db), NewRunStore(db)
			waits := NewSQLWaitpointStore(db)
			t.Cleanup(waits.Close)
			p := saveResumePipeline(t, store, "captured-resume", asyncApprovalDAGDSL)
			executor := func() *Executor {
				return NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
					WithRunStore(runs).WithExecutionStore(NewExecutionStore(db)).WithWaitpointStore(waits).WithRunRegistry(NewRunRegistry())
			}
			started, err := executor().Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil || started.Status != "WAITING" {
				t.Fatalf("start: %+v %v", started, err)
			}
			rec, err := runs.Get(ctx, started.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.PipelineVersion != nil {
				t.Fatal("this regression requires a normal unpinned start")
			}
			var captured string
			if err := db.QueryRow(`SELECT executed_definition_json FROM pipeline_runs WHERE id=?`, rec.ID).Scan(&captured); err != nil || captured == "" {
				t.Fatalf("snapshot: %q %v", captured, err)
			}
			saveResumePipeline(t, store, p.Slug, strings.ReplaceAll(asyncApprovalDAGDSL, "final-{{ steps.draft.output }}", "WRONG-HEAD"))
			restarted := executor()
			if cause == "restart" {
				resumed, interrupted, err := restarted.ResumeInterruptedRuns(ctx, nil)
				if err != nil || resumed != 1 || interrupted != 0 {
					t.Fatalf("boot recovery: %d resumed, %d interrupted, %v", resumed, interrupted, err)
				}
				// Wait until the restarted executor has re-entered the same gate.
				deadline := time.Now().Add(5 * time.Second)
				for {
					var attempts int
					if err := db.QueryRow(`SELECT count(*) FROM pipeline_step_executions WHERE run_id=? AND step_id='gate'`, rec.ID).Scan(&attempts); err != nil {
						t.Fatal(err)
					}
					current, err := runs.Get(ctx, rec.ID)
					if err != nil {
						t.Fatal(err)
					}
					if attempts >= 2 && current.Status == RunStatusWaiting {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("gate not re-entered: attempts=%d status=%s", attempts, current.Status)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err := waits.CompleteApproval(ctx, "ws_test", started.WaitpointToken, true, "user", ""); err != nil {
				t.Fatal(err)
			}
			restarted.ResumeAfterApproval(rec.ID, nil)
			final := waitForRunStatus(t, runs, rec.ID, RunStatusCompleted, 5*time.Second)
			if final.Output != "final-drafted" {
				t.Fatalf("resumed wrong recipe: %q", final.Output)
			}
			var drafts int
			if err := db.QueryRow(`SELECT count(*) FROM pipeline_step_executions WHERE run_id=? AND step_id='draft'`, rec.ID).Scan(&drafts); err != nil {
				t.Fatal(err)
			}
			if drafts != 1 {
				t.Fatalf("completed step executed %d times", drafts)
			}
		})
	}
}

func TestResumeCapturedRecipeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot any
		want     string
	}{
		{"malformed", `{bad`, "no longer parses"},
		{"empty", ``, "no longer parses"},
		{"legacy missing after edit", nil, "content hash mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openResumeTestDB(t)
			t.Cleanup(func() { db.Close() })
			installExecutionSchema(t, db)
			store, runs := NewStore(db), NewRunStore(db)
			p := saveResumePipeline(t, store, "bad-snapshot", asyncApprovalDAGDSL)
			rec := &RunRecord{ID: "snapshot-test", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug, DefinitionHash: p.DefinitionHash, Mode: ModeRun, Status: RunStatusWaiting, CurrentStepID: "gate"}
			insertInFlightRun(t, runs, rec)
			if _, err := db.Exec(`UPDATE pipeline_runs SET executed_definition_json=? WHERE id=?`, tc.snapshot, rec.ID); err != nil {
				t.Fatal(err)
			}
			saveResumePipeline(t, store, p.Slug, strings.ReplaceAll(asyncApprovalDAGDSL, "final-{{ steps.draft.output }}", "WRONG-HEAD"))
			exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).WithRunStore(runs).WithExecutionStore(NewExecutionStore(db))
			plan, reason := exec.buildResumePlan(context.Background(), rec)
			if plan != nil || !strings.Contains(reason, tc.want) {
				t.Fatalf("plan=%+v reason=%q", plan, reason)
			}
		})
	}
}

func TestResumeCapturedRecipeDoesNotApplyNewOverrides(t *testing.T) {
	db := openResumeTestDB(t)
	t.Cleanup(func() { db.Close() })
	installExecutionSchema(t, db)
	if _, err := db.Exec(`CREATE TABLE routine_step_overrides (pipeline_id TEXT, step_id TEXT, prompt TEXT, model_override TEXT)`); err != nil {
		t.Fatal(err)
	}
	store, runs := NewStore(db), NewRunStore(db)
	p := saveResumePipeline(t, store, "override-resume", asyncApprovalDAGDSL)
	// The captured effective recipe can differ from the published source because
	// overrides were applied at original dispatch. Re-entry must keep that copy.
	captured := `{"name":"override-resume","steps":[{"id":"final","type":"agent_run","agent_slug":"s_b","prompt":"original effective prompt"}]}`
	rec := &RunRecord{ID: "override-run", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug, DefinitionHash: p.DefinitionHash, Mode: ModeRun, Status: RunStatusRunning, CurrentStepID: "final"}
	insertInFlightRun(t, runs, rec)
	if _, err := db.Exec(`UPDATE pipeline_runs SET executed_definition_json=? WHERE id=?`, captured, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO routine_step_overrides VALUES (?,?,?,?)`, p.ID, "final", "WRONG NEW PROMPT", ""); err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(runs).WithExecutionStore(NewExecutionStore(db)).WithStepOverrides(NewStepOverrideStore(db))
	resumed, interrupted, err := exec.ResumeInterruptedRuns(context.Background(), nil)
	if err != nil || resumed != 1 || interrupted != 0 {
		t.Fatalf("resume: %d %d %v", resumed, interrupted, err)
	}
	waitForRunStatus(t, runs, rec.ID, RunStatusCompleted, 5*time.Second)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || runner.calls[0].Prompt != "original effective prompt" {
		t.Fatalf("calls=%+v", runner.calls)
	}
}
