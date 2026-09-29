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

type capabilityRevoker struct {
	*mockRunner
	db *sql.DB
}

func (r *capabilityRevoker) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	out, err := r.mockRunner.RunStep(ctx, req)
	if err == nil {
		_, err = r.db.ExecContext(ctx, `UPDATE workspace_members SET capabilities='[]' WHERE user_id='human'`)
	}
	return out, err
}

func TestInvocationAuthorityPolicy(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,role TEXT,capabilities TEXT,access_mode TEXT NOT NULL DEFAULT 'trusted')`)
	check := NewInvocationAuthorityChecker(db)
	for _, tc := range []struct {
		name, role, authority string
		caps                  any
		allow                 bool
	}{
		{"manager", "MANAGER", RoutineRunAuthority, "[]", true},
		{"capability", "MEMBER", RoutineRunAuthority, `["routine.run"]`, true},
		{"viewer-capability", "VIEWER", RoutineRunAuthority, `["routine.run"]`, true},
		{"empty", "MEMBER", RoutineRunAuthority, "[]", false},
		{"null", "MEMBER", RoutineRunAuthority, nil, false},
		{"malformed", "MEMBER", RoutineRunAuthority, `{"routine.run":true}`, false},
		{"batch-manager", "MANAGER", RoutineBatchAuthority, "[]", true},
		{"batch-capability-insufficient", "MEMBER", RoutineBatchAuthority, `["routine.run"]`, false},
		{"unknown-policy", "OWNER", "page_action", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, db, `DELETE FROM workspace_members`)
			if _, err := db.Exec(`INSERT INTO workspace_members(workspace_id,user_id,role,capabilities) VALUES('ws','human',?,?)`, tc.role, tc.caps); err != nil {
				t.Fatal(err)
			}
			in := RunInput{WorkspaceID: "ws", InvokingUserID: "human", InvocationAuthority: tc.authority}
			err := check(t.Context(), in)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v err=%v", tc.allow, err)
			}
			in.WorkspaceID = "other"
			if !errors.Is(check(t.Context(), in), ErrInvocationAuthorityRevoked) {
				t.Fatal("cross-workspace allowed")
			}
			in.WorkspaceID = "ws"
			in.InvokingUserID = ""
			if !errors.Is(check(t.Context(), in), ErrInvocationAuthorityRevoked) {
				t.Fatal("actor missing allowed")
			}
		})
	}
}

func TestInvocationAuthorityRestrictedCannotUseLegacyRun(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,role TEXT,capabilities TEXT,access_mode TEXT)`)
	mustExec(t, db, `INSERT INTO workspace_members VALUES('w','human','MANAGER','["routine.run"]','restricted')`)
	check := NewInvocationAuthorityChecker(db)
	for _, authority := range []string{"", RoutineRunAuthority, RoutineBatchAuthority} {
		if err := check(t.Context(), RunInput{WorkspaceID: "w", InvokingUserID: "human", InvocationAuthority: authority}); !errors.Is(err, ErrInvocationAuthorityRevoked) {
			t.Errorf("legacy authority %q: %v", authority, err)
		}
	}
}

func TestInvocationAuthorityStopsAfterRevocation(t *testing.T) {
	for _, change := range []string{"capability", "role", "resume"} {
		t.Run(change, func(t *testing.T) {
			db := openFactoryTestDB(t)
			defer db.Close()
			mustExec(t, db, `CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,role TEXT,capabilities TEXT,access_mode TEXT NOT NULL DEFAULT 'trusted')`)
			mustExec(t, db, `INSERT INTO workspace_members(workspace_id,user_id,role,capabilities) VALUES('ws_test','human','MEMBER','["routine.run"]')`)
			mock := newMockRunner()
			deps := fullExecutorDeps(t, db, &capabilityRevoker{mock, db})
			p := saveResumePipeline(t, deps.Store, "authority", resumeLinearDSL)
			in := RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", InvokingUserID: "human", InvocationAuthority: RoutineRunAuthority, Mode: ModeRun, MetadataJSON: `{"source":"page_action","invocation_authority":""}`}
			exec := NewWiredExecutor(deps)
			if change == "role" {
				mustExec(t, db, `UPDATE workspace_members SET role='MEMBER', capabilities='[]'`)
				in.InvocationAuthority = RoutineBatchAuthority
				if _, err := exec.Run(t.Context(), in); !errors.Is(err, ErrInvocationAuthorityRevoked) {
					t.Fatalf("demoted batch allowed: %v", err)
				}
			} else if change == "resume" {
				runs := NewRunStore(db)
				insertInFlightRun(t, runs, &RunRecord{ID: "revoked", WorkspaceID: in.WorkspaceID, PipelineID: p.ID, PipelineSlug: p.Slug, Status: RunStatusRunning, Mode: ModeRun, CurrentStepID: "b", StepOutputsJSON: `{"a":"done"}`, InvokingUserID: in.InvokingUserID, InvocationAuthority: in.InvocationAuthority})
				mustExec(t, db, `UPDATE workspace_members SET capabilities='[]'`)
				if _, _, err := exec.WithRunStore(runs).ResumeInterruptedRuns(t.Context(), slog.Default()); err != nil {
					t.Fatal(err)
				}
				rec := waitForRunStatus(t, runs, "revoked", RunStatusInterrupted, 5*time.Second)
				if !strings.Contains(rec.ErrorMessage, ErrInvocationAuthorityRevoked.Error()) {
					t.Fatal(rec.ErrorMessage)
				}
			} else {
				res, err := exec.Run(t.Context(), in)
				if err != nil || res == nil || res.Status != "FAILED" || len(mock.calls) != 1 {
					t.Fatalf("result=%+v err=%v calls=%d", res, err, len(mock.calls))
				}
				return
			}
			mock.mu.Lock()
			defer mock.mu.Unlock()
			if len(mock.calls) != 0 {
				t.Fatal("revoked actor executed")
			}
		})
	}
}

func TestInvocationAuthorityCoalescesWithActor(t *testing.T) {
	store := NewPendingRunStore(newPendingDB(t))
	now := time.Now()
	pr := PendingRun{ID: "first", WorkspaceID: "ws", PipelineID: "pipeline", PipelineSlug: "routine", FireAt: now.Add(-time.Minute), DebounceKey: "same", InvokingUserID: "alice", InvocationAuthority: RoutineBatchAuthority}
	if _, _, err := store.Enqueue(t.Context(), pr); err != nil {
		t.Fatal(err)
	}
	pr.ID = "second"
	pr.InvokingUserID = "bob"
	pr.InvocationAuthority = RoutineRunAuthority
	if _, coalesced, err := store.Enqueue(t.Context(), pr); err != nil || !coalesced {
		t.Fatalf("coalesce: %v %v", coalesced, err)
	}
	due, err := store.DueRuns(t.Context(), now, 10)
	if err != nil || len(due) != 1 || due[0].InvocationAuthority != RoutineRunAuthority {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	claimed, err := store.ClaimDue(t.Context(), "first", now)
	if err != nil || claimed == nil || claimed.InvokingUserID != "bob" || claimed.InvocationAuthority != RoutineRunAuthority {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	nested := buildNestedRunInput(RunInput{InvocationAuthority: RoutineRunAuthority, InvokingUserID: "bob"}, &Pipeline{}, &DSL{}, nil, "run", 0, nil, 1)
	if nested.InvocationAuthority != RoutineRunAuthority || nested.InvokingUserID != "bob" {
		t.Fatal("nested authority lost")
	}
}
