package pipeline

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// openRetentionTestDB mirrors openRunsTestDB (runs_test.go) plus
// pipeline_waitpoints and a workspaces.run_retention_days column, since
// the retention sweep reads both.
func openRetentionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE workspaces (id TEXT PRIMARY KEY, run_retention_days INTEGER);
INSERT INTO workspaces (id) VALUES ('ws_a'), ('ws_b');
CREATE TABLE pipelines (id TEXT PRIMARY KEY);
INSERT INTO pipelines (id) VALUES ('pln_a'), ('pln_b');
CREATE TABLE pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    pipeline_id         TEXT NOT NULL,
    pipeline_slug       TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    is_replay           INTEGER NOT NULL DEFAULT 0,
    replay_of           TEXT,
    warnings_json       TEXT NOT NULL DEFAULT '[]',
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);
CREATE TABLE run_tags (
    run_id       TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    tag          TEXT NOT NULL,
    PRIMARY KEY (run_id, tag)
);
CREATE TABLE pipeline_waitpoints (
    token           TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL,
    pipeline_run_id TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
);`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func insertRunForRetention(t *testing.T, db *sql.DB, id, wsID, pipelineID, status, startedAt string, replayOf string) {
	t.Helper()
	var replay any
	if replayOf != "" {
		replay = replayOf
	}
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, status, started_at, replay_of)
VALUES (?, ?, ?, ?, ?, ?)`,
		id, wsID, pipelineID, status, startedAt, replay,
	); err != nil {
		t.Fatalf("insert run %s: %v", id, err)
	}
}

func runExists(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM pipeline_runs WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("check run %s: %v", id, err)
	}
	return n == 1
}

