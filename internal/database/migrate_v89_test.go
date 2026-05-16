package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

// migrateV89Counter generates unique in-memory DB names so parallel test
// runs don't share state. The bare `file::memory:?cache=shared` DSN
// points every connection at the SAME global in-memory database, so
// `t.Parallel()` was bleeding seed rows between sibling tests — same
// pattern fix as internal/backup/catalog_test.go's catalogMemCounter.
var migrateV89Counter atomic.Int64

// applyMigrationsUpTo applies every migration with version ≤ max to db,
// bypassing the package-level migrations slice's all-or-nothing Migrate()
// API. Used by the upgrade-path test to land a legacy schema, seed it,
// and then apply v89 in isolation against populated tables.
func applyMigrationsUpTo(ctx context.Context, db *sql.DB, max int, logger *slog.Logger) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create _migrations: %w", err)
	}
	for _, m := range migrations {
		if m.version > max {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin v%d: %w", m.version, err)
		}
		if m.fn != nil {
			if err := m.fn(ctx, tx, logger); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply v%d fn: %w", m.version, err)
			}
		} else if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply v%d sql: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO _migrations (version, name) VALUES (?, ?)", m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record v%d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit v%d: %w", m.version, err)
		}
	}
	return nil
}

// findMigration returns a pointer to the migration with the given version,
// or an error if the slice was renumbered. Used by the upgrade-path test
// so a future v89 rename fails loudly here instead of silently no-op'ing.
func findMigration(version int) (*migration, error) {
	for i := range migrations {
		if migrations[i].version == version {
			return &migrations[i], nil
		}
	}
	return nil, errors.New("migration version not found")
}

