package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

// TestMigration089_ChatAssignmentCascades verifies v89's fix for the two
// FK gaps the v01 init shipped with: chats.created_by getting SET NULL on
// user delete, and assignments.chat_id getting CASCADE on chat delete.
//
// Both behaviors used to default to SQLite NO ACTION, which surfaced as
// "FOREIGN KEY constraint failed" any time the user tried to delete an
// account that owned old chats, or a chat that had any sidebar
// assignments. The test seeds the minimum row set needed to exercise
// both delete paths and asserts the downstream state after each.
func TestMigration089_ChatAssignmentCascades(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Minimal row chain: workspace → user → agent → chat → assignment.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed (%s): %v", q, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'ws', 'ws')`)
	exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'u@example.com', 'u')`)
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'ws1', 'u1', 'OWNER')`)
	exec(`INSERT INTO agents (id, workspace_id, name, slug, agent_role) VALUES ('a1', 'ws1', 'agent', 'agent', 'WORKER')`)
	exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by) VALUES ('c1', 'a1', 'ws1', 'u1')`)
	exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task)
	      VALUES ('asn1', 'ws1', 'c1', 'a1', 'a1', 'do thing')`)

	// 1. User delete: created_by must SET NULL on the surviving chat.
	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatalf("delete user (pre-v89 would fail with NO ACTION): %v", err)
	}
	var owner sql.NullString
	if err := db.QueryRow(`SELECT created_by FROM chats WHERE id = 'c1'`).Scan(&owner); err != nil {
		t.Fatalf("select chat owner: %v", err)
	}
	if owner.Valid {
		t.Errorf("chat.created_by should be NULL after user delete; got %q", owner.String)
	}

	// 2. Chat delete: assignment must cascade away.
	if _, err := db.Exec(`DELETE FROM chats WHERE id = 'c1'`); err != nil {
		t.Fatalf("delete chat (pre-v89 would fail on assignment FK): %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id = 'asn1'`).Scan(&remaining); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if remaining != 0 {
		t.Errorf("assignment should cascade on chat delete; %d rows still present", remaining)
	}
}
