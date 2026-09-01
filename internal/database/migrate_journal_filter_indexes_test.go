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

// seedJournalPlannerDB builds a migrated database holding enough
// journal_entries — with realistic column skew — for SQLite's planner to
// have something to reason about, then ANALYZEs it.
//
// Both halves matter. On an empty table every candidate index costs the
// same and the planner picks whatever is first, so an EXPLAIN QUERY PLAN
// assertion made against a fresh schema proves nothing about production.
// And without sqlite_stat1 rows the OR-optimization is never chosen at
// all — see the ANALYZE call at the end of Migrate (migrate.go), which is
// what supplies those statistics on a live instance.
func seedJournalPlannerDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "journalidx.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','Work','work')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	// 2,000 rows shaped like a real workspace: severity overwhelmingly
	// 'info', actor_id set on half, trace_id on a third, payload.run_id on
	// a seventh, priority almost always 'normal'.
	if _, err := db.Exec(`
WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n < 2000)
INSERT INTO journal_entries
  (id, workspace_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, trace_id)
SELECT
  'j' || n,
  'ws1',
  strftime('%Y-%m-%dT%H:%M:%fZ', '2026-01-01', '+' || n || ' seconds'),
  CASE n % 5 WHEN 0 THEN 'run.started' ELSE 'container.metrics' END,
  CASE WHEN n % 200 = 0 THEN 'error' WHEN n % 199 = 0 THEN 'warn' ELSE 'info' END,
  CASE WHEN n % 500 = 0 THEN 'high' ELSE 'normal' END,
  'agent',
  CASE WHEN n % 2 = 0 THEN 'actor_' || (n % 40) END,
  'summary ' || n,
  CASE WHEN n % 7 = 0 THEN json_object('run_id', 'run_' || (n % 50)) ELSE '{}' END,
  CASE WHEN n % 3 = 0 THEN 'trace_' || (n % 60) END
FROM c`); err != nil {
		t.Fatalf("seed entries: %v", err)
	}
	if _, err := db.Exec(`ANALYZE`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}
	return db.DB
}

// queryPlan returns every EXPLAIN QUERY PLAN detail line joined by newlines.
// A MULTI-INDEX OR spans several rows, so only the whole plan answers "is
// this an index union or a scan".
func queryPlan(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatalf("explain %q: %v", query, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var detail string
		if err := rows.Scan(new(int), new(int), new(int), &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return strings.Join(lines, "\n")
}

// listShape mirrors journal.List's SELECT/ORDER BY/LIMIT so the plan under
// test is the plan the Timeline actually provokes, not a simplified stand-in.
func listShape(where string) string {
	return `SELECT journal_entries.id FROM journal_entries WHERE ` + where +
		` ORDER BY journal_entries.ts DESC, journal_entries.id DESC LIMIT 500`
}

// TestJournalFilterIndexes_SingleValueFiltersAreIndexed pins the fix for
// #2210 item 4. The UI sends exactly one severity and one priority, and a
// single-value IN does not imply a partial index's `IN ('warn','error')` /
// `!= 'normal'` predicate, so before this migration both filters fell back
// to idx_journal_ws_ts and scanned the whole workspace partition.
func TestJournalFilterIndexes_SingleValueFiltersAreIndexed(t *testing.T) {
	db := seedJournalPlannerDB(t)

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "severity IN with one value",
			query: listShape(`workspace_id = 'ws1' AND severity IN ('error')`),
			want:  "idx_journal_ws_severity_ts",
		},
		{
			name:  "severity equality",
			query: listShape(`workspace_id = 'ws1' AND severity = 'error'`),
			want:  "idx_journal_ws_severity_ts",
		},
		{
			name:  "priority IN with one value",
			query: listShape(`workspace_id = 'ws1' AND priority IN ('high')`),
			want:  "idx_journal_ws_priority_ts",
		},
		{
			name:  "actor_id alone",
			query: listShape(`workspace_id = 'ws1' AND actor_id = 'actor_4'`),
			want:  "idx_journal_ws_actor_ts",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, db, tc.query)
			if !strings.Contains(plan, tc.want) {
				t.Errorf("plan did not use %s:\n%s", tc.want, plan)
			}
		})
	}
}

// TestJournalFilterIndexes_MultiSeverityStillUsesPartialIndex guards the
// index we deliberately kept. The wide (workspace_id, severity, ts) index
// added here serves the single-value case, but for the two-value
// errors-and-warnings view the planner still prefers the tiny partial
// index from v146 — dropping it would trade one regression for another.
func TestJournalFilterIndexes_MultiSeverityStillUsesPartialIndex(t *testing.T) {
	db := seedJournalPlannerDB(t)
	plan := queryPlan(t, db, listShape(`workspace_id = 'ws1' AND severity IN ('warn','error')`))
	if !strings.Contains(plan, "idx_journal_ws_sev_ts") && !strings.Contains(plan, "idx_journal_ws_severity_ts") {
		t.Errorf("errors+warnings view fell back to a workspace scan:\n%s", plan)
	}
}

// TestJournalFilterIndexes_RunIDUnionsThreeProbes is the claim
// migrate_consts_v120_journal_run_index.go already makes in prose and the
// schema did not deliver: journal.Query.RunID emits
// `(trace_id = ? OR actor_id = ? OR run_id = ?)`, and SQLite refuses to
// index-union an OR whose every leg is not indexed. actor_id had no index
// at all (idx_journal_actor_ts is on actor_TYPE), so the whole predicate
// degraded to a full workspace scan — on Count(), which emits no LIMIT,
// a complete one.
func TestJournalFilterIndexes_RunIDUnionsThreeProbes(t *testing.T) {
	db := seedJournalPlannerDB(t)
	const where = `workspace_id = 'ws1' AND (trace_id = 'run_7' OR actor_id = 'run_7' OR run_id = 'run_7')`

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"List", listShape(where)},
		{"Count", `SELECT COUNT(*) FROM journal_entries WHERE ` + where},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, db, tc.query)
			if !strings.Contains(plan, "MULTI-INDEX OR") {
				t.Errorf("run_id filter is not an index union:\n%s", plan)
			}
			for _, idx := range []string{"idx_journal_ws_trace", "idx_journal_ws_actor_ts", "idx_journal_ws_run"} {
				if !strings.Contains(plan, idx) {
					t.Errorf("union leg %s missing from plan:\n%s", idx, plan)
				}
			}
		})
	}
}

// TestJournalFilterIndexes_RedundantPriorityIndexDropped checks the
// migration cleaned up after itself: (workspace_id, priority) WHERE
// priority != 'normal' is a strict prefix-subset of the new
// (workspace_id, priority, ts DESC), so keeping it would only cost writes.
func TestJournalFilterIndexes_RedundantPriorityIndexDropped(t *testing.T) {
	db := seedJournalPlannerDB(t)
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_journal_entries_priority'`,
	).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 0 {
		t.Errorf("idx_journal_entries_priority still present; it is superseded by idx_journal_ws_priority_ts")
	}
}
