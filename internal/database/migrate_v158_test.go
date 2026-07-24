package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV158_RunRetentionDays verifies the #1407 migration:
// workspaces.run_retention_days exists post-Migrate, defaults to NULL for
// existing rows (meaning "use pipeline.DefaultRunRetentionDays"), and
// accepts a per-workspace override.
func TestMigrateV158_RunRetentionDays(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v158.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_x', 'X', 'x')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	var retention sql.NullInt64
	if err := db.QueryRow(`SELECT run_retention_days FROM workspaces WHERE id = 'ws_x'`).Scan(&retention); err != nil {
		t.Fatalf("select run_retention_days: %v", err)
	}
	if retention.Valid {
		t.Errorf("run_retention_days for a fresh row = %v, want NULL", retention.Int64)
	}

	if _, err := db.Exec(`UPDATE workspaces SET run_retention_days = 14 WHERE id = 'ws_x'`); err != nil {
		t.Fatalf("update run_retention_days: %v", err)
	}
	if err := db.QueryRow(`SELECT run_retention_days FROM workspaces WHERE id = 'ws_x'`).Scan(&retention); err != nil {
		t.Fatalf("re-select run_retention_days: %v", err)
	}
	if !retention.Valid || retention.Int64 != 14 {
		t.Errorf("run_retention_days after override = %v, want 14", retention)
	}
}
