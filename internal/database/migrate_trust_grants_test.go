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

// TestMigrateTrustGrants asserts the standing-approval grant table lands
// with the invariants the feature's safety rests on:
//
//   - a grant is bound to a definition_hash, so editing the routine
//     orphans it rather than silently carrying trust onto new content;
//   - the partial UNIQUE index admits re-granting after a revoke while
//     refusing two live grants for the same (workspace, routine, step,
//     definition) — otherwise use counting splits across duplicates;
//   - FK cascade from pipelines, so deleting a routine cannot leave a
//     grant that a recycled pipeline id would inherit.
//
// Numbered by timestamp, not sequentially: the v1..v169 block closed
// while this branch was open (see the migrations slice tail).
func TestMigrateTrustGrants(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "trustgrants.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, migLogger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	ctx := context.Background()
	if !tableExists(t, db.DB, ctx, "waitpoint_trust_grants") {
		t.Fatal("waitpoint_trust_grants table missing after Migrate")
	}

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	mustExec(t, db.DB, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
	                    VALUES ('pl1', 'ws1', 'triage', 'Triage', '{}', 'hashA')`)

	insert := func(id, hash string) error {
		_, err := db.DB.Exec(`
INSERT INTO waitpoint_trust_grants (id, workspace_id, pipeline_id, step_id, definition_hash, granted_by_user_id, reason)
VALUES (?, 'ws1', 'pl1', 'gate', ?, 'usr1', 'approved this ten times')`, id, hash)
		return err
	}

	cases := []struct {
		name   string
		assert func(t *testing.T, db *sql.DB)
	}{
		{
			name: "schema/uses_starts_at_zero",
			assert: func(t *testing.T, db *sql.DB) {
				if got := strings.Trim(columnDefault(t, db, "waitpoint_trust_grants", "uses"), "'\""); got != "0" {
					t.Errorf("uses default = %q, want 0", got)
				}
			},
		},
		{
			name: "grant/first_live_grant_inserts",
			assert: func(t *testing.T, db *sql.DB) {
				if err := insert("wtg1", "hashA"); err != nil {
					t.Fatalf("first grant insert: %v", err)
				}
			},
		},
		{
			name: "grant/second_live_grant_for_same_definition_is_refused",
			assert: func(t *testing.T, db *sql.DB) {
				if err := insert("wtg2", "hashA"); err == nil {
					t.Error("duplicate live grant accepted — use counting would split across rows")
				}
			},
		},
		{
			name: "grant/different_definition_hash_is_a_separate_grant",
			assert: func(t *testing.T, db *sql.DB) {
				if err := insert("wtg3", "hashB"); err != nil {
					t.Errorf("grant for a different definition refused: %v", err)
				}
			},
		},
		{
			name: "grant/revoked_row_frees_the_slot",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `UPDATE waitpoint_trust_grants SET revoked_at = datetime('now') WHERE id = 'wtg1'`)
				if err := insert("wtg4", "hashA"); err != nil {
					t.Errorf("re-grant after revoke refused: %v", err)
				}
			},
		},
		{
			name: "grant/deleting_the_routine_cascades",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `DELETE FROM pipelines WHERE id = 'pl1'`)
				var n int
				if err := db.QueryRow(`SELECT COUNT(*) FROM waitpoint_trust_grants WHERE pipeline_id = 'pl1'`).Scan(&n); err != nil {
					t.Fatalf("count after cascade: %v", err)
				}
				if n != 0 {
					t.Errorf("grants surviving routine deletion: %d, want 0 — a recycled pipeline id would inherit trust", n)
				}
			},
		},
	}

	// Sequential: each case builds on the rows the previous one left.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.assert(t, db.DB) })
	}
}
