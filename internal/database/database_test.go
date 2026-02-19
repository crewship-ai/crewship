package database

import (
	"context"
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
	if !found {
		t.Error("memory_config column not found on agents table after migration")
	}

	// Verify migration 3 is recorded
	var version int
	err = db.QueryRow("SELECT version FROM _migrations WHERE version = 3").Scan(&version)
	if err != nil {
		t.Errorf("migration 3 not recorded: %v", err)
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
