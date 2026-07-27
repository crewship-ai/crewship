package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// The upgrade this test walks is the one that actually happened on stage: a
// populated v166 database whose audit chain a FOREIGN KEY had already broken
// (#1482). It asserts all three halves of v167 at once, because they only mean
// something together — the schema stops the next break, the repair undoes the
// previous ones, and the rebuild has to carry the whole table across intact
// while doing it.
func TestV167_UpgradeRebuildsAndRepairsDamagedJournal(t *testing.T) {
	// The repair recomputes the KEYED chain hash; with a different key than the
	// one the rows were written under, nothing would verify and the test would
	// pass for the wrong reason (0 restored, 0 asserted).
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))

	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openV167TestDB(t)

	if err := applyMigrationsUpTo(ctx, db, 166, quiet); err != nil {
		t.Fatalf("land schema at v166: %v", err)
	}

	const (
		wsID      = "ws_167"
		crewID    = "crew_167"
		agentID   = "agent_167"
		missionID = "mission_167"
	)
	execV167(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "v167", "v167")
	execV167(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?,?,?,?)`, crewID, wsID, "c", "c")
	execV167(t, db, `INSERT INTO agents (id, workspace_id, name, slug) VALUES (?,?,?,?)`, agentID, wsID, "lead", "lead")
	execV167(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		VALUES (?,?,?,?,?,?,?)`, missionID, wsID, crewID, agentID, "trace_167", "mission", "BACKLOG")

	w := journal.NewWriter(db, quiet, journal.WriterOptions{FlushInterval: time.Hour})
	emit := func(e journal.Entry) string {
		t.Helper()
		id, err := w.Emit(ctx, e)
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		return id
	}
	missionEntry := emit(journal.Entry{
		WorkspaceID: wsID, CrewID: crewID, AgentID: agentID, MissionID: missionID,
		Type: journal.EntryMissionStatus, Severity: journal.SeverityInfo,
		ActorType: journal.ActorUser, ActorID: "user_1",
		Summary: "status_changed: BACKLOG → TODO",
		Payload: map[string]any{"action": "status_changed"},
		Refs:    map[string]any{"mission_id": missionID},
	})
	// A crew-scoped entry with a word only it contains, so the FTS assertion
	// below cannot pass by accident.
	crewEntry := emit(journal.Entry{
		WorkspaceID: wsID, CrewID: crewID, AgentID: agentID,
		Type: journal.EntryRunStarted, Severity: journal.SeverityInfo,
		ActorType: journal.ActorSystem,
		Summary:   "agent started on zarquon deployment",
		Payload:   map[string]any{"k": "v"},
	})
	// An entry that was NEVER written with a mission_id but whose refs mention
	// one. The naive repair (`SET mission_id = json_extract(refs,...)`) would
	// "fix" this healthy row into a permanent break.
	decoyEntry := emit(journal.Entry{
		WorkspaceID: wsID, CrewID: crewID,
		Type: journal.EntryRunCompleted, Severity: journal.SeverityInfo,
		ActorType: journal.ActorSystem,
		Summary:   "unrelated entry that merely mentions a mission",
		Refs:      map[string]any{"mission_id": missionID},
	})
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	// Derived rows whose FK points AT journal_entries. If the rebuild ever runs
	// with foreign-key enforcement left on, DROP TABLE performs an implicit
	// DELETE FROM and CASCADEs these away — silent data loss the row counts
	// below would otherwise never notice.
	execV167(t, db, `INSERT INTO journal_embeddings (entry_id, workspace_id, crew_id, model, dim, vector)
		VALUES (?,?,?,?,?,?)`, missionEntry, wsID, crewID, "test-embed", 2, []byte{1, 2, 3, 4})
	execV167(t, db, `INSERT INTO memory_relations (entry_id, related_entry_id, relation_kind, score)
		VALUES (?,?,?,?)`, missionEntry, crewEntry, "similar", 0.5)

	if res := verifyV167(t, ctx, db, wsID); !res.OK {
		t.Fatalf("precondition: the chain must verify before the mission is deleted (%s)", res.Reason)
	}
	rowsBefore := countRows(t, db, "journal_entries")

	// ── the bug: deleting a mission rewrites the audit log ────────────────────
	execV167(t, db, `DELETE FROM missions WHERE id = ?`, missionID)
	if got := scanString(t, db, `SELECT COALESCE(mission_id,'') FROM journal_entries WHERE id = ?`, missionEntry); got != "" {
		t.Fatalf("precondition: expected the FK action to null mission_id, got %q — has the schema already changed?", got)
	}
	if res := verifyV167(t, ctx, db, wsID); res.OK {
		t.Fatal("precondition: expected the nulled row to break the chain — #1482 no longer reproduces at v166")
	}

	// ── the fix ───────────────────────────────────────────────────────────────
	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("upgrade v166 -> HEAD: %v", err)
	}

	// The schema can no longer rewrite or delete an audit row.
	ddl := scanString(t, db, `SELECT sql FROM sqlite_master WHERE type='table' AND name='journal_entries'`)
	if journalDDLNeedsRebuild(ddl) {
		t.Errorf("journal_entries still declares a destructive FK action:\n%s", ddl)
	}
	if !strings.Contains(ddl, "REFERENCES workspaces(id) ON DELETE CASCADE") {
		t.Error("workspace_id lost its cascade — a deleted workspace must still take its own journal with it")
	}

	// The damage is undone, and the chain verifies again without any rehash.
	if got := scanString(t, db, `SELECT COALESCE(mission_id,'') FROM journal_entries WHERE id = ?`, missionEntry); got != missionID {
		t.Errorf("mission_id = %q after the migration, want %q restored from refs", got, missionID)
	}
	if got := scanString(t, db, `SELECT COALESCE(mission_id,'') FROM journal_entries WHERE id = ?`, decoyEntry); got != "" {
		t.Errorf("decoy row got mission_id = %q — the repair trusted refs instead of the hash, and has just broken a healthy row", got)
	}
	if res := verifyV167(t, ctx, db, wsID); !res.OK {
		t.Errorf("the chain still does not verify after v167 (break at seq %d: %s)", res.BrokenSeq, res.Reason)
	}

	// Nothing was lost in the rebuild.
	if got := countRows(t, db, "journal_entries"); got != rowsBefore {
		t.Errorf("journal_entries: %d rows before the rebuild, %d after", rowsBefore, got)
	}
	if got := countRows(t, db, "journal_embeddings"); got != 1 {
		t.Errorf("journal_embeddings: %d rows after the rebuild, want 1 — DROP TABLE cascaded them away", got)
	}
	if got := countRows(t, db, "memory_relations"); got != 1 {
		t.Errorf("memory_relations: %d rows after the rebuild, want 1 — DROP TABLE cascaded them away", got)
	}

	// Indexes and triggers came back with the table.
	if n := scanInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='journal_entries' AND sql IS NOT NULL`); n < 10 {
		t.Errorf("only %d indexes on journal_entries after the rebuild; the table carries ~12", n)
	}
	for _, trg := range []string{"journal_entries_ai", "journal_entries_ad", "journal_entries_au"} {
		if n := scanInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trg); n != 1 {
			t.Errorf("trigger %s did not survive the rebuild", trg)
		}
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_journal_ws_seq'`); n != 1 {
		t.Error("the UNIQUE(workspace_id, seq) index did not survive the rebuild — two rows could now share a seq")
	}

	// FTS still points at the right rows. The index is keyed on rowid, so a
	// rebuild that reassigned rowids would return a DIFFERENT entry here rather
	// than none — which is why this asserts the id, not just a hit count.
	hit := scanString(t, db, `
		SELECT je.id FROM journal_entries_fts f
		  JOIN journal_entries je ON je.rowid = f.rowid
		 WHERE journal_entries_fts MATCH 'zarquon'`)
	if hit != crewEntry {
		t.Errorf("full-text search for 'zarquon' returned %q, want %q", hit, crewEntry)
	}

	// ── the CASCADE half: deleting a crew must not delete its audit history ───
	execV167(t, db, `DELETE FROM crews WHERE id = ?`, crewID)
	if got := countRows(t, db, "journal_entries"); got != rowsBefore {
		t.Errorf("deleting a crew removed %d audit rows — the journal is still not append-only", rowsBefore-got)
	}
	if res := verifyV167(t, ctx, db, wsID); !res.OK {
		t.Errorf("deleting a crew broke the chain (seq %d: %s)", res.BrokenSeq, res.Reason)
	}

	execV167(t, db, `DELETE FROM agents WHERE id = ?`, agentID)
	if got := countRows(t, db, "journal_entries"); got != rowsBefore {
		t.Errorf("deleting an agent removed %d audit rows", rowsBefore-got)
	}

	// ── and the workspace cascade is still wired, deliberately ────────────────
	execV167(t, db, `DELETE FROM workspaces WHERE id = ?`, wsID)
	if got := countRows(t, db, "journal_entries"); got != 0 {
		t.Errorf("deleting the workspace left %d journal rows behind; workspace_id must keep ON DELETE CASCADE", got)
	}
}

