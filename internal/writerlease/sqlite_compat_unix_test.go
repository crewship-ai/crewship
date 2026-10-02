//go:build linux || darwin

package writerlease

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLeaseAllowsSQLiteTransactionsAcrossConnectionCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owner, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(100)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Force every database/sql operation to close its SQLite descriptor.
	// A process-scoped lock would disappear on the first close.
	db.SetMaxIdleConns(0)
	if _, err = db.Exec("CREATE TABLE evidence (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO evidence(id) VALUES (?)", i); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		competing, err := Acquire(path)
		if competing != nil {
			competing.Close()
		}
		if !errors.Is(err, ErrHeld) {
			t.Fatalf("ownership lost after SQLite connection close at write %d: %v", i, err)
		}
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM evidence").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatalf("writes = %d", count)
	}
	if err = owner.Verify(); err != nil {
		t.Fatal(err)
	}
}
