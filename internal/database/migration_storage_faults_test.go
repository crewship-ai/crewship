package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationStorageFailureRollsBackDataAndSchema(t *testing.T) {
	type migrationFunc func(context.Context, *sql.Tx, *slog.Logger) error
	cases := []struct {
		name       string
		run        migrationFunc
		operations []string
	}{
		{"network default", migrateBackfillNetworkModeRestricted, []string{
			"UPDATE crews SET network_mode", "SELECT sql FROM sqlite_master", "PRAGMA schema_version", "PRAGMA writable_schema = ON", "UPDATE sqlite_master SET sql", "PRAGMA schema_version =", "PRAGMA foreign_key_check",
		}},
		{"timestamp defaults", migrationConvertDatetimeNowDefaults, []string{
			"SELECT name FROM sqlite_master", "SELECT name, type FROM pragma_table_info", "SELECT sql FROM sqlite_master", "PRAGMA schema_version", "PRAGMA writable_schema = ON", "UPDATE sqlite_master SET sql", "PRAGMA schema_version =", "UPDATE \"crews\"", "PRAGMA foreign_key_check",
		}},
		{"legacy timestamp backfill", migrationBackfillLegacyTimestamps, []string{
			"SELECT name FROM sqlite_master", "SELECT name, type FROM pragma_table_info", "UPDATE \"crews\"",
		}},
	}
	for _, scenario := range cases {
		for _, operation := range scenario.operations {
			t.Run(scenario.name+"/"+operation, func(t *testing.T) {
				normalized := func(s string) string { return strings.Join(strings.Fields(s), " ") }
				connector := &journalFaultConnector{path: filepath.Join(t.TempDir(), "migration.db"), match: func(q string) bool { return strings.HasPrefix(normalized(q), operation) }}
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer db.Close()
				const ddl = `CREATE TABLE crews(id TEXT PRIMARY KEY, network_mode TEXT NOT NULL DEFAULT 'free', created_at TEXT DEFAULT (datetime('now')))`
				if _, err := db.Exec(ddl + `; INSERT INTO crews VALUES('crew','free','2025-01-02 03:04:05')`); err != nil {
					t.Fatal(err)
				}
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				connector.mu.Lock()
				connector.armed = true
				connector.mu.Unlock()
				err = scenario.run(context.Background(), tx, slog.New(slog.NewTextHandler(io.Discard, nil)))
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					t.Fatal(rollbackErr)
				}
				connector.mu.Lock()
				hits := connector.hits
				connector.mu.Unlock()
				if hits != 1 {
					t.Fatalf("fixture never reached operation %q; migration returned %v", operation, err)
				}
				if !errors.Is(err, errJournalStorageFault) {
					t.Fatalf("storage failure was hidden: %v", err)
				}
				var actualDDL, mode, created string
				if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='crews'`).Scan(&actualDDL); err != nil || actualDDL != ddl {
					t.Fatalf("schema escaped rollback: %q %v", actualDDL, err)
				}
				if err := db.QueryRow(`SELECT network_mode,created_at FROM crews WHERE id='crew'`).Scan(&mode, &created); err != nil || mode != "free" || created != "2025-01-02 03:04:05" {
					t.Fatalf("data escaped rollback: %q %q %v", mode, created, err)
				}
				var writable int
				if err := db.QueryRow(`PRAGMA writable_schema`).Scan(&writable); err != nil || writable != 0 {
					t.Fatalf("writable schema escaped into pool: %d %v", writable, err)
				}
			})
		}
	}
}
