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

// TestMigrateV101_Autonomy asserts the per-crew autonomy policy
// surface lands cleanly: every new column exists with the documented
// type / default, CHECK constraints reject bogus enum values, and
// pre-v101 insert shapes still succeed (additive migration must not
// break legacy callers — every existing crew insert in tests omits
// these columns and must continue to work).
func TestMigrateV101_Autonomy(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v101.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	migLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, migLogger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	mustExec(t, db.DB, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws1')`)

	cases := []struct {
		name   string
		assert func(t *testing.T, db *sql.DB)
	}{
		{
			name: "schema/autonomy_level_defaults_to_guided",
			assert: func(t *testing.T, db *sql.DB) {
				if got := columnDefault(t, db, "crews", "autonomy_level"); strings.Trim(got, "'\"") != "guided" {
					t.Errorf("crews.autonomy_level default = %q, want 'guided'", got)
				}
			},
		},
		{
			name: "schema/behavior_mode_defaults_to_warn",
			assert: func(t *testing.T, db *sql.DB) {
				if got := columnDefault(t, db, "crews", "behavior_mode"); strings.Trim(got, "'\"") != "warn" {
					t.Errorf("crews.behavior_mode default = %q, want 'warn'", got)
				}
			},
		},
		{
			name: "schema/audit_triple_columns_exist",
			assert: func(t *testing.T, db *sql.DB) {
				for _, col := range []string{"autonomy_set_by_user_id", "autonomy_set_at", "autonomy_reason"} {
					if got := columnType(t, db, "crews", col); strings.ToUpper(got) != "TEXT" {
						t.Errorf("crews.%s type = %q, want TEXT", col, got)
					}
				}
			},
		},
		{
			name: "legacy_crew_insert_without_autonomy_works",
			assert: func(t *testing.T, db *sql.DB) {
				// The whole point of additive defaults: existing crew
				// inserts (which never mention autonomy_level / behavior
				// _mode) must continue to land — guided/warn default
				// kicks in automatically.
				mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_legacy', 'ws1', 'crew', 'crew-legacy')`)
				var lvl, mode string
				if err := db.QueryRow(`SELECT autonomy_level, behavior_mode FROM crews WHERE id = 'cr_legacy'`).Scan(&lvl, &mode); err != nil {
					t.Fatalf("read back: %v", err)
				}
				if lvl != "guided" {
					t.Errorf("autonomy_level = %q, want 'guided' (default)", lvl)
				}
				if mode != "warn" {
					t.Errorf("behavior_mode = %q, want 'warn' (default)", mode)
				}
			},
		},
		{
			name: "explicit_autonomy_levels_accepted",
			assert: func(t *testing.T, db *sql.DB) {
				for i, lvl := range []string{"strict", "guided", "trusted", "full"} {
					id := "cr_lvl_" + lvl
					_, err := db.Exec(
						`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level) VALUES (?, 'ws1', ?, ?, ?)`,
						id, lvl+"-crew", "crew-lvl-"+lvl, lvl,
					)
					if err != nil {
						t.Errorf("case %d: insert with autonomy_level=%q failed: %v", i, lvl, err)
					}
				}
			},
		},
		{
			name: "bogus_autonomy_level_rejected",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(
					`INSERT INTO crews (id, workspace_id, name, slug, autonomy_level) VALUES ('cr_bogus_lvl', 'ws1', 'bogus', 'crew-bogus-lvl', 'YOLO')`,
				)
				if err == nil {
					t.Error("expected CHECK constraint to reject autonomy_level='YOLO'")
				}
			},
		},
		{
			name: "explicit_behavior_modes_accepted",
			assert: func(t *testing.T, db *sql.DB) {
				for _, mode := range []string{"warn", "block"} {
					id := "cr_mode_" + mode
					_, err := db.Exec(
						`INSERT INTO crews (id, workspace_id, name, slug, behavior_mode) VALUES (?, 'ws1', ?, ?, ?)`,
						id, mode+"-crew", "crew-mode-"+mode, mode,
					)
					if err != nil {
						t.Errorf("insert with behavior_mode=%q failed: %v", mode, err)
					}
				}
			},
		},
		{
			name: "bogus_behavior_mode_rejected",
			assert: func(t *testing.T, db *sql.DB) {
				_, err := db.Exec(
					`INSERT INTO crews (id, workspace_id, name, slug, behavior_mode) VALUES ('cr_bogus_mode', 'ws1', 'bogus', 'crew-bogus-mode', 'allow_all')`,
				)
				if err == nil {
					t.Error("expected CHECK constraint to reject behavior_mode='allow_all'")
				}
			},
		},
		{
			name: "audit_triple_writable",
			assert: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, `INSERT INTO users (id, email, full_name) VALUES ('u_audit', 'audit@example.com', 'Audit User')`)
				mustExec(t, db, `
					INSERT INTO crews (id, workspace_id, name, slug, autonomy_level, autonomy_set_by_user_id, autonomy_set_at, autonomy_reason)
					VALUES ('cr_audit', 'ws1', 'audited', 'crew-audit', 'trusted', 'u_audit', '2026-05-21T00:00:00Z', 'engineering crew uplift')`)
				var user, at, reason sql.NullString
				if err := db.QueryRow(
					`SELECT autonomy_set_by_user_id, autonomy_set_at, autonomy_reason FROM crews WHERE id = 'cr_audit'`,
				).Scan(&user, &at, &reason); err != nil {
					t.Fatal(err)
				}
				if !user.Valid || user.String != "u_audit" {
					t.Errorf("autonomy_set_by_user_id = %v, want 'u_audit'", user)
				}
				if !reason.Valid || reason.String != "engineering crew uplift" {
					t.Errorf("autonomy_reason = %v, want 'engineering crew uplift'", reason)
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
