package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type revokingMemberRunner struct {
	*mockRunner
	db *sql.DB
}

func (r *revokingMemberRunner) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	result, err := r.mockRunner.RunStep(ctx, req)
	if err == nil {
		_, err = r.db.ExecContext(ctx, `DELETE FROM workspace_members WHERE user_id='human'`)
	}
	return result, err
}

func TestInvokingUserAuthority(t *testing.T) {
	for _, scenario := range []string{"member", "removed", "other-workspace", "revoked-between-steps", "database-error", "unattended"} {
		t.Run(scenario, func(t *testing.T) {
			db := openFactoryTestDB(t)
			defer db.Close()
			mustExec(t, db, `CREATE TABLE workspace_members (workspace_id TEXT, user_id TEXT)`)
			mustExec(t, db, `INSERT INTO workspace_members VALUES ('ws_test','human')`)
			mock := newMockRunner()
			var runner AgentRunner = mock
			if scenario == "revoked-between-steps" {
				runner = &revokingMemberRunner{mock, db}
			}
			deps := fullExecutorDeps(t, db, runner)
			p := saveResumePipeline(t, deps.Store, "human-authority", resumeLinearDSL)
			in := RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", InvokingUserID: "human", Mode: ModeRun}
			switch scenario {
			case "removed":
				mustExec(t, db, `DELETE FROM workspace_members`)
			case "other-workspace":
				mustExec(t, db, `UPDATE workspace_members SET workspace_id='other'`)
			case "database-error":
				mustExec(t, db, `DROP TABLE workspace_members`)
			case "unattended":
				in.InvokingUserID = ""
				mustExec(t, db, `DELETE FROM workspace_members`)
			}
			res, err := NewWiredExecutor(deps).Run(t.Context(), in)
			switch scenario {
			case "member", "unattended":
				if err != nil || res == nil || res.Status != "COMPLETED" || len(mock.calls) != 3 {
					t.Fatalf("result=%+v err=%v calls=%d", res, err, len(mock.calls))
				}
			case "revoked-between-steps":
				if res == nil || res.Status != "FAILED" || len(mock.calls) != 1 {
					t.Fatalf("revocation did not stop remaining steps: result=%+v err=%v calls=%d", res, err, len(mock.calls))
				}
			default:
				if err == nil || len(mock.calls) != 0 {
					t.Fatalf("unauthorized execution: result=%+v err=%v calls=%d", res, err, len(mock.calls))
				}
			}
		})
	}
}

func TestInvokingUserRevokedAfterReservationReleasesIdempotencyKey(t *testing.T) {
	db := openExecutorGateDB(t)
	defer db.Close()
	store := NewStore(db)
	p, err := store.Save(t.Context(), validSaveInput("revoked-after-reservation"))
	if err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	exec := NewExecutor(store, NewResolver(db), runner, nil).
		WithIdempotencyStore(NewIdempotencyStore(db))
	checks := 0
	exec.memberCheck = func(context.Context, string, string) (bool, error) {
		checks++
		// Admission succeeds; the grant is removed before runDSL's
		// execution-time check, after the idempotency reservation.
		return checks != 2, nil
	}
	in := RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", InvokingUserID: "human",
		Mode: ModeRun, IdempotencyKey: "same-request",
	}
	if _, err := exec.Run(t.Context(), in); !errors.Is(err, ErrInvokingUserNotMember) {
		t.Fatalf("revoked run: %v", err)
	}
	var reserved int
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_run_idempotency WHERE workspace_id=? AND pipeline_id=? AND idempotency_key=?`,
		in.WorkspaceID, in.PipelineID, in.IdempotencyKey).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("revoked pre-start run left %d reservation(s)", reserved)
	}
	res, err := exec.Run(t.Context(), in)
	if err != nil || res == nil || res.Deduped {
		t.Fatalf("authorized retry must execute, got result=%+v err=%v", res, err)
	}
}

func TestInvokingUserAuthorityResumedRun(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE workspace_members (workspace_id TEXT, user_id TEXT)`)
	runner := newMockRunner()
	deps := fullExecutorDeps(t, db, runner)
	p := saveResumePipeline(t, deps.Store, "human-resume", resumeLinearDSL)
	runs := NewRunStore(db)
	insertInFlightRun(t, runs, &RunRecord{
		ID: "run_human_revoked", WorkspaceID: "ws_test", PipelineID: p.ID, PipelineSlug: p.Slug,
		Status: RunStatusRunning, Mode: ModeRun, CurrentStepID: "b", StepOutputsJSON: `{"a":"already completed"}`, InvokingUserID: "removed-human",
	})
	exec := NewWiredExecutor(deps).WithRunStore(runs)
	if _, _, err := exec.ResumeInterruptedRuns(t.Context(), slog.Default()); err != nil {
		t.Fatal(err)
	}
	rec := waitForRunStatus(t, runs, "run_human_revoked", RunStatusInterrupted, 5*time.Second)
	if !strings.Contains(rec.ErrorMessage, ErrInvokingUserNotMember.Error()) {
		t.Fatalf("unexpected reason: %s", rec.ErrorMessage)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Fatalf("resumed revoked user's work: %d calls", len(runner.calls))
	}
}

func TestInvokingUserAuthorityNestedAndHooks(t *testing.T) {
	e := &Executor{memberCheck: func(context.Context, string, string) (bool, error) { return false, nil }}
	in := RunInput{WorkspaceID: "ws", InvokingUserID: "removed", Mode: ModeRun}
	// Denial must precede DSL processing and any HTTP/code hook dispatch.
	if _, err := e.runDSL(t.Context(), in, 1); !errors.Is(err, ErrInvokingUserNotMember) {
		t.Fatalf("nested run: %v", err)
	}
	for _, kind := range []StepType{StepHTTP, StepCode, StepTransform} {
		if _, err := e.dispatchHookStep(t.Context(), &Step{Type: kind}, in, RenderContext{}, "test hook"); !errors.Is(err, ErrInvokingUserNotMember) {
			t.Fatalf("hook %s: %v", kind, err)
		}
	}
}
