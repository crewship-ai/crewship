package pipeline

import (
	"testing"
)

func TestAutonomousTriggersRespectHumanAuthorAccount(t *testing.T) {
	for _, source := range []TriggeredVia{TriggeredViaSchedule, TriggeredViaWebhook, TriggeredViaWakeCheck} {
		for _, suspended := range []bool{false, true} {
			t.Run(string(source)+map[bool]string{false: "/active", true: "/suspended"}[suspended], func(t *testing.T) {
				db := openFactoryTestDB(t)
				defer db.Close()
				mustExec(t, db, `INSERT INTO users(id) VALUES('author');CREATE TABLE workspace_members(workspace_id TEXT,user_id TEXT,access_mode TEXT);INSERT INTO workspace_members VALUES('ws_test','author','trusted')`)
				runner := newMockRunner()
				deps := fullExecutorDeps(t, db, runner)
				p := saveResumePipeline(t, deps.Store, "trigger-author", resumeLinearDSL)
				if _, err := db.Exec(`UPDATE pipelines SET author_user_id='author' WHERE id=?`, p.ID); err != nil {
					t.Fatal(err)
				}
				if suspended {
					mustExec(t, db, `UPDATE users SET suspended_at='2026-10-14T00:00:00Z' WHERE id='author'`)
				}
				executor := NewWiredExecutor(deps)
				result, err := executor.Run(t.Context(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, TriggeredVia: source})
				runner.mu.Lock()
				defer runner.mu.Unlock()
				if suspended {
					if err == nil || len(runner.calls) != 0 {
						t.Fatalf("suspended author dispatched: %+v %v starts=%d", result, err, len(runner.calls))
					}
				} else if err != nil || result == nil || result.Status != "COMPLETED" || len(runner.calls) != 3 {
					t.Fatalf("active author: %+v %v starts=%d", result, err, len(runner.calls))
				}
			})
		}
	}
}
