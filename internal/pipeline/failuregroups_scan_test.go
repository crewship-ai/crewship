package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// These tests guard the fix for finding H4 / T2.8 from the 2026-06 security
// audit (.claude/context/SECURITY-AUDIT-2026-06.md): RunStore.FailureGroups
// formerly ran an UNBOUNDED full scan — the SQL had no LIMIT, so it streamed
// every status='failed' row in the workspace to Go, accumulated ALL of their
// run IDs into in-memory slices, and only THEN truncated the slice of *groups*
// to `limit`. Both the row scan and the memory footprint were O(total failed
// runs), not O(limit).
//
// The fix pushes GROUP BY error_fingerprint ... ORDER BY MAX(started_at) DESC
// LIMIT into SQL and samples only the most-recent maxFailureRunIDSample run
// IDs per group (a separate bounded query). The tests below now assert that
// SECURE behavior: the run-id sample stays bounded no matter how many failures
// share a fingerprint, and they would FAIL if the unbounded scan regressed.

// newFailureDB builds an in-memory pipeline_runs table carrying the columns
// FailureGroups reads. Mirrors the harness in runs_metadata_test.go but adds
// the error_fingerprint / failed_at_step / error_message observability columns.
func newFailureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Pin to one connection: ":memory:" is per-connection, so a pooled
	// reader could otherwise miss the seeded schema/rows.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`
CREATE TABLE pipeline_runs (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL,
    pipeline_slug     TEXT,
    status            TEXT,
    started_at        TEXT DEFAULT '',
    error_fingerprint TEXT,
    failed_at_step    TEXT,
    error_message     TEXT);`); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedFailedRuns inserts n failed runs in workspace ws, all sharing one
// fingerprint, with monotonically increasing started_at so ordering is stable.
// A single shared fingerprint means there is exactly ONE failure group — so the
// returned group's Count / RunIDs length directly equals "rows the scan
// materialized", letting us prove the scan ignores the limit arg.
func seedFailedRuns(t *testing.T, db *sql.DB, ws string, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`
INSERT INTO pipeline_runs
  (id, workspace_id, pipeline_slug, status, started_at, error_fingerprint, failed_at_step, error_message)
VALUES (?, ?, 'probe', 'failed', ?, 'fp_shared', 'probe', 'boom')`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		// Zero-padded started_at keeps lexical order == insert order.
		startedAt := fmt.Sprintf("2026-06-29T%010d", i)
		if _, err := stmt.Exec(fmt.Sprintf("run_%07d", i), ws, startedAt); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestFailureGroups_Baseline_GroupsByFingerprint is a sanity guard: distinct
// fingerprints bucket correctly and the limit caps the number of returned
// GROUPS. This must keep passing across the fix.
func TestFailureGroups_Baseline_GroupsByFingerprint(t *testing.T) {
	db := newFailureDB(t)
	defer db.Close()
	// Three distinct fingerprints, one row each.
	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_slug, status, started_at, error_fingerprint, failed_at_step, error_message) VALUES
  ('a','w','p','failed','2026-06-29T03','fp_a','s','boom-a'),
  ('b','w','p','failed','2026-06-29T02','fp_b','s','boom-b'),
  ('c','w','p','failed','2026-06-29T01','fp_c','s','boom-c'),
  ('ok','w','p','completed','2026-06-29T04',NULL,'','');`); err != nil {
		t.Fatal(err)
	}
	s := NewRunStore(db)

	all, err := s.FailureGroups(context.Background(), "w", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 fingerprint groups, got %d", len(all))
	}
	// Newest-first: fp_a (03) leads.
	if all[0].Fingerprint != "fp_a" {
		t.Errorf("want newest group fp_a first, got %q", all[0].Fingerprint)
	}

	// limit caps GROUPS returned.
	capped, err := s.FailureGroups(context.Background(), "w", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped) != 2 {
		t.Fatalf("limit=2 should return 2 groups, got %d", len(capped))
	}
}

// TestFailureGroups_BoundedScan asserts the H4 fix: with limit=1 over a
// workspace holding N failed runs that share one fingerprint, the single
// returned group reports the true Count (a cheap SQL COUNT(*)) but materializes
// only the bounded most-recent run-id sample — never N ids. If the unbounded
// scan regressed, len(RunIDs) would track N and this would fail.
func TestFailureGroups_BoundedScan(t *testing.T) {
	const total = 2000
	db := newFailureDB(t)
	defer db.Close()
	seedFailedRuns(t, db, "w", total)
	s := NewRunStore(db)

	out, err := s.FailureGroups(context.Background(), "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("single shared fingerprint should yield 1 group, got %d", len(out))
	}
	g := out[0]

	// The aggregate count legitimately reflects the true total (SQL COUNT(*)).
	if g.Count != total {
		t.Errorf("Count should report the true failure total %d, got %d", total, g.Count)
	}
	// The run-id sample MUST be bounded — never the whole failure history.
	if len(g.RunIDs) > maxFailureRunIDSample {
		t.Fatalf("H4 regressed: run-id sample = %d, exceeds bound %d (scan/alloc is O(total) again)", len(g.RunIDs), maxFailureRunIDSample)
	}
	if len(g.RunIDs) != maxFailureRunIDSample {
		t.Errorf("expected the sample capped at %d, got %d", maxFailureRunIDSample, len(g.RunIDs))
	}
	// Newest-first: the most recent seeded run (highest index) must lead.
	if g.RunIDs[0] != "run_0001999" {
		t.Errorf("sample must be most-recent-first; want run_0001999, got %q", g.RunIDs[0])
	}
}

