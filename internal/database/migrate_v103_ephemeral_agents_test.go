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

// TestMigrateV103_EphemeralAgents asserts the PR-D ephemeral lifecycle
// surface lands additively: every new column on agents + the per-crew
// quota on crews exist with the documented defaults, CHECK constraints
// reject invalid values, and pre-v100 insert shapes still succeed (the
// whole point of additive migrations: legacy callers must not have to
// know about the new columns).
//
// The "ghost state preserves the audit row" invariant is enforced by
// the API layer (no DELETE in the EphemeralExpiry sweeper), but this
// migration test still proves the column shape can carry the state —
// specifically that expired_at can be set independently of deleted_at
// and that the partial index excludes already-expired rows from the
// sweeper's hot scan.
func TestMigrateV103_EphemeralAgents(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v103.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, migLogger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)
	mustExec(t, db.DB, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'crew', 'crew')`)

	cases := []struct {
		name   string
		assert func(t *testing.T, db *sql.DB)
	}{
		{
			name: "schema/ephemeral_defaults_to_zero",
			assert: func(t *testing.T, db *sql.DB) {
				if got := columnDefault(t, db, "agents", "ephemeral"); strings.TrimSpace(got) != "0" {
					t.Errorf("agents.ephemeral default = %q, want '0'", got)
				}
			},
		},
		{
			name: "schema/max_ephemeral_agents_defaults_to_ten",
			assert: func(t *testing.T, db *sql.DB) {
				if got := columnDefault(t, db, "crews", "max_ephemeral_agents"); strings.TrimSpace(got) != "10" {
					t.Errorf("crews.max_ephemeral_agents default = %q, want '10'", got)
				}
			},
		},
		{
			name: "schema/lifecycle_columns_exist",
			assert: func(t *testing.T, db *sql.DB) {
				for _, col := range []string{"expires_at", "expired_at", "parent_lead_id", "hire_reason"} {
					if got := columnType(t, db, "agents", col); strings.ToUpper(got) != "TEXT" {
						t.Errorf("agents.%s type = %q, want TEXT", col, got)
					}
				}
			},
		},
		{
			name: "legacy_agent_insert_without_ephemeral_columns_works",
			assert: func(t *testing.T, db *sql.DB) {
				// The whole point of additive defaults: existing agent
				// inserts that don't mention any of the v103 columns
				// must continue to land — ephemeral=0 + nulls everywhere
				// else kicks in automatically.
				mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug)
					VALUES ('a_legacy', 'cr1', 'ws1', 'legacy', 'legacy')`)
				var (
					ephemeral              int
					expires, expired       sql.NullString
					parentLead, hireReason sql.NullString
				)
				if err := db.QueryRow(`
					SELECT ephemeral, expires_at, expired_at, parent_lead_id, hire_reason
					FROM agents WHERE id = 'a_legacy'`,
				).Scan(&ephemeral, &expires, &expired, &parentLead, &hireReason); err != nil {
					t.Fatalf("read back: %v", err)
				}
				if ephemeral != 0 {
					t.Errorf("ephemeral = %d, want 0 (default)", ephemeral)
				}
				if expires.Valid || expired.Valid || parentLead.Valid || hireReason.Valid {
					t.Errorf("expected all lifecycle columns NULL on legacy insert, got expires=%v expired=%v parent=%v reason=%v",
						expires, expired, parentLead, hireReason)
				}
			},
		},
		{
			name: "bogus_ephemeral_value_rejected",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, ephemeral)
					VALUES ('a_bogus', 'cr1', 'ws1', 'bogus', 'bogus-eph', 2)`)
				if err == nil {
					t.Error("expected CHECK constraint to reject ephemeral=2")
				}
			},
		},
		{
			name: "negative_quota_rejected",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, max_ephemeral_agents)
					VALUES ('cr_neg', 'ws1', 'neg', 'crew-neg', -1)`)
				if err == nil {
					t.Error("expected CHECK constraint to reject max_ephemeral_agents=-1")
				}
			},
		},
		{
			name: "ephemeral_row_writable_with_full_lifecycle",
			assert: func(t *testing.T, db *sql.DB) {
				// Insert a parent LEAD first so parent_lead_id has a
				// real referent (the column is a soft FK so SQLite
				// won't enforce, but the test mirrors production shape).
				mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
					VALUES ('a_lead', 'cr1', 'ws1', 'lead', 'lead-x', 'LEAD')`)
				mustExec(t, db, `
					INSERT INTO agents (id, crew_id, workspace_id, name, slug,
						ephemeral, expires_at, parent_lead_id, hire_reason)
					VALUES ('a_eph', 'cr1', 'ws1', 'eph', 'eph-x',
						1, '2026-05-22T00:00:00Z', 'a_lead', 'triage 12 backlog issues')`)
				var (
					ephemeral           int
					expires, parentLead sql.NullString
					expired, hireReason sql.NullString
				)
				if err := db.QueryRow(`
					SELECT ephemeral, expires_at, expired_at, parent_lead_id, hire_reason
					FROM agents WHERE id = 'a_eph'`,
				).Scan(&ephemeral, &expires, &expired, &parentLead, &hireReason); err != nil {
					t.Fatal(err)
				}
				if ephemeral != 1 {
					t.Errorf("ephemeral = %d, want 1", ephemeral)
				}
				if !expires.Valid || expires.String != "2026-05-22T00:00:00Z" {
					t.Errorf("expires_at = %v, want 2026-05-22T00:00:00Z", expires)
				}
				if expired.Valid {
					t.Errorf("expired_at = %v, want NULL on fresh hire", expired)
				}
				if !parentLead.Valid || parentLead.String != "a_lead" {
					t.Errorf("parent_lead_id = %v, want 'a_lead'", parentLead)
				}
				if !hireReason.Valid || !strings.Contains(hireReason.String, "triage 12") {
					t.Errorf("hire_reason = %v, want contains 'triage 12'", hireReason)
				}
			},
		},
		{
			name: "ghost_state_flip_preserves_row",
			assert: func(t *testing.T, db *sql.DB) {
				// "Ghost state" = expired_at NOT NULL, deleted_at IS
				// NULL. The sweeper just flips expired_at; the row
				// stays in the table for audit + rehire. Prove the
				// column shape supports this combination.
				mustExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, ephemeral, expires_at)
					VALUES ('a_ghost', 'cr1', 'ws1', 'ghost', 'ghost-x', 1, '2026-05-21T00:00:00Z')`)
				mustExec(t, db, `UPDATE agents SET expired_at = '2026-05-21T00:00:01Z' WHERE id = 'a_ghost'`)

				var deletedAt, expiredAt sql.NullString
				if err := db.QueryRow(`SELECT deleted_at, expired_at FROM agents WHERE id = 'a_ghost'`).
					Scan(&deletedAt, &expiredAt); err != nil {
					t.Fatal(err)
				}
				if deletedAt.Valid {
					t.Errorf("deleted_at = %v, want NULL (ghost state preserves the row)", deletedAt)
				}
				if !expiredAt.Valid || expiredAt.String == "" {
					t.Errorf("expired_at = %v, want set", expiredAt)
				}
			},
		},
		{
			name: "sweeper_partial_index_excludes_ghosts",
			assert: func(t *testing.T, db *sql.DB) {
				// The partial index on idx_agent_expires_at excludes
				// rows where expired_at IS NOT NULL — those are already
				// ghosts; re-scanning them every 5 minutes wastes work.
				// Prove the index exists and has the WHERE clause we
				// expect by querying sqlite_master.
				var ddl sql.NullString
				if err := db.QueryRow(`
					SELECT sql FROM sqlite_master
					WHERE type = 'index' AND name = 'idx_agent_expires_at'`,
				).Scan(&ddl); err != nil {
					t.Fatalf("introspect idx: %v", err)
				}
				if !ddl.Valid {
					t.Fatal("idx_agent_expires_at missing")
				}
				if !strings.Contains(strings.ToLower(ddl.String), "where") ||
					!strings.Contains(strings.ToLower(ddl.String), "expired_at is null") {
					t.Errorf("idx ddl missing WHERE expired_at IS NULL clause: %q", ddl.String)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.assert(t, db.DB)
		})
	}
}
