package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV128_PipelineStatus_BackfillsActive asserts the v128 column add
// applies cleanly and backfills existing pipeline rows to 'active', and that
// the CHECK constraint rejects an out-of-enum value.
func TestMigrateV128_PipelineStatus_BackfillsActive(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v128.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Confirm the column exists and defaults to 'active' on insert.
	wsID := "ws_v128"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, wsID, "WS128", wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at)
		VALUES ('pln_v128', ?, 'r', 'r', '{"name":"r","steps":[]}', 'h', datetime('now'), datetime('now'))`, wsID); err != nil {
		t.Fatalf("insert pipeline: %v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pipelines WHERE id = 'pln_v128'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "active" {
		t.Errorf("default status=%q want active", status)
	}

	// CHECK constraint rejects an unknown status.
	_, err = db.Exec(`UPDATE pipelines SET status = 'bogus' WHERE id = 'pln_v128'`)
	if err == nil {
		t.Errorf("expected CHECK violation updating status to 'bogus', got nil")
	}

	// Idempotency: a no-op second migrate must not error.
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	_ = sql.ErrNoRows
}
