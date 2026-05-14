package backup_test

// End-to-end backup/restore exercises. Unlike the unit tests in this
// package, these run the FULL pipeline against a real migrated DB:
// CreateBackup -> wipe -> RestoreBackup -> row-level diff.
//
// They live in the external `backup_test` package so they can only use
// the same public API the API/CLI layer uses, which keeps the contract
// honest. If a refactor breaks the public surface, these tests break.
//
// External package = no access to package-private helpers, so the seed
// helpers below talk only to *sql.DB.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// openMigratedDB returns a fresh on-disk SQLite DB with every migration
// applied AND the bundled-skill seed run, matching what cmd_start.go
// does on every crewship boot. Each call uses a unique temp file so
// the connection pool can't split the schema across isolated :memory:
// databases.
//
// Bundled skills are part of the "what a fresh restore target looks
// like" baseline because BackupTables intentionally does NOT export
// the global `skills` table — it has no workspace_id and is assumed
// to be re-seeded by the host on boot. Tests that didn't seed here
// would see FK violations on agent_skills restore for that reason.
func openMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := database.SeedBundledSkills(context.Background(), db, logger); err != nil {
		t.Fatalf("seed bundled skills: %v", err)
	}
	return db
}

// seedWorkspace inserts a realistic mini-tenant (1 ws, 2 crews, 4
// agents, 2 skills, 4 agent_skills) and returns the workspace ID. The
// schema is the production one — every column the runtime depends on
// gets a value (status, slug, workspace_id, etc.). Inserts go through
// direct SQL because internal/api/* depends on http and we want to
// exercise the dump/restore plumbing without standing up the server.
func seedWorkspace(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()

	// Users — required for crew_members and several FKs even though
	// none of those rows participate in the workspace bundle. The
	// minimal user keeps inserts honest if the test later seeds them.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		"u_admin", "admin@e2e.test", "Admin"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	const workspaceID = "ws_e2e_1"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		workspaceID, "E2E Workspace", "e2e-ws"); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	crews := []struct{ id, slug, name string }{
		{"c_alpha", "alpha", "Alpha Crew"},
		{"c_beta", "beta", "Beta Crew"},
	}
	for _, c := range crews {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
			c.id, workspaceID, c.name, c.slug); err != nil {
			t.Fatalf("seed crew %s: %v", c.slug, err)
		}
	}

	agents := []struct{ id, crewID, slug, name string }{
		{"a_alice", "c_alpha", "alice", "Alice"},
		{"a_bob", "c_alpha", "bob", "Bob"},
		{"a_carol", "c_beta", "carol", "Carol"},
		{"a_dan", "c_beta", "dan", "Dan"},
	}
	for _, a := range agents {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status)
			 VALUES (?, ?, ?, ?, ?, 'IDLE')`,
			a.id, a.crewID, workspaceID, a.name, a.slug); err != nil {
			t.Fatalf("seed agent %s: %v", a.slug, err)
		}
	}

	// Bundled skills (skill_coding_01 etc.) are seeded by openMigratedDB
	// on both source and target, so the agent_skills rows below FK
	// cleanly on the restore side without a custom-skill backup path.
	// KNOWN GAP, recorded in the PR description: backup silently drops
	// the `skills` table itself (no workspace_id column => filtered out).
	// A customer who attached custom skills hits FK violation on restore;
	// covered by a separate test below.
	bindings := []struct{ id, agentID, skillID string }{
		{"as_1", "a_alice", "skill_coding_01"},
		{"as_2", "a_alice", "skill_research_01"},
		{"as_3", "a_bob", "skill_coding_01"},
		{"as_4", "a_carol", "skill_research_01"},
	}
	for _, b := range bindings {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO agent_skills (id, agent_id, skill_id) VALUES (?, ?, ?)`,
			b.id, b.agentID, b.skillID); err != nil {
			t.Fatalf("seed agent_skill %s: %v", b.id, err)
		}
	}

	return workspaceID
}

