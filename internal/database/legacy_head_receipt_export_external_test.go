package database_test

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

func TestLegacyReceiptExportGenuineOldRegistryAbsence(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old-prefix.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = database.LegacyReceiptOldPrefixForTest(t.Context(), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('old-export-w','Old','old-export-w')`); err != nil {
		t.Fatal(err)
	}
	var exists int
	if err = db.QueryRow(`SELECT COUNT(*) FROM main.sqlite_master WHERE name='pipeline_legacy_head_receipts'`).Scan(&exists); err != nil || exists != 0 {
		t.Fatalf("not genuinely old: %d %v", exists, err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), db, "old-export-w")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dump.Tables["pipeline_legacy_head_receipts"]; ok {
		t.Fatal("old source gained receipt key")
	}
}
