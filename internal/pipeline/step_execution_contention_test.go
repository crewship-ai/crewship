package pipeline

import (
	"testing"
	"time"
)

// A completed action must get the database's normal write budget to record
// its result. Repeating the action is not a substitute for saving its output.
func TestExecutionResultSurvivesTemporaryPoolContention(t *testing.T) {
	_, db := openRunsTestDB(t)
	t.Cleanup(func() { db.Close() })
	installExecutionSchema(t, db)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO pipeline_runs (id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES ('run','ws_runs','pln_a','test','running','2026-09-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := NewExecutionStore(db)
	ctx, id, err := store.start(t.Context(), "run", "apply", "http", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Exhaust the pool, as concurrent requests waiting for SQLite's writer
	// can do. WaitCount is the rendezvous: the finisher must actually wait.
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waitingBefore := db.Stats().WaitCount
	done := make(chan error, 1)
	go func() { done <- store.finish(ctx, id, `{"receipt":"already-applied"}`, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().WaitCount == waitingBefore && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == waitingBefore {
		t.Fatal("result writer did not reach the exhausted pool")
	}
	// Longer than the former five-second result deadline, shorter than the
	// production database's thirty-second contention budget.
	time.Sleep(6 * time.Second)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("completed action lost its result to transient contention: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("result writer did not finish after the pool was released")
	}
	var status, output string
	if err := db.QueryRowContext(t.Context(), `SELECT status,output FROM pipeline_step_executions WHERE id=?`, id).Scan(&status, &output); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || output != `{"receipt":"already-applied"}` {
		t.Fatalf("recorded result: %s %q", status, output)
	}
}
