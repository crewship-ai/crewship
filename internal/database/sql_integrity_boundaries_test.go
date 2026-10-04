package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestSearchCasefoldMatchesUnicodeAndNullSemantics(t *testing.T) {
	db := migrationBoundaryDB(t)
	for _, tc := range []struct {
		input any
		want  string
	}{{"PŘÍLIŠ ŽLUŤOUČKÝ", "příliš žluťoučký"}, {"ÉCOLE Ω", "école ω"}, {nil, ""}, {int64(123), ""}, {"already lower", "already lower"}} {
		var got string
		if err := db.QueryRow(`SELECT crewship_casefold(?)`, tc.input).Scan(&got); err != nil || got != tc.want {
			t.Errorf("casefold(%v) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
}

func TestLedgerRepairWriteRefusalRollsBackEveryMove(t *testing.T) {
	for _, stage := range []string{"park", "renumber", "commit"} {
		t.Run(stage, func(t *testing.T) {
			db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`, `CREATE TABLE _migrations(version INTEGER PRIMARY KEY, name TEXT, applied_at TEXT); INSERT INTO _migrations VALUES(10,'first','kept'),(20,'second','kept')`)
			var trigger string
			switch stage {
			case "park":
				trigger = `CREATE TRIGGER refuse_move BEFORE UPDATE ON _migrations WHEN NEW.version = -20 BEGIN SELECT RAISE(ABORT,'refused park'); END`
			case "renumber":
				trigger = `CREATE TRIGGER refuse_move BEFORE UPDATE ON _migrations WHEN NEW.version = 40 BEGIN SELECT RAISE(ABORT,'refused renumber'); END`
			case "commit":
				trigger = `CREATE TABLE child(version INTEGER REFERENCES _migrations(version) DEFERRABLE INITIALLY DEFERRED); INSERT INTO child VALUES(10)`
			}
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			err := ApplyLedgerRepair(context.Background(), db, RepairPlan{Renumbers: []Renumber{{Name: "first", From: 10, To: 30}, {Name: "second", From: 20, To: 40}}})
			if err == nil || !strings.Contains(err.Error(), stage) {
				t.Fatalf("error=%v, want %s", err, stage)
			}
			rows, err := ReadLedger(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 || rows[0] != (LedgerEntry{Version: 10, Name: "first", AppliedAt: "kept"}) || rows[1] != (LedgerEntry{Version: 20, Name: "second", AppliedAt: "kept"}) {
				t.Fatalf("failed repair changed ledger: %+v", rows)
			}
		})
	}
}

func TestLedgerReadRejectsMalformedSchemaAndRows(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{`CREATE TABLE _migrations(unrelated TEXT)`, "read _migrations"},
		{`CREATE TABLE _migrations(version TEXT,name TEXT,applied_at TEXT); INSERT INTO _migrations VALUES('not-an-integer','name','date')`, "scan _migrations row"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			db := migrationBoundaryDB(t, tc.schema)
			if _, err := ReadLedger(context.Background(), db); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRegistryRefusesLedgerWriteWithoutCommittingSchema(t *testing.T) {
	for _, transactional := range []bool{true, false} {
		t.Run(map[bool]string{true: "transactional", false: "self-managed"}[transactional], func(t *testing.T) {
			db := migrationBoundaryDB(t, `CREATE TABLE _migrations(version INTEGER PRIMARY KEY,name TEXT,applied_at TEXT); CREATE TRIGGER refuse_ledger BEFORE INSERT ON _migrations BEGIN SELECT RAISE(ABORT,'ledger write refused'); END`)
			m := migration{version: 20261003000001, name: "example", sql: `CREATE TABLE new_state(id TEXT)`}
			if !transactional {
				m.sql = ""
				m.fnNoTx = func(ctx context.Context, db *sql.DB, _ *slog.Logger) error {
					_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS new_state(id TEXT)`)
					return err
				}
			}
			err := applyRegistry(context.Background(), db, []migration{m}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil || !strings.Contains(err.Error(), "record migration") {
				t.Fatalf("error=%v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM _migrations`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed migration was recorded: %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='new_state'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if transactional && count != 0 || !transactional && count != 1 {
				t.Fatalf("unexpected schema after failure: %d", count)
			}
			if _, err := db.Exec(`DROP TRIGGER refuse_ledger`); err != nil {
				t.Fatal(err)
			}
			if err := applyRegistry(context.Background(), db, []migration{m}, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM _migrations`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("retry ledger: %d %v", count, err)
			}
		})
	}
}

func TestNetworkRestrictionMigrationRefusesBrokenForeignKeys(t *testing.T) {
	for _, defaultMode := range []string{"free", "restricted"} {
		t.Run(defaultMode, func(t *testing.T) {
			db := migrationBoundaryDB(t, `CREATE TABLE parents(id TEXT PRIMARY KEY)`, `CREATE TABLE crews(id TEXT PRIMARY KEY, network_mode TEXT NOT NULL DEFAULT '`+defaultMode+`', parent_id TEXT REFERENCES parents(id)); INSERT INTO crews(id,network_mode,parent_id) VALUES('crew','free','missing')`)
			rejectedMigration(t, db, migrateBackfillNetworkModeRestricted, "foreign_key_check")
			var mode string
			if err := db.QueryRow(`SELECT network_mode FROM crews WHERE id='crew'`).Scan(&mode); err != nil || mode != "free" {
				t.Fatalf("failed integrity gate committed backfill: %q %v", mode, err)
			}
			var ddl string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='crews'`).Scan(&ddl); err != nil || !strings.Contains(ddl, "DEFAULT '"+defaultMode+"'") {
				t.Fatalf("failed gate changed default: %q %v", ddl, err)
			}
			var writable int
			if err := db.QueryRow(`PRAGMA writable_schema`).Scan(&writable); err != nil || writable != 0 {
				t.Fatalf("writable_schema leaked: %d %v", writable, err)
			}
		})
	}
}

func TestMissingDefaultRewriteResetsWritableSchema(t *testing.T) {
	db := migrationBoundaryDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if ddl, err := tableCreateSQL(context.Background(), tx, "missing"); err != nil || ddl != "" {
		t.Fatalf("missing table=%q %v", ddl, err)
	}
	if err := rewriteTableDefaultLiteral(context.Background(), tx, "missing"); err == nil || !strings.Contains(err.Error(), "rewrote 0") {
		t.Fatalf("rewrite=%v", err)
	}
	var writable int
	if err := tx.QueryRow(`PRAGMA writable_schema`).Scan(&writable); err != nil || writable != 0 {
		t.Fatalf("writable_schema leaked: %d %v", writable, err)
	}
}
