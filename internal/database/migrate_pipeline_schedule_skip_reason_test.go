package database

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestMigrationPipelineScheduleSkipReason(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := MigrateBefore(t.Context(), db, logger, "pipeline_schedule_skip_reason"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('skip-w','Skip','skip')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash) VALUES('skip-p','skip-w','Skip','skip','{}','hash')`,
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,last_status,last_run_at,last_missed_count) VALUES('legacy-skip','skip-w','Legacy','skip-p','* * * * *','SKIPPED','2026-11-01T10:00:00Z',3)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	var reason, status, at string
	var missed int
	if err := db.QueryRow(`SELECT last_skip_reason,last_status,last_run_at,last_missed_count FROM pipeline_schedules WHERE id='legacy-skip'`).Scan(&reason, &status, &at, &missed); err != nil {
		t.Fatal(err)
	}
	if reason != "" || status != "SKIPPED" || at != "2026-11-01T10:00:00Z" || missed != 3 {
		t.Fatalf("migration guessed historical cause or changed outcome: %q %q %q %d", reason, status, at, missed)
	}
	// Old insert statements/backup bundles omit the column and keep working.
	if _, err := db.Exec(`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr) VALUES('old-writer','skip-w','Old writer','skip-p','* * * * *')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT last_skip_reason FROM pipeline_schedules WHERE id='old-writer'`).Scan(&reason); err != nil || reason != "" {
		t.Fatalf("old insert default=%q: %v", reason, err)
	}
	for _, value := range []string{"capacity", "catchup", "inactive", "busy", ""} {
		if _, err := db.Exec(`UPDATE pipeline_schedules SET last_skip_reason=? WHERE id='legacy-skip'`, value); err != nil {
			t.Errorf("valid reason %q: %v", value, err)
		}
	}
	if _, err := db.Exec(`UPDATE pipeline_schedules SET last_skip_reason='guessed' WHERE id='legacy-skip'`); err == nil {
		t.Fatal("invalid reason accepted")
	}
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatalf("migration is not restart-idempotent: %v", err)
	}
}
