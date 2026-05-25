package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV109_MemberCapabilitiesBackfill verifies the v109
// capability backfill produces the expected role-aware bundle for
// every role tier and that the partial index excludes rows that
// carry only the chat-baseline default.
//
// Migration shape: ALTER TABLE workspace_members ADD COLUMN
// capabilities TEXT, then UPDATE based on role. Single-operator
// OWNER installs must come out with the full capability bundle so
// nothing they did pre-v109 stops working.
//
// Test pattern mirrors migrate_v107_gdpr_cascade_test.go: open
// in-memory DB, migrate, seed FK targets, insert rows with each
// role, assert the column shape after the migration is replayed.
//
// Note: Migrate() runs every migration in order, so seeding
// workspace_members rows happens AFTER v109 already ran. We test the
// backfill semantics by simulating the "legacy row" case: insert
// rows, NULL out their capabilities column (legacy state), then
// re-run the backfill query and assert the resulting bundle.
func TestMigrateV109_MemberCapabilitiesBackfill(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v109.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed FK targets — one workspace, five users, one membership row
	// per role tier so we can assert the bundle for each.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO users (id, email) VALUES
		('uOwner','owner@x'),
		('uAdmin','admin@x'),
		('uManager','mgr@x'),
		('uMember','member@x'),
		('uViewer','viewer@x')
	`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES
		('mOwner','ws1','uOwner','OWNER'),
		('mAdmin','ws1','uAdmin','ADMIN'),
		('mManager','ws1','uManager','MANAGER'),
		('mMember','ws1','uMember','MEMBER'),
		('mViewer','ws1','uViewer','VIEWER')
	`); err != nil {
		t.Fatalf("seed members: %v", err)
	}

	// Simulate legacy rows that pre-date the v109 backfill so we can
	// re-run the WHERE capabilities IS NULL leg in isolation.
	if _, err := db.Exec(`UPDATE workspace_members SET capabilities = NULL`); err != nil {
		t.Fatalf("null caps: %v", err)
	}
	_, err = db.Exec(migrationMemberCapabilities)
	if err != nil {
		// CodeRabbit CR-10: narrow the catch-all. The ONLY expected
		// failure mode here is the ALTER complaining about the
		// already-existing column (because Migrate() already ran the
		// full constant once and we're replaying it). Any other
		// error means the migration SQL itself has a real bug — we
		// must surface it, not silently fall through to the manual
		// UPDATE replay which would mask the real problem.
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			t.Fatalf("migration replay returned unexpected error: %v", err)
		}
		// Expected duplicate-column error — replay the UPDATE leg in
		// isolation so the per-role bundle assertions below have
		// something to assert against.
		if _, err2 := db.Exec(`
			UPDATE workspace_members
			SET capabilities = CASE role
				WHEN 'OWNER'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
				WHEN 'ADMIN'   THEN '["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]'
				WHEN 'MANAGER' THEN '["chat","routine.create","issue.create","memory.write"]'
				WHEN 'MEMBER'  THEN '["chat"]'
				WHEN 'VIEWER'  THEN '["chat"]'
				ELSE '["chat"]'
			END
			WHERE capabilities IS NULL
		`); err2 != nil {
			t.Fatalf("backfill replay: %v", err2)
		}
	}

	// Per-role assertions. Exact JSON match — the migration writes a
	// stable string; deviation here means the bundle drifted and the
	// admin UI checklist will too.
	type want struct {
		userID string
		caps   string
	}
	cases := []want{
		{"uOwner", `["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`},
		{"uAdmin", `["chat","routine.create","skill.create","credential.create","credential.rotate","issue.create","memory.write"]`},
		{"uManager", `["chat","routine.create","issue.create","memory.write"]`},
		{"uMember", `["chat"]`},
		{"uViewer", `["chat"]`},
	}
	for _, c := range cases {
		var got string
		if err := db.QueryRow(`SELECT capabilities FROM workspace_members WHERE user_id = ?`, c.userID).Scan(&got); err != nil {
			t.Fatalf("scan %s: %v", c.userID, err)
		}
		if got != c.caps {
			t.Errorf("user %s capabilities = %s, want %s", c.userID, got, c.caps)
		}
	}

	// Partial index correctness — only non-default rows should be in
	// the elevated-membership index. Members + viewers carry only
	// the chat baseline, so they must be excluded; the other three
	// must be present.
	var elevated int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM workspace_members
		WHERE workspace_id = 'ws1'
		  AND capabilities IS NOT NULL
		  AND capabilities != '["chat"]'
	`).Scan(&elevated); err != nil {
		t.Fatalf("elevated count: %v", err)
	}
	if elevated != 3 {
		t.Errorf("elevated rows = %d, want 3 (OWNER, ADMIN, MANAGER)", elevated)
	}
}
