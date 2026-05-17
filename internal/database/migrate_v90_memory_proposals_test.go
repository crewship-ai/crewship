package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV90_MemoryProposalsSchema asserts the v90 migration created
// the memory_proposals table with the expected columns + constraints, the
// inbox_items.kind CHECK now admits 'memory_consolidation', and
// workspaces gained the memory_config column. (Originally v89 on
// feat/memory-reliability-bundle; renumbered to v90 on rebase.)
//
// Refactored into subtests + table-driven status matrix per the project's
// test-style guidelines: one t.Fatalf in a flat function masks downstream
// regressions, whereas distinct subtests let CI surface every breakage at
// once.
func TestMigrateV90_MemoryProposalsSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v90.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Suite-standard test logger: warnings + errors only on stderr.
	// io.Discard would hide useful migration diagnostics (cascade-
	// trigger conflicts, partial-applies) on a failing test run.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed the prerequisite workspace + crew once; the inbox + status
	// subtests reuse them.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew1', 'ws1', 'Crew', 'crew1')`); err != nil {
		t.Fatalf("insert crew: %v", err)
	}

	t.Run("schema", func(t *testing.T) {
		assertMemoryProposalsSchema(t, db.DB)
	})

	t.Run("inbox_kind_check", func(t *testing.T) {
		assertInboxKindCheck(t, db.DB)
	})

	t.Run("workspace_memory_config_column", func(t *testing.T) {
		var memCfg *string
		if err := db.QueryRow(`SELECT memory_config FROM workspaces WHERE id = 'ws1'`).Scan(&memCfg); err != nil {
			t.Fatalf("read workspaces.memory_config: %v", err)
		}
		if memCfg != nil {
			t.Errorf("expected memory_config NULL by default, got %q", *memCfg)
		}
	})

	t.Run("proposal_status_matrix", func(t *testing.T) {
		assertProposalStatusMatrix(t, db.DB)
	})
}

// assertMemoryProposalsSchema runs the column-type table check for the
// memory_proposals table. Kept in a helper so the t.Run wrapper above
// stays readable.
func assertMemoryProposalsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	wantCols := map[string]string{
		"id":                 "TEXT",
		"workspace_id":       "TEXT",
		"crew_id":            "TEXT",
		"proposal_path":      "TEXT",
		"status":             "TEXT",
		"inbox_item_id":      "TEXT",
		"evidence_json":      "TEXT",
		"rules_count":        "INTEGER",
		"entries_scanned":    "INTEGER",
		"created_at":         "TEXT",
		"decided_at":         "TEXT",
		"decided_by_user_id": "TEXT",
	}
	rows, err := db.Query(`PRAGMA table_info(memory_proposals)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		got[name] = strings.ToUpper(ctype)
	}
	// Surface iterator-level failures (closed DB, malformed PRAGMA)
	// before checking column types — otherwise a partial-iteration
	// failure would produce a false-green or wrong-reason FAIL.
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info rows: %v", err)
	}
	for col, ctype := range wantCols {
		if got[col] != ctype {
			t.Errorf("memory_proposals.%s type = %q, want %q (full schema: %+v)", col, got[col], ctype, got)
		}
	}
}

// assertInboxKindCheck asserts the widened CHECK admits the new
// memory_consolidation kind and still rejects unknown kinds.
func assertInboxKindCheck(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Run("accepts_memory_consolidation", func(t *testing.T) {
		if _, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_mc_1', 'ws1', 'memory_consolidation', 'prop_1', 'Memory consolidation proposal')`); err != nil {
			t.Fatalf("insert memory_consolidation inbox item: %v", err)
		}
	})
	t.Run("rejects_unknown_kind", func(t *testing.T) {
		_, err := db.Exec(`
INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
VALUES ('ibx_bad_1', 'ws1', 'bogus_kind', 'x', 'x')`)
		if err == nil {
			t.Fatalf("expected CHECK violation on unknown kind, got nil")
		}
		// Tighten: a NOT NULL or FK violation would also produce a
		// non-nil err and silently pass this test for the wrong
		// reason. Assert the underlying constraint type so only a
		// genuine CHECK failure satisfies the contract.
		if !isCheckConstraintErr(err) {
			t.Fatalf("expected CHECK violation, got %T: %v", err, err)
		}
	})
}

