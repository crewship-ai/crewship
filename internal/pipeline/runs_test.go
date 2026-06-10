package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openRunsTestDB sets up the v83 schema minimally — pipeline_runs
// plus the FK targets it references. Mirrors the openWaitpointsTestDB
// pattern: pull in just what the store needs, not the full migrate
// stack.
func openRunsTestDB(t *testing.T) (*RunStore, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_runs');
CREATE TABLE pipelines (id TEXT PRIMARY KEY);
INSERT INTO pipelines (id) VALUES ('pln_a'), ('pln_b');
CREATE TABLE pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pipeline_id         TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    pipeline_slug       TEXT NOT NULL,
    pipeline_version    INTEGER,
    definition_hash     TEXT,
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    output              TEXT,
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,
    failed_at_step      TEXT,
    error_fingerprint   TEXT,
    invoking_crew_id    TEXT,
    invoking_agent_id   TEXT,
    invoking_user_id    TEXT,
    triggered_via       TEXT NOT NULL DEFAULT 'manual',
    triggered_by_id     TEXT,
    idempotency_key     TEXT,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return NewRunStore(db), db
}

func TestRunStore_InsertAndGet_RoundTrip(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	r := &RunRecord{
		ID:           "run_a",
		WorkspaceID:  "ws_runs",
		PipelineID:   "pln_a",
		PipelineSlug: "demo",
		Mode:         ModeRun,
		TriggeredVia: TriggeredViaManual,
	}
	if err := store.Insert(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := store.Get(ctx, "run_a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PipelineSlug != "demo" || got.Status != RunStatusQueued {
		t.Errorf("round-trip: status=%q slug=%q", got.Status, got.PipelineSlug)
	}
}

func TestRunStore_Get_NotFound(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	_, err := store.Get(context.Background(), "ghost")
	if !errors.Is(err, ErrRunNotFoundInStore) {
		t.Errorf("expected ErrRunNotFoundInStore, got %v", err)
	}
}

func TestRunStore_LifecycleTransitions(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{
		ID: "run_lc", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, "run_lc", "step1"); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	r, _ := store.Get(ctx, "run_lc")
	if r.Status != RunStatusRunning || r.CurrentStepID != "step1" {
		t.Errorf("running: %+v", r)
	}

	if err := store.AppendStepOutput(ctx, "run_lc",
		map[string]string{"step1": "hello world"},
		0.0023, 120); err != nil {
		t.Fatalf("append: %v", err)
	}
	r, _ = store.Get(ctx, "run_lc")
	if r.CostUSD != 0.0023 || r.DurationMs != 120 {
		t.Errorf("append cost/dur: %+v", r)
	}
	if r.StepOutputsJSON == "{}" {
		t.Errorf("step_outputs not persisted: %s", r.StepOutputsJSON)
	}

	if err := store.MarkTerminal(ctx, MarkTerminalInput{
		RunID:      "run_lc",
		Status:     RunStatusCompleted,
		Output:     "final result",
		CostUSD:    0.0050,
		DurationMs: 250,
	}); err != nil {
		t.Fatalf("terminal: %v", err)
	}
	r, _ = store.Get(ctx, "run_lc")
	if r.Status != RunStatusCompleted || r.Output != "final result" {
		t.Errorf("terminal: %+v", r)
	}
	if r.EndedAt == nil {
		t.Error("EndedAt not set on completion")
	}
}

func TestRunStore_MarkTerminal_RejectsNonTerminalStatus(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	_ = store.Insert(ctx, &RunRecord{
		ID: "run_x", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo",
	})
	err := store.MarkTerminal(ctx, MarkTerminalInput{
		RunID:  "run_x",
		Status: RunStatusRunning,
	})
	if err == nil {
		t.Error("expected error: 'running' is not a terminal status")
	}
}

func TestRunStore_ListByPipeline_OrdersByStartedDesc(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for i, dt := range []time.Duration{-3 * time.Minute, -1 * time.Minute, -2 * time.Minute} {
		r := &RunRecord{
			ID:          "run_" + string(rune('a'+i)),
			WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo",
			StartedAt: now.Add(dt),
		}
		if err := store.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListByPipeline(ctx, "pln_a", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("count: %d", len(got))
	}
	// Newest first: run_b started -1 min, run_c at -2 min, run_a at -3 min
	want := []string{"run_b", "run_c", "run_a"}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("order [%d]: got %s want %s", i, got[i].ID, w)
		}
	}
}

func TestRunStore_ListActive_FiltersByStatus(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	cases := []struct {
		id, status string
	}{
		{"r_q", "queued"},
		{"r_run", "running"},
		{"r_done", "completed"},
		{"r_fail", "failed"},
	}
	for _, c := range cases {
		_ = store.Insert(ctx, &RunRecord{
			ID: c.id, WorkspaceID: "ws_runs", PipelineID: "pln_a",
			PipelineSlug: "demo", Status: RunStatus(c.status),
		})
	}
	active, err := store.ListActive(ctx, "ws_runs")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active (queued+running), got %d", len(active))
	}
}

func TestRunStore_RecoverInterruptedAtBoot(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	// Two in-flight from a "previous lifetime", one terminal.
	_ = store.Insert(ctx, &RunRecord{
		ID: "ghost1", WorkspaceID: "ws_runs", PipelineID: "pln_a",
		PipelineSlug: "demo", Status: RunStatusRunning,
	})
	_ = store.Insert(ctx, &RunRecord{
		ID: "ghost2", WorkspaceID: "ws_runs", PipelineID: "pln_a",
		PipelineSlug: "demo", Status: RunStatusQueued,
	})
	_ = store.Insert(ctx, &RunRecord{
		ID: "real", WorkspaceID: "ws_runs", PipelineID: "pln_a",
		PipelineSlug: "demo", Status: RunStatusCompleted,
	})

	n, err := store.RecoverInterruptedAtBoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 promoted, got %d", n)
	}

	// Verify the ghosts are now interrupted, the real one untouched.
	for _, id := range []string{"ghost1", "ghost2"} {
		r, _ := store.Get(ctx, id)
		if r.Status != RunStatusInterrupted {
			t.Errorf("%s: status=%q want interrupted", id, r.Status)
		}
		if r.ErrorMessage == "" {
			t.Errorf("%s: error_message not set", id)
		}
	}
	r, _ := store.Get(ctx, "real")
	if r.Status != RunStatusCompleted {
		t.Errorf("real run was modified: %q", r.Status)
	}
}

func TestRunStore_ResolveByIdempotencyKey(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{
		ID: "r1", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "demo",
		IdempotencyKey: "key_xyz",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ResolveByIdempotencyKey(ctx, "ws_runs", "key_xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "r1" {
		t.Errorf("got %q want r1", got)
	}
	miss, err := store.ResolveByIdempotencyKey(ctx, "ws_runs", "absent")
	if err != nil || miss != "" {
		t.Errorf("miss: got=%q err=%v", miss, err)
	}
}
