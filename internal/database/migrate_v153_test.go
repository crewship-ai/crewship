package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigrationV153_WidensIdempotencyPKToPipelineScope lands the
// pre-v153 schema (PK = workspace_id, idempotency_key), seeds a row,
// applies v153, and asserts:
//   - the pre-existing row survived the table rebuild intact
//   - two different pipelines can now hold rows with the SAME
//     (workspace_id, idempotency_key) — the collision the migration
//     exists to close (#1415)
//   - the old 2-column uniqueness is gone: a fresh INSERT under the
//     OLD PK shape would have collided; under the new PK it does not
func TestMigrationV153_WidensIdempotencyPKToPipelineScope(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := applyMigrationsUpTo(ctx, db, 152, logger); err != nil {
		t.Fatalf("migrate to 152: %v", err)
	}

	// Pre-existing row under the OLD (workspace_id, idempotency_key) PK.
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_run_idempotency
  (workspace_id, idempotency_key, run_id, pipeline_id, expires_at)
VALUES ('ws_a', 'order-123', 'run_legacy', 'pipe_legacy', '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	m153, err := findMigration(153)
	if err != nil {
		t.Fatalf("find v153: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(ctx, m153.sql); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v153: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v153: %v", err)
	}

	// The legacy row survived the rebuild with all columns intact.
	var runID, pipelineID string
	if err := db.QueryRowContext(ctx,
		`SELECT run_id, pipeline_id FROM pipeline_run_idempotency WHERE workspace_id = 'ws_a' AND idempotency_key = 'order-123' AND pipeline_id = 'pipe_legacy'`,
	).Scan(&runID, &pipelineID); err != nil {
		t.Fatalf("legacy row missing after migration: %v", err)
	}
	if runID != "run_legacy" || pipelineID != "pipe_legacy" {
		t.Fatalf("legacy row corrupted: run_id=%q pipeline_id=%q", runID, pipelineID)
	}

	// A DIFFERENT pipeline can now reuse the SAME (workspace_id,
	// idempotency_key) pair without violating the PK — this would have
	// failed under the pre-v153 schema.
	if _, err := db.ExecContext(ctx, `
INSERT INTO pipeline_run_idempotency
  (workspace_id, idempotency_key, run_id, pipeline_id, expires_at)
VALUES ('ws_a', 'order-123', 'run_other_pipeline', 'pipe_other', '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("expected independent insert under new PK to succeed, got: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_run_idempotency WHERE workspace_id = 'ws_a' AND idempotency_key = 'order-123'`,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 independent rows sharing (workspace_id, idempotency_key), got %d", count)
	}

	// A genuine duplicate WITHIN the same pipeline must still violate the
	// (now three-column) PK — same-pipeline dedup guarantee is preserved.
	_, err = db.ExecContext(ctx, `
INSERT INTO pipeline_run_idempotency
  (workspace_id, idempotency_key, run_id, pipeline_id, expires_at)
VALUES ('ws_a', 'order-123', 'run_duplicate', 'pipe_legacy', '2099-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatalf("expected PK violation for a true same-pipeline duplicate, insert succeeded")
	}

	// Supporting index survived the rebuild.
	var idxCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_pipeline_run_idempotency_expires'`,
	).Scan(&idxCount); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idxCount != 1 {
		t.Fatalf("expected idx_pipeline_run_idempotency_expires to exist after rebuild, got count=%d", idxCount)
	}
}