// TestFailureGroups_SampleBoundedNotTotal drives the same tight limit against
// two workspace sizes (1k vs 10k) and asserts the materialized run-id sample is
// bounded by maxFailureRunIDSample in BOTH cases — i.e. the work no longer
// tracks the total failed-row count.
func TestFailureGroups_SampleBoundedNotTotal(t *testing.T) {
	cases := []struct {
		ws    string
		total int
	}{
		{"small", 1000},
		{"big", 10000},
	}
	s := func(db *sql.DB) *RunStore { return NewRunStore(db) }

	for _, c := range cases {
		c := c
		t.Run(c.ws, func(t *testing.T) {
			db := newFailureDB(t)
			defer db.Close()
			seedFailedRuns(t, db, c.ws, c.total)

			out, err := s(db).FailureGroups(context.Background(), c.ws, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 {
				t.Fatalf("ws=%s: want 1 group, got %d", c.ws, len(out))
			}
			if out[0].Count != c.total {
				t.Errorf("ws=%s: Count should be true total %d, got %d", c.ws, c.total, out[0].Count)
			}
			if got := len(out[0].RunIDs); got != maxFailureRunIDSample {
				t.Fatalf("ws=%s: run-id sample must be capped at %d regardless of total %d, got %d",
					c.ws, maxFailureRunIDSample, c.total, got)
			}
		})
	}
}

// TestFailureGroups_QueryPushesGroupByLimit is a non-flaky static guard: it
// reads the FailureGroups SQL straight from source and asserts the grouping
// query pushes GROUP BY + LIMIT into SQL (the root-cause fix for H4). It would
// fail if someone reverted to an unbounded full scan.
func TestFailureGroups_QueryPushesGroupByLimit(t *testing.T) {
	src, err := os.ReadFile("runs_observability.go")
	if err != nil {
		t.Fatalf("could not read runs_observability.go to inspect query: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func (s *RunStore) FailureGroups(")
	if idx < 0 {
		t.Fatal("FailureGroups signature moved; re-point this static guard")
	}
	// Grab the function body up to the next top-level func.
	rest := body[idx:]
	if next := strings.Index(rest[1:], "\nfunc "); next >= 0 {
		rest = rest[:next+1]
	}
	// Isolate the raw SQL string (backtick-quoted).
	open := strings.Index(rest, "`")
	closeIdx := -1
	if open >= 0 {
		closeIdx = strings.Index(rest[open+1:], "`")
	}
	if open < 0 || closeIdx < 0 {
		t.Fatal("could not isolate FailureGroups SQL literal; re-point this guard")
	}
	query := strings.ToUpper(rest[open+1 : open+1+closeIdx])
	if !strings.Contains(query, "GROUP BY") {
		t.Error("H4 regression: FailureGroups query no longer pushes GROUP BY into SQL")
	}
	if !strings.Contains(query, "LIMIT") {
		t.Error("H4 regression: FailureGroups grouping query no longer carries a LIMIT — unbounded scan risk")
	}
}

// TestFailureGroups_BoundedScan_Large is the high-volume regression guard:
// 10k failures under one fingerprint, limit=1. Count reflects the true total
// (cheap SQL COUNT(*)) but the materialized run-id sample stays bounded — the
// caller never loads 10k ids into memory.
func TestFailureGroups_BoundedScan_Large(t *testing.T) {
	const total = 10000
	db := newFailureDB(t)
	defer db.Close()
	seedFailedRuns(t, db, "w", total)
	s := NewRunStore(db)

	out, err := s.FailureGroups(context.Background(), "w", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 group, got %d", len(out))
	}
	if out[0].Count != total {
		t.Errorf("Count should report true total %d, got %d", total, out[0].Count)
	}
	if len(out[0].RunIDs) >= total {
		t.Fatalf("run-id sample must be bounded after the fix, got %d (unbounded)", len(out[0].RunIDs))
	}
	if len(out[0].RunIDs) != maxFailureRunIDSample {
		t.Errorf("run-id sample should be capped at %d, got %d", maxFailureRunIDSample, len(out[0].RunIDs))
	}
}
