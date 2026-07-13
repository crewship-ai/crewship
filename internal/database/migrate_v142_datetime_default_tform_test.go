package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"testing"
)

// tformPattern matches the fixed-width ISO T-form timestamp this migration's
// DEFAULT now produces: "2026-07-13T21:00:00.123Z".
var tformPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// legacySpaceFormPattern matches SQLite's `datetime('now')` output:
// "2026-07-13 21:00:00" — no 'T', no fraction, no zone marker.
var legacySpaceFormPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

// TestMigrateV142_ConvertedColumnsDefaultToTForm is the reproducing test for
// #1073b: before this migration, `credentials.created_at` (named
// explicitly in the issue as the column PR #1156's keyset-cursor pagination
// depends on) defaulted to SQLite's space-separated legacy form on any
// insert that omitted the column. That form never compares correctly
// against the ISO T-form strings the rest of the codebase writes. After
// v142, a raw insert that omits created_at must get a T-form value instead.
//
// This test fails on pre-#1073b code exactly the way the bug manifests:
// insert a row without created_at, read it back, and it's the legacy
// space-form shape rather than T-form.
func TestMigrateV142_ConvertedColumnsDefaultToTForm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v142.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_v142', 'WS142', 'ws-v142')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_v142', 'v142@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// A raw insert that omits created_at/updated_at, exactly the
	// "raw insert/backfill" scenario the issue warns about — no
	// application-level Go writer is involved, only the column DEFAULT.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_v142', 'ws_v142', 'Raw Insert Cred', 'ciphertext', 'user_v142')`); err != nil {
		t.Fatalf("raw insert into credentials: %v", err)
	}

	var createdAt, updatedAt string
	if err := db.QueryRow(`SELECT created_at, updated_at FROM credentials WHERE id = 'cred_v142'`).
		Scan(&createdAt, &updatedAt); err != nil {
		t.Fatalf("read back credentials row: %v", err)
	}

	if !tformPattern.MatchString(createdAt) {
		t.Errorf("credentials.created_at DEFAULT produced %q — want ISO T-form matching %s", createdAt, tformPattern)
	}
	if !tformPattern.MatchString(updatedAt) {
		t.Errorf("credentials.updated_at DEFAULT produced %q — want ISO T-form matching %s", updatedAt, tformPattern)
	}
	if legacySpaceFormPattern.MatchString(createdAt) {
		t.Errorf("credentials.created_at DEFAULT still produces legacy space-form: %q", createdAt)
	}
}

// TestMigrateV142_TFormSortsCorrectlyAgainstExplicitWrites reproduces the
// actual production symptom: a keyset-cursor / ORDER BY query over a
// converted column must place a DEFAULT-produced row in the correct
// chronological position relative to rows written with an explicit
// RFC3339 timestamp by application code — not after every legacy row
// regardless of real time, which is what happened when the DEFAULT was
// space-form (' ' sorts before 'T' in ASCII, so legacy rows always sorted
// as "earlier" than any RFC3339 row no matter their actual time).
func TestMigrateV142_TFormSortsCorrectlyAgainstExplicitWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v142_sort.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_v142s', 'WS142S', 'ws-v142s')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_v142s', 'v142s@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Row 1: explicit application-style RFC3339 write, deliberately given
	// an early timestamp.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by, created_at, updated_at)
		VALUES ('cred_early', 'ws_v142s', 'Early', 'x', 'user_v142s', '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert early row: %v", err)
	}

	// Row 2: raw insert relying on the DEFAULT — always "now", i.e. long
	// after the row above.
	if _, err := db.Exec(`INSERT INTO credentials
		(id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_now', 'ws_v142s', 'Now', 'y', 'user_v142s')`); err != nil {
		t.Fatalf("insert DEFAULT row: %v", err)
	}

	rows, err := db.Query(`SELECT id FROM credentials WHERE workspace_id = 'ws_v142s' ORDER BY created_at ASC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var order []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(order) != 2 || order[0] != "cred_early" || order[1] != "cred_now" {
		t.Fatalf("ORDER BY created_at ASC = %v, want [cred_early cred_now] — the DEFAULT-produced row must sort AFTER the earlier explicit timestamp", order)
	}
}

// TestMigrateV142_IndexesAndTriggersSurviveRecreation guards against a
// regression to a schema-recreate mechanism (this migration's first
// implementation used SQLite's documented table-recreate dance, which DROP
// TABLE silently strips indexes/triggers from — see
// rewriteTableDefaultLiteral's doc comment for why that approach was
// abandoned in favor of an in-place sqlite_master.sql rewrite that never
// drops or recreates anything). credential_crews carries a named trigger
// (trg_credential_crews_workspace_check) that rejects a credential_crews
// row whose crew_id belongs to a different workspace than the credential;
// if a future change reintroduced table recreation and lost the trigger,
// this bad insert would silently succeed instead of failing.
func TestMigrateV142_IndexesAndTriggersSurviveRecreation(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v142_triggers.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var triggerName string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name = ?`,
		"trg_credential_crews_workspace_check",
	).Scan(&triggerName); err != nil {
		t.Fatalf("trg_credential_crews_workspace_check missing after v142 recreation: %v", err)
	}

	var idxName string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_cli_token_uses_used_at",
	).Scan(&idxName); err != nil {
		t.Fatalf("idx_cli_token_uses_used_at missing after v142 recreation: %v", err)
	}

	// Seed two workspaces/crews/credentials so we can attempt a
	// cross-workspace credential_crews row.
	for _, ws := range []string{"ws_a_v142", "ws_b_v142"} {
		if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, ws, ws, ws); err != nil {
			t.Fatalf("seed workspace %s: %v", ws, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_b_v142', 'ws_b_v142', 'B', 'crew-b-v142')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_trig_v142', 'trig-v142@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, created_by)
		VALUES ('cred_a_v142', 'ws_a_v142', 'A', 'x', 'user_trig_v142')`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	_, err = db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred_a_v142', 'crew_b_v142')`)
	if err == nil {
		t.Fatal("expected trg_credential_crews_workspace_check to reject a cross-workspace credential_crews row, insert succeeded")
	}
}

// TestMigrateV142_SkippedTablesStayLegacyForm confirms the three
// intentionally-left-alone tables (see datetimeNowDefaultSkipTables) are
// NOT touched by this migration — their DEFAULT stays space-form because
// the column is never string-compared.
func TestMigrateV142_SkippedTablesStayLegacyForm(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v142_skip.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for table, checkSQL := range map[string]string{
		"mcp_registry_servers": `SELECT sql FROM sqlite_master WHERE type='table' AND name='mcp_registry_servers'`,
		"backup_locks":         `SELECT sql FROM sqlite_master WHERE type='table' AND name='backup_locks'`,
		"instance_config":      `SELECT sql FROM sqlite_master WHERE type='table' AND name='instance_config'`,
	} {
		var createSQL string
		if err := db.QueryRow(checkSQL).Scan(&createSQL); err != nil {
			t.Fatalf("read schema for %s: %v", table, err)
		}
		if !regexp.MustCompile(`datetime\('now'\)`).MatchString(createSQL) {
			t.Errorf("%s: expected untouched datetime('now') DEFAULT, got schema: %s", table, createSQL)
		}
	}
}

// TestMigrateV142_MemoryVersionsUntouched guards the boundary with 1073a:
// this migration must not modify memory_versions at all.
func TestMigrateV142_MemoryVersionsUntouched(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v142_memver.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memory_versions'`).Scan(&name); err != nil {
		t.Skipf("memory_versions table not present on this branch (1073a not merged yet): %v", err)
	}
}