// TestMigration089_ChatAssignmentCascades verifies v89's runtime triggers
// give us the cascade semantics the v01 init's NO-ACTION FKs lacked:
// deleting a user nulls created_by on surviving chats, and deleting a
// chat removes its sidebar assignments instead of blocking on FK.
//
// Both behaviors used to fail with "FOREIGN KEY constraint failed" the
// moment a beta tester tried to delete an account that owned a chat,
// or a chat that had any open assignments. v89 installs BEFORE-DELETE
// triggers that clear/cascade dependents before the parent delete runs.
func TestMigration089_ChatAssignmentCascades(t *testing.T) {
	t.Parallel()

	name := fmt.Sprintf("crewship-migrate-v89-%d", migrateV89Counter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
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

// TestMigration089_AppliesCleanlyAgainstPopulatedLegacyDB exercises the
// v88→v89 upgrade path against a database that already has chats and
// assignments rows — the state every existing beta install is in when
// they pull this PR. The earlier "recreate-table-and-swap" recipe
// passed the fresh-install test above but blew up at COMMIT here
// because DROP TABLE chats queued a deferred FK violation that the
// rename couldn't clear.
//
// This test lands v01..v88, seeds two chats and two assignments with a
// full column payload, applies v89 alone, and asserts:
//   - the apply succeeds (the prior recipe failed at COMMIT)
//   - every legacy row survives untouched
//   - the new cascade semantics fire on subsequent deletes
func TestMigration089_AppliesCleanlyAgainstPopulatedLegacyDB(t *testing.T) {
	t.Parallel()

	name := fmt.Sprintf("crewship-migrate-v89-upgrade-%d", migrateV89Counter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Step 1: land v01..v88 — the schema a v0.1.0-beta.1 install runs.
	if err := applyMigrationsUpTo(ctx, db, 88, logger); err != nil {
		t.Fatalf("apply v01..v88: %v", err)
	}

	// Step 2: seed legacy rows. Two chats (one owned, one anonymous —
	// exercises the nullable created_by path) and two assignments with
	// distinctive payload so post-migration assertions can confirm no
	// values shifted columns.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed (%s): %v", q, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'ws', 'ws')`)
	exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'u@example.com', 'Legacy User')`)
	exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m1', 'ws1', 'u1', 'OWNER')`)
	exec(`INSERT INTO agents (id, workspace_id, name, slug, agent_role) VALUES ('a1', 'ws1', 'agent', 'agent', 'WORKER')`)
	exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, message_count, jsonl_path, origin)
	      VALUES ('c-owned', 'a1', 'ws1', 'u1', 'Owned chat', 'CHAT', 5, '/tmp/c.jsonl', 'web')`)
	exec(`INSERT INTO chats (id, agent_id, workspace_id, title)
	      VALUES ('c-anon', 'a1', 'ws1', 'Anonymous chat')`)
	exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, result_summary, group_id)
	      VALUES ('asn1', 'ws1', 'c-owned', 'a1', 'a1', 'task one', 'DONE', 'finished', 'grp-1')`)
	exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task)
	      VALUES ('asn2', 'ws1', 'c-anon', 'a1', 'a1', 'task two')`)

	// Step 3: apply v89 alone inside a transaction (mirrors what
	// Migrate() does for every migration). With the trigger-based
	// approach there's no FK juggling — the migration just installs
	// two BEFORE-DELETE triggers and commits.
	v89, err := findMigration(89)
	if err != nil {
		t.Fatalf("find v89: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin v89 tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, v89.sql); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v89 against populated tables: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v89: %v", err)
	}

	// Step 4a: row counts unchanged — triggers don't touch existing data.
	assertCount := func(table string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s: want %d rows after v89, got %d", table, want, got)
		}
	}
	assertCount("chats", 2)
	assertCount("assignments", 2)

	// Step 4b: column payload survives — a swap recipe with the wrong
	// INSERT-SELECT column order would land here with shuffled values.
	// With triggers there's no swap, but the check is cheap insurance
	// against a future v89 rewrite that DOES move data.
	var (
		title, createdBy, mode, jsonlPath, origin sql.NullString
		messageCount                              int
	)
	row := db.QueryRow(`SELECT title, created_by, mode, message_count, jsonl_path, origin FROM chats WHERE id = 'c-owned'`)
	if err := row.Scan(&title, &createdBy, &mode, &messageCount, &jsonlPath, &origin); err != nil {
		t.Fatalf("scan c-owned: %v", err)
	}
	if title.String != "Owned chat" || createdBy.String != "u1" || mode.String != "CHAT" ||
		messageCount != 5 || jsonlPath.String != "/tmp/c.jsonl" || origin.String != "web" {
		t.Errorf("c-owned payload mangled after v89: title=%q created_by=%q mode=%q msgs=%d jsonl=%q origin=%q",
			title.String, createdBy.String, mode.String, messageCount, jsonlPath.String, origin.String)
	}

	var asnStatus, asnSummary, asnGroup sql.NullString
	if err := db.QueryRow(`SELECT status, result_summary, group_id FROM assignments WHERE id = 'asn1'`).
		Scan(&asnStatus, &asnSummary, &asnGroup); err != nil {
		t.Fatalf("scan asn1: %v", err)
	}
	if asnStatus.String != "DONE" || asnSummary.String != "finished" || asnGroup.String != "grp-1" {
		t.Errorf("asn1 payload mangled: status=%q summary=%q group=%q",
			asnStatus.String, asnSummary.String, asnGroup.String)
	}

	// Step 4c: new cascade semantics are live — the triggers actually
	// fire on a populated legacy database, not just on a fresh install.
	if _, err := db.Exec(`DELETE FROM users WHERE id = 'u1'`); err != nil {
		t.Fatalf("delete user after v89 should succeed (trigger nulls created_by): %v", err)
	}
	var ownerAfter sql.NullString
	if err := db.QueryRow(`SELECT created_by FROM chats WHERE id = 'c-owned'`).Scan(&ownerAfter); err != nil {
		t.Fatalf("re-read c-owned creator: %v", err)
	}
	if ownerAfter.Valid {
		t.Errorf("c-owned.created_by should be NULL after user delete; got %q", ownerAfter.String)
	}

	if _, err := db.Exec(`DELETE FROM chats WHERE id = 'c-owned'`); err != nil {
		t.Fatalf("delete chat after v89 should succeed (trigger cascades asn1): %v", err)
	}
	var asn1Left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id = 'asn1'`).Scan(&asn1Left); err != nil {
		t.Fatalf("re-count asn1: %v", err)
	}
	if asn1Left != 0 {
		t.Errorf("asn1 should cascade on chat delete; %d rows still present", asn1Left)
	}
	// asn2 (different chat) must remain untouched.
	var asn2Left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id = 'asn2'`).Scan(&asn2Left); err != nil {
		t.Fatalf("re-count asn2: %v", err)
	}
	if asn2Left != 1 {
		t.Errorf("asn2 should survive c-owned delete (different chat); got %d", asn2Left)
	}
}
