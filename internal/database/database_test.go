package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if db.Path() != dbPath {
		t.Errorf("Path() = %q, want %q", db.Path(), dbPath)
	}

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Error("foreign_keys not enabled")
	}
}

func TestOpenEmptyDSN(t *testing.T) {
	_, err := Open("")
	if err == nil {
		t.Error("expected error for empty DSN")
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c", "test.db")

	db, err := Open("file:" + nested)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()

	if _, err := os.Stat(nested); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate.db")

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	tables := []string{
		"users", "accounts", "sessions", "verification_tokens",
		"workspaces", "workspace_members", "workspace_invitations",
		"crews", "crew_members", "agents", "assignments",
		"skills", "skill_reviews", "agent_skills",
		"credentials", "agent_credentials",
		"chats", "agent_runs", "audit_logs",
		"subscriptions", "plans", "feature_flags", "feature_flag_overrides",
		"agent_config_history",
	}

	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}

	// Run again -- should be idempotent
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate (idempotent): %v", err)
	}
}

func TestMigrateMemoryConfigColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Verify memory_config column exists on agents table
	var cid int
	var colName, colType string
	var notNull, dfltValue, pk interface{}
	found := false
	rows, err := db.Query("PRAGMA table_info(agents)")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if colName == "memory_config" {
			found = true
			if colType != "TEXT" {
				t.Errorf("memory_config type = %q, want TEXT", colType)
			}
			break
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if !found {
		t.Error("memory_config column not found on agents table after migration")
	}

	// Verify migration 3 is recorded
	var version int
	err = db.QueryRow("SELECT version FROM _migrations WHERE version = 3").Scan(&version)
	if err == sql.ErrNoRows {
		t.Errorf("migration 3 not recorded")
	} else if err != nil {
		t.Fatalf("query migration 3: %v", err)
	}

	// Verify memory_config is nullable (can insert agent without it)
	_, err = db.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'test@example.com')`)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Test', 'test')`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES ('a1', 'w1', 'Agent', 'agent')`)
	if err != nil {
		t.Fatalf("insert agent without memory_config: %v", err)
	}

	// Verify we can set memory_config
	_, err = db.Exec(`UPDATE agents SET memory_config = '{"max_size_mb": 10}' WHERE id = 'a1'`)
	if err != nil {
		t.Fatalf("update memory_config: %v", err)
	}

	var memCfg *string
	err = db.QueryRow("SELECT memory_config FROM agents WHERE id = 'a1'").Scan(&memCfg)
	if err != nil {
		t.Fatalf("read memory_config: %v", err)
	}
	if memCfg == nil || *memCfg != `{"max_size_mb": 10}` {
		t.Errorf("unexpected memory_config: %v", memCfg)
	}
}

func TestMigrateInsertAndQuery(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "crud.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err = db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'test@example.com', 'Test User')`)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	_, err = db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'Test Workspace', 'test-workspace')`)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	_, err = db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm1', 'w1', 'u1', 'OWNER')`)
	if err != nil {
		t.Fatalf("insert workspace member: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workspace_members WHERE workspace_id = 'w1'").Scan(&count); err != nil {
		t.Fatalf("query members: %v", err)
	}
	if count != 1 {
		t.Errorf("member count = %d, want 1", count)
	}
}

