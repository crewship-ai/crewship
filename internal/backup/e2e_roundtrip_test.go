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
	"strings"
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
	// Custom skill (source='CUSTOM' by default) — exercises the new
	// transitive skills filter so the round-trip can prove user-created
	// skills survive a restore. Bundled skills (skill_coding_01) are
	// already in both source and target from SeedBundledSkills; the
	// bundle's INSERT OR IGNORE will no-op against the pre-seeded
	// target row, which is the correct production behavior.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO skills (id, name, slug, display_name, author_id) VALUES (?, ?, ?, ?, ?)`,
		"sk_custom_e2e", "Custom E2E", "custom-e2e", "Custom E2E", "u_admin"); err != nil {
		t.Fatalf("seed custom skill: %v", err)
	}

	bindings := []struct{ id, agentID, skillID string }{
		{"as_1", "a_alice", "skill_coding_01"},
		{"as_2", "a_alice", "skill_research_01"},
		{"as_3", "a_bob", "skill_coding_01"},
		{"as_4", "a_carol", "sk_custom_e2e"}, // custom — the round-trip critical path
	}
	for _, b := range bindings {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO agent_skills (id, agent_id, skill_id) VALUES (?, ?, ?)`,
			b.id, b.agentID, b.skillID); err != nil {
			t.Fatalf("seed agent_skill %s: %v", b.id, err)
		}
	}

	// crew_members — now safely round-trippable because `users` is in
	// BackupTables. Previously this seed caused a deferred FK violation
	// on every restore; the gap test that pinned that behavior is gone.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO crew_members (id, crew_id, user_id) VALUES (?, ?, ?)`,
		"cm_alpha_admin", "c_alpha", "u_admin"); err != nil {
		t.Fatalf("seed crew_member: %v", err)
	}

	// chats — exercises the renamed entry (was `agent_chats` in the
	// pre-fix BackupTables, which silently dropped this data) plus the
	// chats.created_by → users FK now satisfied by the users carry.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, status)
		 VALUES (?, ?, ?, ?, ?, 'ACTIVE')`,
		"ch_alice_1", "a_alice", workspaceID, "u_admin", "First chat"); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// journal_entries — the crew journal is documented as "canonical
	// source of truth for every observable action in the platform" and
	// was excluded from BackupTables pre-fix. Two entries cover the
	// most common shapes (agent action, user action).
	journal := []struct{ id, entryType, actorType, summary string }{
		{"j_1", "agent.run.start", "agent", "Alice started a run"},
		{"j_2", "user.note", "user", "Operator left a note"},
	}
	for _, j := range journal {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, entry_type, actor_type, summary)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			j.id, workspaceID, "c_alpha", "a_alice", j.entryType, j.actorType, j.summary); err != nil {
			t.Fatalf("seed journal_entry %s: %v", j.id, err)
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
	//
	// BUNDLED skills (source='BUNDLED') are intentionally EXCLUDED from
	// the skills snapshot: SeedBundledSkills runs on both source and
	// target as part of openMigratedDB, so their created_at/updated_at
	// `datetime('now')` defaults are computed at independent insert
	// times and drift by a second under any non-trivial test runtime.
	// The bundle's INSERT OR IGNORE leaves the target's local row
	// untouched, which is the correct production behavior. Custom
	// skills (source='CUSTOM') DO round-trip via the bundle and are
	// compared exactly.
	queries := []struct{ table, sql string }{
		{"workspaces", `SELECT * FROM workspaces WHERE id = ? ORDER BY id`},
		{"crews", `SELECT * FROM crews WHERE workspace_id = ? ORDER BY id`},
		{"agents", `SELECT * FROM agents WHERE workspace_id = ? ORDER BY id`},
		// Mirrors workspaceFilterSQL's users branch verbatim — including
		// the skills.author_id UNION leg. CodeRabbit caught the earlier
		// two-branch version: a custom skill whose author was NOT also
		// in crew_members or chats.created_by would land in the bundle
		// (the production filter carries it) but be invisible to this
		// snapshot, masking a real row-count drift.
		{"users", `SELECT u.* FROM users u WHERE u.id IN (
			SELECT user_id FROM crew_members WHERE crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)
			UNION SELECT created_by FROM chats WHERE workspace_id = ? AND created_by IS NOT NULL
			UNION SELECT s.author_id FROM skills s
			  JOIN agent_skills ask ON ask.skill_id = s.id
			  JOIN agents a ON a.id = ask.agent_id
			  JOIN crews c ON c.id = a.crew_id
			  WHERE c.workspace_id = ? AND s.author_id IS NOT NULL
		) ORDER BY u.id`},
		{"skills", `SELECT s.* FROM skills s
		            JOIN agent_skills ask ON ask.skill_id = s.id
		            JOIN agents a ON a.id = ask.agent_id
		            WHERE a.workspace_id = ? AND s.source != 'BUNDLED'
		            GROUP BY s.id ORDER BY s.id`},
		{"agent_skills", `SELECT ask.* FROM agent_skills ask
		                  JOIN agents a ON a.id = ask.agent_id
		                  WHERE a.workspace_id = ? ORDER BY ask.id`},
		{"crew_members", `SELECT cm.* FROM crew_members cm
		                  JOIN crews c ON c.id = cm.crew_id
		                  WHERE c.workspace_id = ? ORDER BY cm.id`},
		{"chats", `SELECT * FROM chats WHERE workspace_id = ? ORDER BY id`},
		{"journal_entries", `SELECT * FROM journal_entries WHERE workspace_id = ? ORDER BY id`},
	}

	out := map[string]tableSnapshot{}
	for _, q := range queries {
		// Some scoping clauses (e.g. the users UNION) reference the
		// workspace id more than once. Pass workspaceID as many times
		// as there are positional placeholders in the SQL so callers
		// can write the most natural form per table.
		nArgs := strings.Count(q.sql, "?")
		args := make([]any, nArgs)
		for i := range args {
			args[i] = workspaceID
		}
		rows, err := db.QueryContext(ctx, q.sql, args...)
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
		// CodeRabbit catch: a partial row iteration (e.g. driver
		// disconnect) returns from rows.Next() as false, but only
		// rows.Err() distinguishes "clean EOF" from "iteration error
		// silently truncated my dataset" — which would corrupt the
		// snapshot hash and produce a misleading round-trip failure.
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatalf("snapshot %s iterate: %v", q.table, err)
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
	// The manifest must record the EXACT set of migrations the source
	// had applied at backup time — that's what the restore-side schema
	// skew check and the backfill replay both depend on. A regression
	// that stamps a wrong/empty list silently neuters both safeguards.
	if !equalInts(createResult.Manifest.SchemaMigrationVersions, sourceMigrations) {
		t.Errorf("manifest SchemaMigrationVersions drift:\n  source applied=%v\n  manifest stamped=%v",
			sourceMigrations, createResult.Manifest.SchemaMigrationVersions)
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
	// seedWorkspace creates exactly 2 crews; assert the restorer
	// reports the same. A drift here means either the manifest's
	// Contents.Crews didn't capture both or the count is being computed
	// from a different source.
	if restoreResult.CrewsCount != 2 {
		t.Errorf("RestoreResult.CrewsCount=%d; expected 2 seeded crews", restoreResult.CrewsCount)
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

	// Anti-vacuity guard: a future seed change (or a stray production
	// migration) that pre-populates `description` would make the
	// post-restore assertion succeed without the backfill ever firing.
	// Pin the source's pre-backup state so the test can only pass when
	// the hook actually did the work.
	var prePop int
	if err := source.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND description = ?`,
		workspaceID, sentinelMarker).Scan(&prePop); err != nil {
		t.Fatalf("pre-backup sentinel scan: %v", err)
	}
	if prePop != 0 {
		t.Fatalf("seed contamination: %d source agents already carry the sentinel; "+
			"the post-restore assertion would be vacuous", prePop)
	}

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

// The two gap-pinning tests (CustomSkill, CrewMembers) that used to
// live here were inverted into positive coverage on the round-trip
// once BackupTables started exporting `users` and `skills`. Asserting
// failure when the underlying data path now succeeds would be a
// permanent false alarm; the round-trip's per-table diff already
// proves both classes round-trip cleanly.
