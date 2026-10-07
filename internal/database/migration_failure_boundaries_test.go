package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func migrationBoundaryDB(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func rejectedMigration(t *testing.T, db *sql.DB, run func(context.Context, *sql.Tx, *slog.Logger) error, want string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = run(context.Background(), tx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("migration error = %v, want %q", err, want)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestRunOutputMigrationRejectsBrokenHistoryAtomically(t *testing.T) {
	for _, tc := range []struct{ name, setup, want string }{
		{"missing source", "", "backfill: query"},
		{"unreadable timestamp", `CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY, step_outputs_json TEXT, updated_at TEXT); INSERT INTO pipeline_runs VALUES ('r', '{"step":"kept"}', NULL)`, "backfill: scan"},
		{"target insert refused", `CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY, step_outputs_json TEXT, updated_at TEXT); INSERT INTO pipeline_runs VALUES ('r', '{"step":"kept"}', ''); CREATE TABLE pipeline_run_step_outputs(run_id TEXT, step_id TEXT, output TEXT, updated_at TEXT, PRIMARY KEY(run_id,step_id)); CREATE TRIGGER refuse_output BEFORE INSERT ON pipeline_run_step_outputs BEGIN SELECT RAISE(ABORT,'disk policy refused output'); END`, "insert run r step step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var setup []string
			if tc.setup != "" {
				setup = append(setup, tc.setup)
			}
			db := migrationBoundaryDB(t, setup...)
			rejectedMigration(t, db, migrationRunStepOutputs, tc.want)
			if tc.name == "target insert refused" {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM pipeline_run_step_outputs`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial output rows = %d, %v", count, err)
				}
			} else {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='pipeline_run_step_outputs'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed migration leaked target table: %d, %v", count, err)
				}
			}
			if tc.name != "missing source" {
				var original string
				if err := db.QueryRow(`SELECT step_outputs_json FROM pipeline_runs WHERE id='r'`).Scan(&original); err != nil || original != `{"step":"kept"}` {
					t.Fatalf("original history = %q, %v", original, err)
				}
			}
		})
	}
}

func TestRunOutputBackfillRejectsMalformedTarget(t *testing.T) {
	db := migrationBoundaryDB(t, `CREATE TABLE pipeline_runs(id TEXT, step_outputs_json TEXT, updated_at TEXT)`, `CREATE TABLE pipeline_run_step_outputs(unrelated TEXT)`)
	rejectedMigration(t, db, backfillRunStepOutputs, "backfill: prepare")
}

func TestNotificationMigrationDDLFailureKeepsOriginalSchema(t *testing.T) {
	db := migrationBoundaryDB(t, `CREATE TABLE notification_channels(id TEXT PRIMARY KEY, provider TEXT, type TEXT)`)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='notification_channels'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rejectedMigration(t, db, migrationNotificationPrefs, "ddl: ALTER TABLE notification_channels ADD COLUMN provider")
	var after string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='notification_channels'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("refused migration changed original schema")
	}
	for _, input := range []struct{ source, want string }{{"  CREATE TABLE t (\n id TEXT\n)", "CREATE TABLE t ("}, {"  DROP TABLE t  ", "DROP TABLE t"}} {
		if got := firstLine(input.source); got != input.want {
			t.Errorf("diagnostic first line = %q, want %q", got, input.want)
		}
	}
}

func TestTaxonomyFailureRollsBackPreferenceAndSchemaChanges(t *testing.T) {
	for _, failure := range []string{"preference update", "split insert", "channel update", "missing channels"} {
		t.Run(failure, func(t *testing.T) {
			db := openV169FixtureDB(t)
			for _, statement := range []string{
				`INSERT INTO user_notification_prefs (id,workspace_id,user_id,category,channel_id,state) VALUES ('pref','ws','user','system','channel','off')`,
				`INSERT INTO notification_channels(id,categories_json) VALUES ('channel','["system"]')`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			var statement, want string
			switch failure {
			case "preference update":
				statement = `CREATE TRIGGER refuse_pref BEFORE UPDATE ON user_notification_prefs BEGIN SELECT RAISE(ABORT,'refused'); END`
				want = "remap pref pref"
			case "split insert":
				statement = `CREATE TRIGGER refuse_split BEFORE INSERT ON user_notification_prefs BEGIN SELECT RAISE(ABORT,'refused'); END`
				want = "split pref pref"
			case "channel update":
				statement = `CREATE TRIGGER refuse_channel BEFORE UPDATE ON notification_channels BEGIN SELECT RAISE(ABORT,'refused'); END`
				want = "update allowlist for channel"
			case "missing channels":
				statement = `DROP TABLE notification_channels`
				want = "read channel allowlists"
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			rejectedMigration(t, db, migrationNotifyTaxonomy, want)
			var category, state string
			if err := db.QueryRow(`SELECT category,state FROM user_notification_prefs WHERE id='pref'`).Scan(&category, &state); err != nil || category != "system" || state != "off" {
				t.Fatalf("operator preference changed: %s %s, %v", category, state, err)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM user_notification_prefs`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("split escaped rollback: %d, %v", count, err)
			}
			var ddl string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='user_notification_prefs'`).Scan(&ddl); err != nil || !strings.Contains(ddl, prefsCategoryCheckOld) {
				t.Fatalf("schema rewrite escaped rollback: %v", err)
			}
			if failure != "missing channels" {
				var categories string
				if err := db.QueryRow(`SELECT categories_json FROM notification_channels WHERE id='channel'`).Scan(&categories); err != nil || categories != `["system"]` {
					t.Fatalf("allowlist changed: %q, %v", categories, err)
				}
			}
		})
	}
}

func TestMigrationsRejectFinishedTransactions(t *testing.T) {
	db := migrationBoundaryDB(t)
	for name, run := range map[string]func(context.Context, *sql.Tx, *slog.Logger) error{
		"run outputs":              migrationRunStepOutputs,
		"journal hash chain":       migrationJournalHashChain,
		"notification preferences": migrationNotificationPrefs,
		"notification taxonomy":    migrationNotifyTaxonomy,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := run(context.Background(), tx, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("finished transaction was accepted")
			}
		})
	}
}