// A fnNoTx migration's _migrations row is written after it returns, so a crash
// in between re-runs it against an already-migrated schema. That makes
// idempotency a correctness requirement, not a nicety.
func TestV167_IsIdempotent(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openV167TestDB(t)

	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const wsID = "ws_167_idem"
	execV167(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "idem", "idem")
	w := journal.NewWriter(db, quiet, journal.WriterOptions{FlushInterval: time.Hour})
	if _, err := w.Emit(ctx, journal.Entry{
		WorkspaceID: wsID, Type: journal.EntryRunStarted, Severity: journal.SeverityInfo,
		ActorType: journal.ActorSystem, Summary: "entry",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_ = w.Close()

	before := countRows(t, db, "journal_entries")
	for i := 0; i < 2; i++ {
		if err := migrationJournalAppendOnlyFKs(ctx, db, quiet); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	if got := countRows(t, db, "journal_entries"); got != before {
		t.Errorf("re-running the migration changed the row count: %d -> %d", before, got)
	}
	if res := verifyV167(t, ctx, db, wsID); !res.OK {
		t.Errorf("re-running the migration broke the chain (%s)", res.Reason)
	}
	if n := scanInt(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name=?`, journalRebuildTable); n != 0 {
		t.Error("the staging table was left behind")
	}
}

// A restore re-inserts journal rows straight out of a bundle. If that bundle
// was taken from a damaged pre-v167 instance it carries the nulled mission_ids
// with it, and the migration — already recorded as applied — will never run
// again. The registered restore-backfill hook is the only thing standing
// between that and an instance that verifies green, then red.
func TestV167_RestoreBackfillRepairsRestoredRows(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := openV167TestDB(t)

	if err := Migrate(ctx, db, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const wsID, missionID = "ws_167_restore", "mission_restore"
	execV167(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "restore", "restore")

	w := journal.NewWriter(db, quiet, journal.WriterOptions{FlushInterval: time.Hour})
	entryID, err := w.Emit(ctx, journal.Entry{
		WorkspaceID: wsID, MissionID: missionID,
		Type: journal.EntryMissionStatus, Severity: journal.SeverityInfo,
		ActorType: journal.ActorUser, Summary: "status_changed",
		Refs: map[string]any{"mission_id": missionID},
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	_ = w.Close()

	// What the bundle carries: the column already nulled by the old schema.
	execV167(t, db, `UPDATE journal_entries SET mission_id = NULL WHERE id = ?`, entryID)
	if res := verifyV167(t, ctx, db, wsID); res.OK {
		t.Fatal("precondition: a nulled mission_id must break the chain")
	}

	hook := RestoreBackfillFor(167)
	if hook == nil {
		t.Fatal("v167 registers no restore backfill — a pre-v167 bundle would restore the damage unrepaired")
	}
	for i := 0; i < 2; i++ { // the contract is that hooks are re-runnable
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := hook(ctx, tx, quiet); err != nil {
			_ = tx.Rollback()
			t.Fatalf("restore backfill run %d: %v", i, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	if res := verifyV167(t, ctx, db, wsID); !res.OK {
		t.Errorf("the chain does not verify after the restore backfill (%s)", res.Reason)
	}
}

// rewriteJournalDDL is the one place a rebuild could silently lose a column, so
// it is tested directly rather than only through a live database.
func TestV167_RewriteJournalDDL(t *testing.T) {
	const live = `CREATE TABLE IF NOT EXISTS journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE,
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL,
    severity TEXT NOT NULL DEFAULT 'info' CHECK(severity IN ('info','notice','warn','error'))
, priority TEXT NOT NULL DEFAULT 'normal', priority_at_emit TEXT)`

	got, err := rewriteJournalDDL(live)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if journalDDLNeedsRebuild(got) {
		t.Errorf("rewritten DDL still carries a destructive FK action:\n%s", got)
	}
	if !strings.Contains(got, "REFERENCES workspaces(id) ON DELETE CASCADE") {
		t.Error("rewrite dropped the workspace cascade, which is deliberately kept")
	}
	// Columns added by later migrations, and the CHECK constraint, must survive.
	for _, keep := range []string{"crew_id TEXT", "agent_id TEXT", "mission_id TEXT",
		"priority TEXT NOT NULL DEFAULT 'normal'", "priority_at_emit TEXT",
		"CHECK(severity IN ('info','notice','warn','error'))"} {
		if !strings.Contains(got, keep) {
			t.Errorf("rewritten DDL lost %q", keep)
		}
	}
	if !strings.HasPrefix(got, "CREATE TABLE "+journalRebuildTable+" ") {
		t.Errorf("rewritten DDL targets the wrong table: %.60q", got)
	}
	// IF NOT EXISTS must be gone: a staging table left by a crashed run would
	// otherwise be silently reused and copied into with the wrong shape.
	if strings.Contains(got, "IF NOT EXISTS") {
		t.Error("rewritten DDL kept IF NOT EXISTS")
	}

	if _, err := rewriteJournalDDL("CREATE TABLE journal_entries"); err == nil {
		t.Error("a definition with no column list must be rejected, not rebuilt")
	}
}

// The clause matcher doubles as the migration's "already done?" probe, so a
// definition it fails to recognise is not a cosmetic miss: the rebuild would be
// skipped and the migration would record SUCCESS with the destructive
// constraint still in place. SQLite rewrites this stored text on
// ALTER TABLE DROP COLUMN (v121 does exactly that to journal_entries), so the
// exact spacing is not something to bet the audit log on.
func TestV167_ClauseMatchingToleratesFormatting(t *testing.T) {
	spaced := `CREATE TABLE journal_entries (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT REFERENCES  crews( id )   ON DELETE   CASCADE,
    agent_id TEXT
        REFERENCES "agents" ("id")
        ON DELETE CASCADE,
    mission_id TEXT references missions(id) on delete set  null,
    ts TEXT NOT NULL)`

	if !journalDDLNeedsRebuild(spaced) {
		t.Fatal("a differently-formatted definition read as already-rebuilt — the migration would silently no-op on a still-destructive schema")
	}
	got, err := rewriteJournalDDL(spaced)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if journalDDLNeedsRebuild(got) {
		t.Errorf("rewritten DDL still carries a destructive FK action:\n%s", got)
	}
	for _, keep := range []string{"crew_id TEXT", "agent_id TEXT", "mission_id TEXT", "ts TEXT NOT NULL"} {
		if !strings.Contains(got, keep) {
			t.Errorf("rewritten DDL lost %q", keep)
		}
	}

	// The workspace cascade must never look like a match.
	if journalDDLNeedsRebuild(`CREATE TABLE journal_entries (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE, ts TEXT)`) {
		t.Error("the deliberately-kept workspace cascade was matched as destructive")
	}

	// Fail loud, not quiet: a definition that is not the one this migration was
	// written against must be rejected rather than half-rebuilt.
	if _, err := rewriteJournalDDL(`CREATE TABLE journal_entries (
    crew_id TEXT REFERENCES crews(id) ON DELETE CASCADE, ts TEXT)`); err == nil {
		t.Error("a definition with only one destructive clause must be rejected")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// openV167TestDB opens a file-backed database with foreign keys ON. Both
// matter: FK actions are what #1482 is about and SQLite only performs them when
// the pragma is on, and an in-memory database would hand each pooled connection
// its own empty schema.
func openV167TestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/v167.db?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	return db
}

func execV167(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %.70s: %v", q, err)
	}
}

func verifyV167(t *testing.T, ctx context.Context, db *sql.DB, wsID string) *journal.VerifyResult {
	t.Helper()
	res, err := journal.VerifyChain(ctx, db, wsID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return res
}

func scanString(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(q, args...).Scan(&s); err != nil {
		if err == sql.ErrNoRows {
			return ""
		}
		t.Fatalf("query %.70s: %v", q, err)
	}
	return s
}

func scanInt(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("query %.70s: %v", q, err)
	}
	return n
}