// isCheckConstraintErr returns true when err is a CHECK-constraint
// violation from the SQLite driver. The driver wraps the constraint
// failure in a generic Error type whose Error() includes "CHECK
// constraint failed"; case-insensitive substring is the portable check
// across driver versions. A Postgres-targeted port would assert
// *pq.Error.Code == "23514" instead.
func isCheckConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "check constraint failed")
}

// assertProposalStatusMatrix is the table-driven sibling of the old
// flat insert sequence. Each row describes one (status, decided_at,
// decided_by_user_id) combination and whether the CHECK should accept
// it. Table-driven so a future status-vocabulary expansion stays
// O(1) — add a row, don't append another insert block.
func assertProposalStatusMatrix(t *testing.T, db *sql.DB) {
	t.Helper()
	// Fixed timestamp passed as a parameter, not inlined as
	// datetime('now') SQL — keeps the test deterministic AND removes
	// the SQL-injection-via-string-concat shape the earlier table
	// had. We're inserting test fixtures so the precise time doesn't
	// matter; pick any past-but-non-zero RFC3339 timestamp.
	const decidedTS = "2026-05-17T12:00:00Z"
	cases := []struct {
		name           string
		id             string
		status         string
		bindDecidedAt  bool
		bindDecidedBy  bool
		decidedByValue string
		wantAccept     bool
		violationHint  string
	}{
		{
			name:       "pending_with_neither_decided_field",
			id:         "p_pending_ok",
			status:     "pending",
			wantAccept: true,
		},
		{
			name:          "approved_without_either_decided_field",
			id:            "p_approved_bare",
			status:        "approved",
			wantAccept:    false,
			violationHint: "approved with no decided_at AND no decided_by_user_id must violate CHECK",
		},
		{
			name:          "approved_with_decided_at_only",
			id:            "p_approved_partial",
			status:        "approved",
			bindDecidedAt: true,
			wantAccept:    false,
			violationHint: "approved with decided_at but NO decided_by_user_id must violate CHECK (audit trail integrity)",
		},
		{
			name:           "approved_with_both_decided_fields",
			id:             "p_approved_full",
			status:         "approved",
			bindDecidedAt:  true,
			bindDecidedBy:  true,
			decidedByValue: "usr_op_1",
			wantAccept:     true,
		},
		{
			name:          "rejected_with_decided_at_only",
			id:            "p_rejected_partial",
			status:        "rejected",
			bindDecidedAt: true,
			wantAccept:    false,
			violationHint: "rejected with decided_at but NO decided_by_user_id must violate CHECK",
		},
		{
			name:           "rejected_with_both_decided_fields",
			id:             "p_rejected_full",
			status:         "rejected",
			bindDecidedAt:  true,
			bindDecidedBy:  true,
			decidedByValue: "usr_op_2",
			wantAccept:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Build the INSERT dynamically based on which decided_*
			// fields are set. Both values are now parameter-bound —
			// no SQL strings come from the case struct, so even a
			// future test-case adder can't accidentally introduce
			// injection-shaped patterns.
			cols := []string{"id", "workspace_id", "crew_id", "proposal_path", "status"}
			vals := []string{"?", "?", "?", "?", "?"}
			args := []any{c.id, "ws1", "crew1", "/tmp/" + c.id + ".md", c.status}
			if c.bindDecidedAt {
				cols = append(cols, "decided_at")
				vals = append(vals, "?")
				args = append(args, decidedTS)
			}
			if c.bindDecidedBy {
				cols = append(cols, "decided_by_user_id")
				vals = append(vals, "?")
				args = append(args, c.decidedByValue)
			}
			q := "INSERT INTO memory_proposals (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")"
			_, err := db.Exec(q, args...)
			if c.wantAccept && err != nil {
				t.Errorf("insert should have succeeded but failed: %v\n  query: %s", err, q)
			}
			if !c.wantAccept {
				switch {
				case err == nil:
					t.Errorf("insert should have violated CHECK but succeeded.\n  hint: %s\n  query: %s", c.violationHint, q)
				case !isCheckConstraintErr(err):
					t.Errorf("insert failed for non-CHECK reason: %v\n  hint: %s\n  query: %s", err, c.violationHint, q)
				}
			}
		})
	}
}
