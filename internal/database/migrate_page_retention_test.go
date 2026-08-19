package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigratePageRetentionDays covers 20260812180544_page_retention.sql:
// workspaces.page_retention_days exists after Migrate, is NULL for a fresh row
// (meaning "use pages.DefaultPageRetentionDays"), and accepts a per-workspace
// override.
//
// Deliberately the same shape as TestMigrateV158_RunRetentionDays, because
// docs/prd/pages.md §10b.3 asks for the same convention: a nullable INTEGER on
// workspaces, NULL = instance default. If this test and that one ever have to
// differ, the convention has been broken.
func TestMigratePageRetentionDays(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "page_retention.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_pages', 'Pages', 'pages')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	var retention sql.NullInt64
	if err := db.QueryRow(`SELECT page_retention_days FROM workspaces WHERE id = 'ws_pages'`).Scan(&retention); err != nil {
		t.Fatalf("select page_retention_days: %v", err)
	}
	if retention.Valid {
		t.Errorf("page_retention_days for a fresh row = %v, want NULL — an unset column must mean "+
			"'no opinion', so the product default can move without rewriting every row", retention.Int64)
	}

	if _, err := db.Exec(`UPDATE workspaces SET page_retention_days = 3 WHERE id = 'ws_pages'`); err != nil {
		t.Fatalf("update page_retention_days: %v", err)
	}
	if err := db.QueryRow(`SELECT page_retention_days FROM workspaces WHERE id = 'ws_pages'`).Scan(&retention); err != nil {
		t.Fatalf("re-select page_retention_days: %v", err)
	}
	if !retention.Valid || retention.Int64 != 3 {
		t.Errorf("page_retention_days after override = %v, want 3", retention)
	}
}
