package database

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestDurableRoutineStartSchemaUpgrade(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	var target migration
	for _, candidate := range migrations {
		if candidate.name == "durable_routine_start_schema" {
			target = candidate
		}
	}
	if target.version == 0 {
		t.Fatal("durable routine start schema migration missing")
	}
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, target.version-1, logger); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('durable-w1','Fixture','durable-w1'),('durable-w2','Fixture','durable-w2')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('durable-p1','durable-w1','fixture','Fixture','{}','hash'),('durable-p2','durable-w2','fixture','Fixture','{}','hash')`,
		`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,status,fire_at,inputs_json,fired_run_id,dispatch_attempts) VALUES
          ('old-pending','durable-w1','durable-p1','fixture','pending','2026-10-18T10:00:00Z','{"value":1}',NULL,0),
          ('old-unlinked','durable-w1','durable-p1','fixture','fired','2026-10-18T10:01:00Z','{"value":2}','',1),
          ('old-linked','durable-w1','durable-p1','fixture','fired','2026-10-18T10:02:00Z','{"value":3}','old-run',2),
          ('old-failed','durable-w1','durable-p1','fixture','failed','2026-10-18T10:03:00Z','{}',NULL,10),
          ('old-expired','durable-w1','durable-p1','fixture','expired','2026-10-18T10:04:00Z','{}',NULL,4)`,
		`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES
          ('old-run','durable-w1','durable-p1','fixture','running','2026-10-18T10:02:00Z'),
          ('old-complete','durable-w1','durable-p1','fixture','completed','2026-10-18T10:03:00Z'),
          ('old-waiting','durable-w1','durable-p1','fixture','waiting','2026-10-18T10:04:00Z')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Select the exact pre-upgrade columns again afterward: every historical
	// field must survive, not just a reconstructed subset of the fixture.
	snapshot := func(query string) string {
		t.Helper()
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			refs := make([]any, len(columns))
			for i := range values {
				refs[i] = &values[i]
			}
			if err := rows.Scan(refs...); err != nil {
				t.Fatal(err)
			}
			records = append(records, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	oldQueries := map[string]string{}
	before := map[string]string{}
	for _, table := range []string{"pending_runs", "pipeline_runs"} {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, `"`+name+`"`)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		oldQueries[table] = "SELECT " + strings.Join(columns, ",") + " FROM " + table + " ORDER BY id"
		before[table] = snapshot(oldQueries[table])
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), target.sql); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for table, query := range oldQueries {
		if after := snapshot(query); after != before[table] {
			t.Fatalf("%s historical fields changed", table)
		}
	}
	for _, tc := range []struct{ table, column string }{
		{"pending_runs", "run_id"}, {"pending_runs", "idempotency_key"},
		{"pending_runs", "due_at"}, {"pending_runs", "reason_code"},
		{"pipeline_runs", "start_phase"},
	} {
		var kind string
		var notNull int
		var defaultValue sql.NullString
		if err := db.QueryRow(`SELECT type,"notnull",dflt_value FROM pragma_table_info(?) WHERE name=?`, tc.table, tc.column).Scan(&kind, &notNull, &defaultValue); err != nil {
			t.Fatal(err)
		}
		if kind != "TEXT" || notNull != 0 || defaultValue.Valid {
			t.Fatalf("%s.%s is not nullable TEXT without a default", tc.table, tc.column)
		}
		var nonNull int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + tc.table + " WHERE " + tc.column + " IS NOT NULL").Scan(&nonNull); err != nil {
			t.Fatal(err)
		}
		if nonNull != 0 {
			t.Fatalf("invented historical %s.%s", tc.table, tc.column)
		}
	}
	insert := func(id, workspace, pipeline string, key any) error {
		_, err := db.Exec(`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,fire_at,idempotency_key) VALUES(?,?,?,'fixture','2026-10-18T12:00:00Z',?)`, id, workspace, pipeline, key)
		return err
	}
	if err := insert("new-first", "durable-w1", "durable-p1", "sched:first"); err != nil {
		t.Fatal(err)
	}
	if err := insert("new-duplicate", "durable-w1", "durable-p1", "sched:first"); err == nil {
		t.Fatal("duplicate workspace occurrence accepted")
	}
	for _, row := range []struct {
		id, workspace, pipeline string
		key                     any
	}{
		{"new-other-ws", "durable-w2", "durable-p2", "sched:first"},
		{"new-other-key", "durable-w1", "durable-p1", "sched:second"},
		{"new-null-a", "durable-w1", "durable-p1", nil},
		{"new-null-b", "durable-w1", "durable-p1", nil},
	} {
		if err := insert(row.id, row.workspace, row.pipeline, row.key); err != nil {
			t.Fatal(err)
		}
	}
	var indexed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs INDEXED BY idx_pending_runs_start_recovery WHERE status='fired' AND COALESCE(fired_run_id,'')=''`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("recovery index candidates=%d, want only the unlinked claim", indexed)
	}
	for _, tc := range []struct{ query, index string }{
		{`SELECT id FROM pending_runs WHERE workspace_id='durable-w1' AND run_id='planned'`, "idx_pending_runs_planned_run"},
		{`SELECT id FROM pending_runs WHERE status='fired' AND run_id IS NOT NULL AND COALESCE(fired_run_id,'')='' ORDER BY id LIMIT 100`, "idx_pending_runs_start_recovery"},
	} {
		rows, err := db.Query("EXPLAIN QUERY PLAN " + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var details string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			details += detail + "\n"
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(details, tc.index) {
			t.Fatalf("expected %s in plan: %s", tc.index, details)
		}
	}
}