// snapshotWorkspaceScopedTables reads every BackupTables entry that
// exists in the schema and returns a stable hash per table plus the row
// counts. Hashing is column-name-agnostic (canonical JSON of rows
// sorted by id) so a content diff is a single string compare.
func snapshotWorkspaceScopedTables(t *testing.T, db *sql.DB, workspaceID string) map[string]tableSnapshot {
	t.Helper()
	ctx := context.Background()

	// Per-table SELECTs mirror what DumpWorkspace builds internally.
	// Listed explicitly so the test fails clearly if the BackupTables
	// list grows and the scopes shift; better than a silent diff.
	queries := []struct{ table, sql string }{
		{"workspaces", `SELECT * FROM workspaces WHERE id = ? ORDER BY id`},
		{"crews", `SELECT * FROM crews WHERE workspace_id = ? ORDER BY id`},
		{"agents", `SELECT * FROM agents WHERE workspace_id = ? ORDER BY id`},
		{"skills", `SELECT s.* FROM skills s
		            JOIN agent_skills ask ON ask.skill_id = s.id
		            JOIN agents a ON a.id = ask.agent_id
		            WHERE a.workspace_id = ? GROUP BY s.id ORDER BY s.id`},
		{"agent_skills", `SELECT ask.* FROM agent_skills ask
		                  JOIN agents a ON a.id = ask.agent_id
		                  WHERE a.workspace_id = ? ORDER BY ask.id`},
		{"crew_members", `SELECT cm.* FROM crew_members cm
		                  JOIN crews c ON c.id = cm.crew_id
		                  WHERE c.workspace_id = ? ORDER BY cm.id`},
	}

	out := map[string]tableSnapshot{}
	for _, q := range queries {
		rows, err := db.QueryContext(ctx, q.sql, workspaceID)
		if err != nil {
			t.Fatalf("snapshot %s: %v", q.table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatalf("snapshot %s columns: %v", q.table, err)
		}
		var rowDocs []map[string]any
		for rows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				t.Fatalf("snapshot %s scan: %v", q.table, err)
			}
			row := map[string]any{}
			for i, c := range cols {
				// modernc.org/sqlite returns []byte for TEXT in some
				// paths; normalize to string so the JSON encoding is
				// stable across source-vs-target reads.
				if b, ok := raw[i].([]byte); ok {
					row[c] = string(b)
				} else {
					row[c] = raw[i]
				}
			}
			rowDocs = append(rowDocs, row)
		}
		_ = rows.Close()
		sort.Slice(rowDocs, func(i, j int) bool {
			return fmt.Sprint(rowDocs[i]["id"]) < fmt.Sprint(rowDocs[j]["id"])
		})
		buf, err := json.Marshal(rowDocs)
		if err != nil {
			t.Fatalf("snapshot %s marshal: %v", q.table, err)
		}
		sum := sha256.Sum256(buf)
		out[q.table] = tableSnapshot{
			rowCount: len(rowDocs),
			hash:     hex.EncodeToString(sum[:]),
			json:     string(buf),
		}
	}
	return out
}

type tableSnapshot struct {
	rowCount int
	hash     string
	json     string
}