// TestSweepRunRetention_DeletesOldTerminalRuns_KeepsRecentAndProtected is the
// acceptance test from issue #1407: old terminal runs are purged; recent runs,
// in-flight runs, the keep-last-N floor, pending-waitpoint runs, and
// surviving replay parents are all retained.
func TestSweepRunRetention_DeletesOldTerminalRuns_KeepsRecentAndProtected(t *testing.T) {
	db := openRetentionTestDB(t)
	defer db.Close()
	ctx := context.Background()

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)

	// pln_a: 3 old completed runs. The keep-3 floor used below counts
	// ACROSS the whole pipeline_id including run_recent (inserted next) —
	// run_recent(rank1) + run_old_3(rank2) + run_old_2(rank3) fill the
	// floor, leaving only run_old_1 (rank4, oldest) eligible on rank alone.
	insertRunForRetention(t, db, "run_old_1", "ws_a", "pln_a", "completed", old, "")
	insertRunForRetention(t, db, "run_old_2", "ws_a", "pln_a", "completed",
		time.Now().Add(-99*24*time.Hour).UTC().Format(time.RFC3339Nano), "")
	insertRunForRetention(t, db, "run_old_3", "ws_a", "pln_a", "completed",
		time.Now().Add(-98*24*time.Hour).UTC().Format(time.RFC3339Nano), "")

	// Recent run — within the retention window, never eligible regardless
	// of rank.
	insertRunForRetention(t, db, "run_recent", "ws_a", "pln_a", "completed", recent, "")

	// In-flight run, old started_at — never eligible while non-terminal.
	insertRunForRetention(t, db, "run_inflight", "ws_a", "pln_a", "running", old, "")

	// Old run protected by a pending waitpoint.
	insertRunForRetention(t, db, "run_waiting_approval", "ws_a", "pln_a", "completed", old, "")
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, status)
VALUES ('tok_1', 'ws_a', 'run_waiting_approval', 'pending')`); err != nil {
		t.Fatalf("seed waitpoint: %v", err)
	}

	// Old run that is the replay_of target of a surviving (recent) replay —
	// must not be deleted even though it's old and beyond the floor, since
	// deleting it would dangle run_replay_child's provenance pointer.
	insertRunForRetention(t, db, "run_replayed_parent", "ws_a", "pln_b", "completed", old, "")
	insertRunForRetention(t, db, "run_replay_child", "ws_a", "pln_b", "completed", recent, "run_replayed_parent")

	// Different workspace — must never be touched by a ws_a sweep.
	insertRunForRetention(t, db, "run_other_ws", "ws_b", "pln_a", "completed", old, "")

	deleted, err := SweepRunRetention(ctx, db, nil, "ws_a", 90, 3)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only run_old_1)", deleted)
	}

	if runExists(t, db, "run_old_1") {
		t.Error("run_old_1 (oldest, beyond keep-2 floor, terminal, unprotected) should have been deleted")
	}
	for _, id := range []string{
		"run_old_2", "run_old_3", // keep-last-N floor
		"run_recent",           // within window
		"run_inflight",         // non-terminal
		"run_waiting_approval", // pending waitpoint
		"run_replayed_parent",  // replay_of target of a surviving run
		"run_replay_child",     // recent
		"run_other_ws",         // different workspace
	} {
		if !runExists(t, db, id) {
			t.Errorf("%s should have been retained but was deleted", id)
		}
	}
}

// TestSweepRunRetention_Idempotent proves re-running the sweep after a
// successful pass deletes 0 rows (per issue #1407's acceptance criterion).
func TestSweepRunRetention_Idempotent(t *testing.T) {
	db := openRetentionTestDB(t)
	defer db.Close()
	ctx := context.Background()

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	insertRunForRetention(t, db, "run_old", "ws_a", "pln_a", "completed", old, "")

	first, err := SweepRunRetention(ctx, db, nil, "ws_a", 90, 0)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if first != 1 {
		t.Fatalf("first sweep deleted = %d, want 1", first)
	}

	second, err := SweepRunRetention(ctx, db, nil, "ws_a", 90, 0)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if second != 0 {
		t.Errorf("second sweep deleted = %d, want 0 (idempotent)", second)
	}
}

// TestSweepRunRetention_EmitsJournalBreadcrumb proves the #1407 acceptance
// criterion "emit a journal breadcrumb per sweep (rows purged)".
func TestSweepRunRetention_EmitsJournalBreadcrumb(t *testing.T) {
	db := openRetentionTestDB(t)
	defer db.Close()
	ctx := context.Background()

	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	insertRunForRetention(t, db, "run_old", "ws_a", "pln_a", "completed", old, "")

	rec := &captureEmitter{}
	deleted, err := SweepRunRetention(ctx, db, rec, "ws_a", 90, 0)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if len(rec.entries) != 1 {
		t.Fatalf("journal entries emitted = %d, want 1", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Type != journal.EntryPipelineRunsSwept {
		t.Errorf("entry type = %q, want %q", e.Type, journal.EntryPipelineRunsSwept)
	}
	if e.Payload["deleted_count"] != 1 {
		t.Errorf("payload deleted_count = %v, want 1", e.Payload["deleted_count"])
	}
}

// TestSweepAllWorkspacesRunRetention_PerWorkspaceOverride proves the sweep
// honours workspaces.run_retention_days when set, and falls back to
// DefaultRunRetentionDays when NULL.
func TestSweepAllWorkspacesRunRetention_PerWorkspaceOverride(t *testing.T) {
	db := openRetentionTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// ws_a: tight 5-day override — a 10-day-old run is eligible.
	if _, err := db.ExecContext(ctx, `UPDATE workspaces SET run_retention_days = 5 WHERE id = 'ws_a'`); err != nil {
		t.Fatalf("set override: %v", err)
	}
	// ws_b: no override — falls back to the 90-day default, so the same
	// 10-day-old run must NOT be eligible.
	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	insertRunForRetention(t, db, "run_a", "ws_a", "pln_a", "completed", tenDaysAgo, "")
	insertRunForRetention(t, db, "run_b", "ws_b", "pln_a", "completed", tenDaysAgo, "")

	if err := SweepAllWorkspacesRunRetention(ctx, db, nil, 0); err != nil {
		t.Fatalf("sweep all: %v", err)
	}

	if runExists(t, db, "run_a") {
		t.Error("run_a should have been swept under ws_a's 5-day override")
	}
	if !runExists(t, db, "run_b") {
		t.Error("run_b should have survived under ws_b's 90-day default")
	}
}
