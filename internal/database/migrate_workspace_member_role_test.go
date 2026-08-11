package database

import (
	"strings"
	"testing"
)

// workspace_members.role decides what a member can do across a whole
// workspace, and it had no constraint at all — `TEXT NOT NULL DEFAULT
// 'MEMBER'`, accepting any string. crew_members.role has carried a CHECK since
// v99.
//
// The reason it matters is an asymmetry in how the value is read: canRole's
// write tiers switch over the known roles and fall through to false on an
// unrecognised one, but its "read" tier accepts ANY non-empty string. So a
// garbage role fails closed for every mutation and open for every read — and
// the schema is the only place that can refuse it independently of whichever
// write path appears next.
//
// Migration 20260810160400 enforces it with BEFORE INSERT / BEFORE UPDATE
// triggers rather than a CHECK, because SQLite cannot add a CHECK without a
// full table rebuild. These tests pin that the enforcement is real on both
// paths, that it names every legitimate role, and that it does not reach
// backwards into rows already stored.

// workspaceMemberRoleFixture creates the workspace and user a membership row
// needs, and returns their ids.
func workspaceMemberRoleFixture(t *testing.T, db *DB) (wsID, userID string) {
	t.Helper()
	wsID, userID = "ws_role", "user_role"
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Roles', 'roles')`, wsID)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES (?, 'roles@example.com')`, userID)
	return
}

func TestWorkspaceMemberRoleIsConstrained(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, userID := workspaceMemberRoleFixture(t, db)

	t.Run("every real role is accepted", func(t *testing.T) {
		// Sourced from roleRank in internal/api/helpers.go. If a role is added
		// there and not here, this test passes while the trigger silently locks
		// the new role out of every workspace — so the list is the contract.
		// UNIQUE(workspace_id, user_id) means one membership per user, so each
		// role needs its own user.
		for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
			u := userID + "_" + role
			execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES (?, ?)`, u, role+"@example.com")
			if _, err := db.Exec(
				`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
				"wm_ok_"+role, wsID, u, role); err != nil {
				t.Errorf("role %q was refused: %v", role, err)
			}
		}
	})

	t.Run("a role outside the set is refused", func(t *testing.T) {
		bad := []struct {
			name string
			role string
		}{
			{"lowercase", "owner"},
			{"unknown word", "SUPERUSER"},
			{"empty string", ""},
			{"whitespace", " OWNER"},
			{"sql-ish", "OWNER'--"},
		}
		for _, tc := range bad {
			t.Run(tc.name, func(t *testing.T) {
				_, err := db.Exec(
					`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`,
					"wm_bad_"+tc.name, wsID, userID, tc.role)
				if err == nil {
					t.Fatalf("role %q was accepted; canRole(%q, \"read\") returns true for any non-empty string, so this row would carry read access", tc.role, tc.role)
				}
				if !strings.Contains(err.Error(), "workspace_members.role must be one of") {
					t.Errorf("refused, but not by the role guard: %v", err)
				}
			})
		}
	})

	t.Run("an update cannot smuggle one in", func(t *testing.T) {
		// The INSERT guard alone would be half a constraint: a row could be
		// created as MEMBER and then updated to anything.
		execMigrationFixture(t, db,
			`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm_upd', ?, ?, 'MEMBER')`,
			wsID, userID)
		if _, err := db.Exec(`UPDATE workspace_members SET role = 'SUPERUSER' WHERE id = 'wm_upd'`); err == nil {
			t.Fatal("UPDATE to an unknown role succeeded — the BEFORE UPDATE trigger is missing")
		}
		// A legitimate change must still work.
		if _, err := db.Exec(`UPDATE workspace_members SET role = 'ADMIN' WHERE id = 'wm_upd'`); err != nil {
			t.Fatalf("legitimate promotion to ADMIN was refused: %v", err)
		}
		var got string
		if err := db.QueryRow(`SELECT role FROM workspace_members WHERE id = 'wm_upd'`).Scan(&got); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if got != "ADMIN" {
			t.Errorf("role = %q, want ADMIN", got)
		}
	})
}

// TestWorkspaceMemberRoleGuardDoesNotBreakBoot pins the deliberate difference
// between a trigger and a rebuilt CHECK: a row already stored with an odd role
// keeps working. A CHECK added by table rebuild would refuse to apply at all
// on such an instance, turning a defensive tightening into a boot failure on
// exactly the database that most needs looking at. The guard bites when
// something tries to WRITE that value, which is the point.
func TestWorkspaceMemberRoleGuardDoesNotBreakBoot(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	wsID, userID := workspaceMemberRoleFixture(t, db)

	// Reconstruct a legacy row: drop the guards, write the odd value, restore.
	// This is the state a pre-migration instance could genuinely be in.
	for _, s := range []string{
		`DROP TRIGGER IF EXISTS trg_workspace_members_role_check`,
		`DROP TRIGGER IF EXISTS trg_workspace_members_role_check_upd`,
	} {
		execMigrationFixture(t, db, s)
	}
	execMigrationFixture(t, db,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm_legacy', ?, ?, 'LEGACY_ROLE')`,
		wsID, userID)

	// Re-applying the migration must succeed with that row present.
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260810160400`); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := Migrate(t.Context(), db.DB, newTestLogger()); err != nil {
		t.Fatalf("re-Migrate with a legacy role row present: %v — a trigger must not reach backwards into stored rows", err)
	}

	var got string
	if err := db.QueryRow(`SELECT role FROM workspace_members WHERE id = 'wm_legacy'`).Scan(&got); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if got != "LEGACY_ROLE" {
		t.Errorf("legacy role = %q, want it left alone", got)
	}

	// But writing it back is refused.
	if _, err := db.Exec(`UPDATE workspace_members SET role = role WHERE id = 'wm_legacy'`); err == nil {
		t.Error("rewriting the legacy role succeeded; the guard should refuse it on the way in")
	}
}
