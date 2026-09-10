package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
)

func installExecutionSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	body, err := os.ReadFile("../database/migrations/20260908104844_pipeline_step_executions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
}

func TestN9ArtifactFailureDoesNotFailSuccessfulWork(t *testing.T) {
	db := openResumeTestDB(t)
	t.Cleanup(func() { db.Close() })
	installExecutionSchema(t, db)
	store := NewStore(db)
	seedTierFallback(t, store)
	p := fakePipeline(t, "artifact-failure", `{"name":"artifact-failure","steps":[{"id":"write","type":"agent_run","agent_slug":"worker","prompt":"work"}]}`, "crew_a", "agent_lead")
	if _, err := db.Exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,workspace_visible,author_crew_id,author_agent_id,authored_via,last_test_run_at,last_test_run_passed,created_at,updated_at) VALUES(?,?,?,?,?,?,1,?,?,'agent_tool_call','2026-09-09T00:00:00Z',1,'2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')`, p.ID, p.WorkspaceID, p.Slug, p.Name, p.DefinitionJSON, p.DefinitionHash, p.AuthorCrewID, p.AuthorAgentID); err != nil {
		t.Fatal(err)
	}
	runner := newMockRunner()
	runner.outputsBySlug["worker"] = []string{"finished work"}
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).WithRunStore(NewRunStore(db)).WithExecutionStore(NewExecutionStore(db))
	calls := 0
	exec.executionStore.publisher = ArtifactPublishFunc(func(context.Context, string, string, string, string, string, string) error {
		calls++
		return errors.New("artifact storage unavailable")
	})
	res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: p.WorkspaceID, Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("successful work became %s", res.Status)
	}
	if calls != 2 {
		t.Fatalf("want agent and step publication, got %d", calls)
	}
	var completed int
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_step_executions WHERE status='completed'`).Scan(&completed); err != nil || completed != 2 {
		t.Fatalf("execution results: %d %v", completed, err)
	}
}
func TestExecutionHistory_AttemptsImmutableAndItemsDistinct(t *testing.T) {
	_, db := openRunsTestDB(t)
	defer db.Close()
	installExecutionSchema(t, db)
	_, err := db.Exec(`INSERT INTO pipeline_runs (id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES ('run','ws_runs','pln_a','test','running','2026-09-08T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewExecutionStore(db)
	ctx := context.Background()
	firstCtx, first, err := store.start(ctx, "run", "collect", "foreach", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, id, err := store.start(foreachExecutionContext(firstCtx, i), "run", "read", "script", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err = store.finish(ctx, id, "original", nil); err != nil {
			t.Fatal(err)
		}
		if err = store.finish(ctx, id, "overwritten", errors.New("late callback")); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.finish(ctx, first, "partial", errors.New("retry me")); err != nil {
		t.Fatal(err)
	}
	_, second, err := store.start(ctx, "run", "collect", "foreach", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("attempt identity reused")
	}
	if err = store.finish(ctx, second, "done", nil); err != nil {
		t.Fatal(err)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM pipeline_step_executions WHERE parent_execution_id=? AND output='original' AND status='completed'`, first).Scan(&count)
	if count != 2 {
		t.Fatalf("items/immutable outputs: %d", count)
	}
	db.QueryRow(`SELECT attempt FROM pipeline_step_executions WHERE id=?`, second).Scan(&count)
	if count != 2 {
		t.Fatalf("attempt = %d", count)
	}
	cancelledCtx, cancel := context.WithCancel(ctx)
	_, stopped, err := store.start(cancelledCtx, "run", "script", "script", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = store.finish(cancelledCtx, stopped, "partial", errors.New("script process group stopped")); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM pipeline_step_executions WHERE id=?`, stopped).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("lost cancellation: %s %v", status, err)
	}

}
