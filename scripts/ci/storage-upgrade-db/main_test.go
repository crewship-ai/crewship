package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkerRequiresExistingDatabase(t *testing.T) {
	for _, mode := range []string{"seed", "check"} {
		path := filepath.Join(t.TempDir(), "missing.db")
		if err := run([]string{mode, path, "fixture"}); err == nil {
			t.Fatal("missing database accepted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("missing database was created: %v", err)
		}
	}
}

func TestMarkerSurvivesWithoutReplacingApplicationData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE application_data (value TEXT); INSERT INTO application_data VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"seed", path, "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", path, "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"check", path, "other"}); err == nil {
		t.Fatal("wrong marker accepted")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow("SELECT value FROM application_data").Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("application data lost: %q %v", value, err)
	}
}
