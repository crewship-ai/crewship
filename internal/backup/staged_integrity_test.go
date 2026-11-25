package backup

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagedIntegrityChecksConstraintsWithWritesDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staged.db")
	source, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec("CREATE TABLE sample(value INTEGER CHECK(value > 0)); PRAGMA ignore_check_constraints=ON; INSERT INTO sample VALUES(-1)"); err != nil {
		source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openStagedIntegrityDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE sample SET value=1"); err == nil {
		t.Fatal("verification connection accepted a write")
	}
	var value int
	if err := db.QueryRow("SELECT value FROM sample").Scan(&value); err != nil || value != -1 {
		t.Fatalf("staged value = %d, err = %v", value, err)
	}
	if err := checkRecoveryIntegrity(t.Context(), db); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed in sample") {
		t.Fatalf("expected the CHECK violation in the staged row, got %v", err)
	}
}