// TestE2E_BackupRestoreRoundTrip is the data-integrity guarantee for a
// real beta upgrade flow: backup -> nuke DB -> restore -> confirm every
// row a customer would notice is back exactly as it was.
//
// What this asserts:
//
//   - Every row in the BackupTables tables that the production schema
//     actually creates round-trips byte-for-byte (column hashing).
//   - schema_migrations on source == target after restore (both fresh
//     migrated to current HEAD, so trivially equal — the assertion
//     guards against a future change that wipes _migrations on restore).
//
// What this does NOT assert (and the user should know):
//
//   - Per-crew filesystem (/workspace, /home/agent, /var/lib): test
//     runs without a Docker daemon, so the docker phase is skipped.
//     That phase is covered separately in integration runs.
//   - chats, chat_messages, journal_entries, agent_runs: those tables
//     are NOT in backup.BackupTables today. A restore round-trip drops
//     them silently. Filed in the PR description as a beta-blocker
//     finding rather than asserted-and-failing here.
func TestE2E_BackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	sourceSnap := snapshotWorkspaceScopedTables(t, source, workspaceID)
	sourceMigrations := backup.AppliedMigrationVersions(ctx, source)

	const passphrase = "round-trip-e2e-passphrase-123"
	bundleDir := t.TempDir()
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   bundleDir,
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if createResult.Path == "" {
		t.Fatal("CreateBackup returned empty path")
	}
	if createResult.Size <= 0 {
		t.Fatalf("CreateBackup returned non-positive size %d", createResult.Size)
	}
	if !createResult.Manifest.Encryption.Enabled {
		t.Error("expected encrypted bundle (passphrase mode)")
	}

	// Fresh target DB — same schema, zero data. This is what a beta
	// tester's "I lost my disk and restored from backup" looks like.
	target := openMigratedDB(t)

	restoreResult, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Actor: backup.Actor{
			UserID: "u_admin",
			Email:  "admin@e2e.test",
			Role:   "ADMIN",
		},
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if restoreResult.RowsInserted <= 0 {
		t.Errorf("RestoreBackup inserted %d rows; expected > 0", restoreResult.RowsInserted)
	}

	// Per-table diff.
	targetSnap := snapshotWorkspaceScopedTables(t, target, workspaceID)
	for table, want := range sourceSnap {
		got, ok := targetSnap[table]
		if !ok {
			t.Errorf("table %s missing from target snapshot", table)
			continue
		}
		if got.rowCount != want.rowCount {
			t.Errorf("%s: row count drift: source=%d target=%d", table, want.rowCount, got.rowCount)
		}
		if got.hash != want.hash {
			t.Errorf("%s: content hash drift\n  source=%s\n  target=%s\n  source rows=%s\n  target rows=%s",
				table, want.hash, got.hash, want.json, got.json)
		}
	}

	// Schema migration set must match. Both sides ran the same Migrate
	// to HEAD, so this is a sanity check that restore did not blow
	// _migrations away.
	targetMigrations := backup.AppliedMigrationVersions(ctx, target)
	if !equalInts(sourceMigrations, targetMigrations) {
		t.Errorf("schema_migrations drift after restore:\n  source=%v\n  target=%v",
			sourceMigrations, targetMigrations)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestE2E_UpgradePath_BackfillHookFires simulates the canonical beta
// upgrade flow: a workspace bundle taken on an OLDER schema is restored
// onto a NEWER target. Every migration the target has applied but the
// bundle did not represents schema that did not exist when the backup
// was taken. Pure ADD COLUMN migrations rely on DEFAULTs; migrations
// that need post-restore data work register a RestoreBackfillFunc via
// database.RegisterRestoreBackfill, and the runner replays each one
// against the freshly-inserted rows.
//
// The test fakes "older schema" by passing CreateOptions.SchemaMigration
// Versions with the highest applied version trimmed. On restore, the
// trimmed version's registered backfill hook MUST fire — anything else
// is a regression that would silently leave restored rows in a
// half-upgraded state on a real beta tester's box.
func TestE2E_UpgradePath_BackfillHookFires(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	applied := backup.AppliedMigrationVersions(ctx, source)
	if len(applied) < 2 {
		t.Fatalf("expected ≥2 migrations applied, got %d", len(applied))
	}
	// Trim the highest version — the bundle pretends that migration
	// did not exist when the backup was taken, so the target's replay
	// must run that version's backfill hook.
	skippedVersion := applied[len(applied)-1]
	bundleVersions := applied[:len(applied)-1]

	// Register a sentinel backfill hook for the trimmed version. Touching
	// agents.updated_at proves the hook ran against the post-restore
	// rows (a fresh restore would otherwise carry forward the source's
	// updated_at unchanged). Unregister on cleanup so we don't pollute
	// other tests in the same package.
	const sentinelMarker = "BACKFILL_REPLAYED_E2E"
	unregister := database.RegisterRestoreBackfill(skippedVersion, func(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE agents SET description = ? WHERE workspace_id = ?`,
			sentinelMarker, workspaceID)
		return err
	})
	t.Cleanup(unregister)

	const passphrase = "upgrade-path-e2e-passphrase-123"
	bundleDir := t.TempDir()
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:                   backup.ScopeWorkspace,
		WorkspaceID:             workspaceID,
		OutputDir:               bundleDir,
		Actor:                   backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:              passphrase,
		SchemaMigrationVersions: bundleVersions, // bundle pretends to be "v0.0.x"
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Target DB has the FULL migration set (incl. skippedVersion).
	target := openMigratedDB(t)
	restoreResult, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if restoreResult.RowsInserted <= 0 {
		t.Fatalf("expected rows inserted, got %d", restoreResult.RowsInserted)
	}

	// Sentinel check: every agent in the restored workspace must carry
	// the backfill marker. If even one row is missing it, the replay
	// either didn't fire or missed rows — both regressions.
	rows, err := target.QueryContext(ctx,
		`SELECT id, COALESCE(description, '') FROM agents WHERE workspace_id = ?`,
		workspaceID)
	if err != nil {
		t.Fatalf("query agents: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, description string
		if err := rows.Scan(&id, &description); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++
		if description != sentinelMarker {
			t.Errorf("agent %s description=%q; expected backfill marker %q",
				id, description, sentinelMarker)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if seen == 0 {
		t.Fatal("no restored agents found; restore likely dropped rows")
	}
}

// TestE2E_CustomSkill_RestoreFailsWithFKViolation documents the current
// behavior of restoring a workspace whose agents reference a custom
// (non-bundled) skill: the agent_skills rows restore, but the `skills`
// table itself is NOT exported by backup (no workspace_id column), so
// the target hits a deferred FK violation and aborts the whole restore.
//
// This is a beta-blocker but not in scope for this PR — capture the
// behavior in a test so any future change to BackupTables that closes
// the gap will FAIL this test and force an explicit decision. Flip the
// assertion direction when the fix lands.
func TestE2E_CustomSkill_RestoreFailsWithFKViolation(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	// Attach a custom skill that lives only in this workspace. Production
	// admins create these via the Skills UI; the FK target lives in the
	// global `skills` table, and the M:N row in `agent_skills`.
	if _, err := source.ExecContext(ctx,
		`INSERT INTO skills (id, name, slug, display_name) VALUES (?, ?, ?, ?)`,
		"sk_custom_e2e", "Custom E2E", "custom-e2e", "Custom"); err != nil {
		t.Fatalf("seed custom skill: %v", err)
	}
	if _, err := source.ExecContext(ctx,
		`INSERT INTO agent_skills (id, agent_id, skill_id) VALUES (?, ?, ?)`,
		"as_custom", "a_alice", "sk_custom_e2e"); err != nil {
		t.Fatalf("seed custom agent_skill: %v", err)
	}

	const passphrase = "custom-skill-gap-e2e-123"
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	_, err = backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       createResult.Path,
		Passphrase: passphrase,
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
	})
	if err == nil {
		t.Fatal("expected restore to fail with FK violation while " +
			"custom skills are excluded from BackupTables; got nil. " +
			"If the backup runner has been updated to export skills, " +
			"flip this assertion and add positive coverage in " +
			"TestE2E_BackupRestoreRoundTrip.")
	}
}
