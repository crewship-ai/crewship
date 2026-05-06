package database

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		// agent_runs was dropped in migration v61 (unified-journal Phase
		// J). Runs are now reconstructed from journal_entries grouped by
		// trace_id; agent_runs_archive holds the pre-migration data for
		// one release cycle as an insurance policy.
		"chats", "agent_runs_archive", "audit_logs",
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

// TestMigrateCredentialAuditSignal verifies migration v65 adds the columns the
// 5-state status taxonomy + last-used IP ringbuffer rely on. Mirrors the shape
// of TestMigrateMemoryConfigColumn — pragma_table_info introspection plus a
// round-trip insert/update so we catch nullability or type regressions.
func TestMigrateCredentialAuditSignal(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "credaudit.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	wantCols := map[string]string{"last_used_at": "TEXT", "last_used_ips": "TEXT"}
	got := map[string]string{}
	rows, err := db.Query("PRAGMA table_info(credentials)")
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	defer rows.Close()
	var cid int
	var colName, colType string
	var notNull, dfltValue, pk interface{}
	for rows.Next() {
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, want := wantCols[colName]; want {
			got[colName] = colType
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	for col, typ := range wantCols {
		if got[col] != typ {
			t.Errorf("column %s: got type %q, want %q", col, got[col], typ)
		}
	}

	// Round-trip: existing row may insert with nulls (pre-sidecar credentials),
	// then sidecar populates the columns later.
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1', 'a@b.c')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('w1', 'W', 'w')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('c1', 'w1', 'TEST_KEY', 'enc', 'u1')
	`); err != nil {
		t.Fatalf("insert credential without audit columns: %v", err)
	}
	// Sidecar update path — JSON array, max 5 enforced in Go (not schema).
	if _, err := db.Exec(`
		UPDATE credentials SET last_used_at = '2026-05-04T10:00:00Z',
		                       last_used_ips = '["1.2.3.4","5.6.7.8"]'
		WHERE id = 'c1'
	`); err != nil {
		t.Fatalf("update audit cols: %v", err)
	}
	var lastUsed, lastIPs *string
	if err := db.QueryRow(`SELECT last_used_at, last_used_ips FROM credentials WHERE id = 'c1'`).Scan(&lastUsed, &lastIPs); err != nil {
		t.Fatalf("read audit cols: %v", err)
	}
	if lastUsed == nil || *lastUsed != "2026-05-04T10:00:00Z" {
		t.Errorf("last_used_at: got %v, want 2026-05-04T10:00:00Z", lastUsed)
	}
	if lastIPs == nil || *lastIPs != `["1.2.3.4","5.6.7.8"]` {
		t.Errorf("last_used_ips: got %v, want JSON array", lastIPs)
	}

	var version int
	if err := db.QueryRow("SELECT version FROM _migrations WHERE version = 65").Scan(&version); err != nil {
		t.Errorf("migration 65 not recorded: %v", err)
	}
}

// TestMigrateVersionCollision guards the collision check in Migrate(): if the
// _migrations table already has a different name recorded for the version the
// code is about to apply, the runner must fail loudly instead of silently
// skipping. This prevents the classic two-branch-merge schema divergence
// (both PRs claim the same version number with different SQL).
func TestMigrateVersionCollision(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "collision.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// First pass: apply everything normally.
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("initial Migrate: %v", err)
	}

	// Tamper with _migrations as if a sibling PR had been merged first with
	// a different name for the same version. Pick the latest version because
	// it's the one the real-world collision would hit.
	var latest int
	if err := db.QueryRow("SELECT MAX(version) FROM _migrations").Scan(&latest); err != nil {
		t.Fatalf("query max version: %v", err)
	}
	if _, err := db.Exec("UPDATE _migrations SET name = ? WHERE version = ?",
		"sibling_pr_claimed_this_slot", latest); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	// Re-run: must fail with a collision error naming both sides.
	err = Migrate(context.Background(), db.DB, logger)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "collision") {
		t.Errorf("error message missing 'collision': %q", msg)
	}
	if !strings.Contains(msg, "sibling_pr_claimed_this_slot") {
		t.Errorf("error message missing applied name: %q", msg)
	}
}

// TestMigrateIdempotentWithMatchingNames is the happy-path counterpart: when
// the _migrations entry matches the code's migration definition, re-running
// must succeed silently. Regression guard for over-eager collision checks.
func TestMigrateIdempotentWithMatchingNames(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "idempotent.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("second Migrate (should be no-op): %v", err)
	}
}

// TestOpenChmodsDBFile verifies the file-permission tightening applied during
// Open(). The data directory and the DB file (plus WAL/SHM if they exist) must
// be owner-only after opening.
func TestOpenChmodsDBFile(t *testing.T) {
	// chmod behavior is POSIX-specific. Skip on non-POSIX.
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "perms.db")

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Force a write so the WAL file gets materialized.
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS t (x INTEGER); INSERT INTO t VALUES (1);"); err != nil {
		t.Fatalf("write: %v", err)
	}

	mode := func(p string) os.FileMode {
		fi, err := os.Stat(p)
		if err != nil {
			return 0
		}
		return fi.Mode().Perm()
	}

	if got := mode(dbPath); got != 0o600 {
		t.Errorf("db file mode = %o, want 0600", got)
	}
	// The WAL file may or may not be present depending on timing; only assert
	// when it actually exists.
	if _, err := os.Stat(dbPath + "-wal"); err == nil {
		if got := mode(dbPath + "-wal"); got != 0o600 {
			t.Errorf("wal file mode = %o, want 0600", got)
		}
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

// TestMigrateConnectorIDColumn verifies migration v76 adds the
// connector_id column on both workspace_mcp_servers and crew_mcp_servers
// and that the column is nullable (existing rows keep working without
// a manifest reference).
func TestMigrateConnectorIDColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "connector.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Verify both tables got the column. The pragma_table_info pattern
	// matches what TestMigrateMemoryConfigColumn does — keeping the
	// idiom consistent so future migration tests are mechanical.
	for _, table := range []string{"workspace_mcp_servers", "crew_mcp_servers"} {
		t.Run(table, func(t *testing.T) {
			var cid int
			var colName, colType string
			var notNull, dfltValue, pk interface{}
			rows, err := db.Query("PRAGMA table_info(" + table + ")")
			if err != nil {
				t.Fatalf("pragma: %v", err)
			}
			defer rows.Close()
			found := false
			for rows.Next() {
				if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
					t.Fatalf("scan: %v", err)
				}
				if colName == "connector_id" {
					found = true
					if colType != "TEXT" {
						t.Errorf("%s.connector_id type = %q, want TEXT", table, colType)
					}
					// Nullable: notNull must be 0 (or interface holding 0/false).
					if nn, ok := notNull.(int64); ok && nn != 0 {
						t.Errorf("%s.connector_id NOT NULL = %v, want nullable", table, nn)
					}
					break
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows err: %v", err)
			}
			if !found {
				t.Errorf("%s.connector_id column missing after migration", table)
			}
		})
	}

	// Migration row recorded.
	var version int
	if err := db.QueryRow("SELECT version FROM _migrations WHERE version = 76").Scan(&version); err == sql.ErrNoRows {
		t.Error("migration 76 not recorded in _migrations")
	} else if err != nil {
		t.Fatalf("query _migrations: %v", err)
	}

	// Partial index exists. SQLite stores them in sqlite_master.
	for _, idx := range []string{"idx_workspace_mcp_connector_id", "idx_crew_mcp_connector_id"} {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?",
			idx,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("partial index %q not created", idx)
		} else if err != nil {
			t.Fatalf("sqlite_master: %v", err)
		}
	}
}