// TestMigrationBackfillLegacyTimestamps is a regression for a CodeRabbit
// finding on PR #130: the codebase writes timestamps via time.RFC3339 but
// the schema DEFAULTs use SQLite's `datetime('now')` which produces the
// legacy `YYYY-MM-DD HH:MM:SS` format. Rows created by relying on the
// DEFAULT end up in a different format than rows written by app code, and
// text-based ORDER BY sorts them wrong (' ' < 'T'). The backfill migration
// normalizes all legacy rows to RFC3339 in place.
//
// The test exercises three properties: (1) legacy rows are converted,
// (2) already-RFC3339 rows are left alone, (3) the migration is idempotent
// (running it twice is a no-op).
func TestMigrationBackfillLegacyTimestamps(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "backfill.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// At this point all migrations (including 44) have already run. Insert
	// synthetic legacy-format rows AFTER the fact to simulate data that was
	// created before the backfill ever ran, then re-run the migration body
	// directly against the DB to test its logic in isolation.
	_, err = db.Exec(`INSERT INTO users (id, email, created_at, updated_at) VALUES
		('u-legacy', 'legacy@example.com', '2026-04-10 12:34:56', '2026-04-10 13:00:00'),
		('u-rfc3339', 'rfc@example.com',  '2026-04-10T12:34:56Z', '2026-04-10T13:00:00Z')`)
	if err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// Run the backfill directly via a fresh transaction (migration 44 is
	// already recorded, so we can't re-run through Migrate).
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := migrationBackfillLegacyTimestamps(context.Background(), tx, logger); err != nil {
		tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Legacy row is now RFC3339.
	var legacyCreated, legacyUpdated string
	err = db.QueryRow(`SELECT created_at, updated_at FROM users WHERE id = 'u-legacy'`).Scan(&legacyCreated, &legacyUpdated)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if legacyCreated != "2026-04-10T12:34:56Z" {
		t.Errorf("legacy created_at = %q, want 2026-04-10T12:34:56Z", legacyCreated)
	}
	if legacyUpdated != "2026-04-10T13:00:00Z" {
		t.Errorf("legacy updated_at = %q, want 2026-04-10T13:00:00Z", legacyUpdated)
	}

	// RFC3339 row was NOT touched (idempotency property 1: already-normalized
	// rows stay exactly as they were, no spurious 'Z' appended).
	var rfcCreated, rfcUpdated string
	err = db.QueryRow(`SELECT created_at, updated_at FROM users WHERE id = 'u-rfc3339'`).Scan(&rfcCreated, &rfcUpdated)
	if err != nil {
		t.Fatalf("read rfc3339: %v", err)
	}
	if rfcCreated != "2026-04-10T12:34:56Z" {
		t.Errorf("rfc3339 created_at corrupted: %q", rfcCreated)
	}
	if rfcUpdated != "2026-04-10T13:00:00Z" {
		t.Errorf("rfc3339 updated_at corrupted: %q", rfcUpdated)
	}

	// Run the backfill a second time — it must be a pure no-op.
	tx2, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	if err := migrationBackfillLegacyTimestamps(context.Background(), tx2, logger); err != nil {
		tx2.Rollback()
		t.Fatalf("second backfill: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit2: %v", err)
	}

	var legacyCreatedAfter string
	if err := db.QueryRow(`SELECT created_at FROM users WHERE id = 'u-legacy'`).Scan(&legacyCreatedAfter); err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	if legacyCreatedAfter != legacyCreated {
		t.Errorf("second run mutated row: %q -> %q", legacyCreated, legacyCreatedAfter)
	}
}

// TestIsSafeIdent guards the conservative identifier filter used by the
// backfill migration. If this ever starts accepting funky characters we
// could wind up interpolating user-supplied garbage into dynamic SQL.
func TestIsSafeIdent(t *testing.T) {
	valid := []string{"users", "workspace_members", "A", "ab12", "_test"}
	invalid := []string{"", "1users", "users;DROP", "user's", "user name", "user-name", "users.col"}

	// "_test" starts with underscore but letter rules allow underscore as a
	// valid leading char; we're not filtering _-prefixed tables here (that's
	// done separately in the sqlite_master query).
	for _, s := range valid {
		if !isSafeIdent(s) {
			t.Errorf("isSafeIdent(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isSafeIdent(s) {
			t.Errorf("isSafeIdent(%q) = true, want false", s)
		}
	}
}

// TestIsTimestampColumnName guards the column-name heuristic. Adjust with
// care: loosening it risks the backfill touching a non-timestamp column that
// happens to contain a date-shaped string; tightening it risks missing real
// timestamp columns added by future migrations.
func TestIsTimestampColumnName(t *testing.T) {
	timestamps := []string{
		"created_at", "updated_at", "deleted_at", "started_at",
		"ended_at", "finished_at", "completed_at", "last_used_at",
		"token_expires_at", "resolved_at", "expires",
	}
	others := []string{
		"id", "name", "email", "password", "description", "expires_in",
		"at_most", "created", // "created" without _at suffix
	}
	for _, n := range timestamps {
		if !isTimestampColumnName(n) {
			t.Errorf("isTimestampColumnName(%q) = false, want true", n)
		}
	}
	for _, n := range others {
		if isTimestampColumnName(n) {
			t.Errorf("isTimestampColumnName(%q) = true, want false", n)
		}
	}
}
