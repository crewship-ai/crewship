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
