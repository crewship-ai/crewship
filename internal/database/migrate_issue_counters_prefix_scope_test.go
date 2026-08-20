package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// migrate_issue_counters_prefix_scope_test.go — #1797.
//
// issue_counters was keyed by crew_id, so every crew counted privately from 1.
// The namespace those numbers feed is per WORKSPACE: an identifier is
// "<prefix>-<n>" and missions carries UNIQUE(workspace_id, identifier) (#1733).
// Two crews in one workspace whose effective prefix collides — `engineering`
// and `engine` both derive ENG with no issue_prefix set on either — each minted
// ENG-1, and the loser's insert was rejected by that index. Because the counter
// upsert shared the caller's transaction with the mission insert, the rejection
// rolled the increment back too, so the crew retried the same identifier
// forever. The counter now lives on (workspace_id, prefix).

// prefixScopeVersion is the version of the re-key migration, or 0 if it is not
// in the registry.
func prefixScopeVersion(t *testing.T) int {
	t.Helper()
	m, ok := migrationByName("issue_counters_prefix_scope")
	if !ok {
		t.Fatal("no migration named `issue_counters_prefix_scope` in the registry")
	}
	return m.version
}

// openPrefixScopeDBBeforeRekey migrates to the version just below the re-key,
// so a fixture can seed the OLD per-crew issue_counters shape and then let
// Migrate carry it across.
func openPrefixScopeDBBeforeRekey(t *testing.T) (*sql.DB, context.Context, *slog.Logger) {
	t.Helper()
	db, ctx, logger := openIssueCountersDB(t)
	if err := applyMigrationsUpTo(ctx, db, prefixScopeVersion(t)-1, logger); err != nil {
		t.Fatalf("migrate to the version before the re-key: %v", err)
	}
	if present, _ := issueCountersColumnIsNotNull(t, db, ctx, "crew_id"); !present {
		t.Fatal("the pre-migration schema has no crew_id column — this fixture no longer " +
			"reproduces the upgrade the migration exists for")
	}
	return db, ctx, logger
}

func prefixScopeExec(t *testing.T, db *sql.DB, ctx context.Context, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("seed %q: %v", query, err)
	}
}

// seedPrefixScopeCrew inserts a crew and its LEAD agent, so missions written
// against it satisfy their foreign keys.
func seedPrefixScopeCrew(t *testing.T, db *sql.DB, ctx context.Context, wsID, crewID, slug, issuePrefix string) {
	t.Helper()
	prefix := sql.NullString{String: issuePrefix, Valid: issuePrefix != ""}
	prefixScopeExec(t, db, ctx,
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, ?, ?, ?)`,
		crewID, wsID, strings.ToUpper(slug), slug, prefix)
	prefixScopeExec(t, db, ctx,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		 VALUES (?, ?, ?, 'Lead', ?, 'LEAD')`,
		crewID+"_lead", crewID, wsID, slug+"-lead")
}

func seedPrefixScopeMission(t *testing.T, db *sql.DB, ctx context.Context, wsID, crewID, identifier string, number int) {
	t.Helper()
	prefixScopeExec(t, db, ctx, `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		                      status, mission_type, identifier, number, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Ship it', 'BACKLOG', 'issue', ?, ?, datetime('now'), datetime('now'))`,
		"msn_"+identifier, wsID, crewID, crewID+"_lead", "trace-"+identifier, identifier, number)
}

func prefixScopeCounter(t *testing.T, db *sql.DB, ctx context.Context, wsID, prefix string) (int, bool) {
	t.Helper()
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT next_number FROM issue_counters WHERE workspace_id = ? AND prefix = ?`,
		wsID, prefix).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read counter (%s, %s): %v", wsID, prefix, err)
	}
	return n, true
}

// TestIssueCountersPrefixScope_KeyShape reads the key back off the table's own
// primary-key index rather than matching text against the CREATE statement. The
// sibling assertion in migrate_mission_identifier_workspace_scope_test.go
// records why: a `strings.Contains` over the DDL passes for a table that merely
// MENTIONS the column, and that mutation survived the first version of it. The
// PRAGMA form did not.
func TestIssueCountersPrefixScope_KeyShape(t *testing.T) {
	t.Parallel()
	db, ctx, logger := openIssueCountersDB(t)
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The composite PRIMARY KEY of a rowid table is implemented as an
	// auto-index with origin 'pk'; that index IS the key.
	var pkIndex string
	rows, err := db.QueryContext(ctx, `PRAGMA index_list(issue_counters)`)
	if err != nil {
		t.Fatalf("index_list: %v", err)
	}
	for rows.Next() {
		var (
			seq, unique, partial int
			name, origin         string
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			t.Fatalf("scan index_list: %v", err)
		}
		if origin == "pk" {
			if unique != 1 {
				t.Errorf("the primary-key index %s is not UNIQUE", name)
			}
			pkIndex = name
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("index_list rows: %v", err)
	}
	_ = rows.Close()
	if pkIndex == "" {
		t.Fatal("issue_counters has no primary-key index — the counter is unkeyed, so nothing " +
			"stops two rows from counting the same prefix independently")
	}

	var keys []string
	info, err := db.QueryContext(ctx, `PRAGMA index_info(`+pkIndex+`)`)
	if err != nil {
		t.Fatalf("index_info: %v", err)
	}
	for info.Next() {
		var seqno, cid int
		var name sql.NullString
		if err := info.Scan(&seqno, &cid, &name); err != nil {
			_ = info.Close()
			t.Fatalf("scan index_info: %v", err)
		}
		keys = append(keys, name.String)
	}
	if err := info.Err(); err != nil {
		_ = info.Close()
		t.Fatalf("index_info rows: %v", err)
	}
	_ = info.Close()

	want := []string{"workspace_id", "prefix"}
	if len(keys) != len(want) {
		t.Fatalf("primary key = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("primary key column %d = %q, want %q (key: %v)", i, keys[i], want[i], keys)
		}
	}
}

// TestIssueCountersPrefixScope_KeyIsEnforced is the behavioural half: the shape
// above is only worth having if the database refuses a second row for the same
// prefix in the same workspace, and still allows the same prefix in another.
func TestIssueCountersPrefixScope_KeyIsEnforced(t *testing.T) {
	t.Parallel()
	db, ctx, logger := openIssueCountersDB(t)
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prefixScopeExec(t, db, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_pk_a','A','ws-pk-a')`)
	prefixScopeExec(t, db, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_pk_b','B','ws-pk-b')`)

	prefixScopeExec(t, db, ctx,
		`INSERT INTO issue_counters (workspace_id, prefix, next_number) VALUES ('ws_pk_a','ENG',3)`)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO issue_counters (workspace_id, prefix, next_number) VALUES ('ws_pk_a','ENG',1)`); err == nil {
		t.Error("a second ENG counter in the same workspace was accepted — two crews sharing a " +
			"prefix would count independently again, which is the whole bug")
	} else if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
		t.Errorf("second ENG counter rejected for the wrong reason: %v", err)
	}
	// A different workspace is a different namespace (#1733) and must keep its
	// own ENG sequence.
	prefixScopeExec(t, db, ctx,
		`INSERT INTO issue_counters (workspace_id, prefix, next_number) VALUES ('ws_pk_b','ENG',1)`)
}

// TestIssueCountersPrefixScope_CollapsesCollidingCrews is the upgrade: the two
// crews that were fighting over ENG arrive as two rows and must leave as one,
// carrying the HIGHER number. Taking the lower — or the sum, or whichever the
// GROUP BY happened to pick — re-issues identifiers that already exist.
func TestIssueCountersPrefixScope_CollapsesCollidingCrews(t *testing.T) {
	t.Parallel()
	db, ctx, logger := openPrefixScopeDBBeforeRekey(t)

	prefixScopeExec(t, db, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_cc','CC','ws-cc')`)
	prefixScopeExec(t, db, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_other','Other','ws-other')`)
	// No issue_prefix on either: both derive ENG from the slug, which is how
	// this collision arrives without anybody configuring anything.
	seedPrefixScopeCrew(t, db, ctx, "ws_cc", "crew_engineering", "engineering", "")
	seedPrefixScopeCrew(t, db, ctx, "ws_cc", "crew_engine", "engine", "")
	// A third crew in ANOTHER workspace with the same prefix — its sequence is a
	// different namespace and must not be folded in.
	seedPrefixScopeCrew(t, db, ctx, "ws_other", "crew_elsewhere", "engineering", "")

	prefixScopeExec(t, db, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_engineering', 9)`)
	// The wedged crew: it never got past its first create, because ENG-1 was
	// taken and the failed insert rolled its own increment back every time.
	prefixScopeExec(t, db, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_engine', 1)`)
	prefixScopeExec(t, db, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_elsewhere', 4)`)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	n, ok := prefixScopeCounter(t, db, ctx, "ws_cc", "ENG")
	if !ok {
		t.Fatal("the collapsed ENG counter is missing — every crew in this workspace now restarts at 1 " +
			"on top of the identifiers it already minted")
	}
	if n != 9 {
		t.Errorf("collapsed ENG counter = %d, want 9 (MAX of 9 and 1) — anything lower re-issues "+
			"identifiers that exist, and a SUM would silently skip a block of numbers", n)
	}
	if n, ok := prefixScopeCounter(t, db, ctx, "ws_other", "ENG"); !ok || n != 4 {
		t.Errorf("the other workspace's ENG counter = %d (present=%v), want 4 — identifiers are a "+
			"per-workspace namespace and the collapse must not cross workspaces", n, ok)
	}
	var rowsTotal int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_counters`).Scan(&rowsTotal); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if rowsTotal != 2 {
		t.Errorf("%d counter rows after the collapse, want 2 (one per workspace)", rowsTotal)
	}
}

// TestIssueCountersPrefixScope_PrefixChangeDoesNotRestartNumbering is the
// residual hazard the re-key introduces if the backfill is naive. issue_prefix
// is mutable: a crew that changed it leaves identifiers behind under a prefix no
// crew derives any more, and no per-crew counter row names it either. Backfill
// only from the counters and that prefix gets no row at all — so the next crew
// to derive it starts at 1, straight into identifiers that already exist. The
// migration therefore also folds in the high-water mark of what was minted.
func TestIssueCountersPrefixScope_PrefixChangeDoesNotRestartNumbering(t *testing.T) {
	t.Parallel()
	db, ctx, logger := openPrefixScopeDBBeforeRekey(t)

	prefixScopeExec(t, db, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_pc','PC','ws-pc')`)
	// The crew minted ENG-1..ENG-7 and was LATER re-prefixed to PLT. Its
	// effective prefix today is PLT; nothing in crews or issue_counters
	// mentions ENG any more.
	seedPrefixScopeCrew(t, db, ctx, "ws_pc", "crew_pc", "engineering", "PLT")
	for i := 1; i <= 7; i++ {
		seedPrefixScopeMission(t, db, ctx, "ws_pc", "crew_pc", "ENG-"+strconv.Itoa(i), i)
	}
	prefixScopeExec(t, db, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_pc', 7)`)
	// An identifier that is not "<prefix>-<number>" for its own number must not
	// be mined for a prefix: this one's number is 7 but its text ends in -12.
	seedPrefixScopeMission(t, db, ctx, "ws_pc", "crew_pc", "WEIRD-12", 7)

	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	n, ok := prefixScopeCounter(t, db, ctx, "ws_pc", "ENG")
	if !ok {
		t.Fatal("no ENG counter after the migration — ENG-1..ENG-7 exist in this workspace, so the " +
			"next crew to derive ENG will mint ENG-1 and be rejected by " +
			"idx_mission_workspace_identifier: the same wedge through a different door")
	}
	if n != 7 {
		t.Errorf("ENG counter = %d, want 7 (the highest ENG identifier already minted)", n)
	}
	// The crew's own counter moved to its CURRENT prefix, so it keeps counting
	// where it left off rather than restarting under the new prefix.
	if n, ok := prefixScopeCounter(t, db, ctx, "ws_pc", "PLT"); !ok || n != 7 {
		t.Errorf("PLT counter = %d (present=%v), want 7 — the crew's live counter must follow it "+
			"to the prefix it uses now", n, ok)
	}
	var rowsTotal int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_counters`).Scan(&rowsTotal); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if rowsTotal != 2 {
		var got []string
		rows, err := db.QueryContext(ctx, `SELECT prefix FROM issue_counters ORDER BY prefix`)
		if err != nil {
			t.Fatalf("list counters: %v", err)
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				_ = rows.Close()
				t.Fatalf("scan prefix: %v", err)
			}
			got = append(got, p)
		}
		_ = rows.Close()
		t.Errorf("counter prefixes = %v, want exactly [ENG PLT] — WEIRD-12 carries number 7, so "+
			"a backfill that just chops the last token off an identifier invents a prefix from it; "+
			"the reconstruction test in the migration is what keeps identifiers that are not "+
			"<prefix>-<number> out", got)
	}
}

// TestIssueCountersPrefixScope_SurvivesOrphanedWorkspaceRows is the boot-safety
// half. The re-key copies into a table whose workspace_id is NOT NULL and
// carries its own FK, on a connection with foreign_keys ON — so a crew or a
// mission whose workspace is already gone fails the copy with
// SQLITE_CONSTRAINT_FOREIGNKEY (787) and takes startup down, naming neither the
// table nor the row. Nobody can log in to repair it.
//
// Such rows should not exist — ON DELETE CASCADE has been declared on both
// parents for as long as the columns have — but "should not exist" is not
// "cannot", and a row that outlived its workspace under `PRAGMA
// foreign_keys=OFF` is the same class
// 20260820074400_issue_counters_crew_not_null guards with an explicit EXISTS.
// This migration now guards it the same way, which is what its header claims.
func TestIssueCountersPrefixScope_SurvivesOrphanedWorkspaceRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "orphan.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// Phase 1: the pre-rekey schema on a handle with foreign_keys OFF, which is
	// the only way to manufacture the orphan at all.
	loose, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(OFF)")
	if err != nil {
		t.Fatalf("open loose handle: %v", err)
	}
	version := prefixScopeVersion(t)
	if err := applyMigrationsUpTo(ctx, loose, version-1, logger); err != nil {
		t.Fatalf("migrate to the version before the re-key: %v", err)
	}
	prefixScopeExec(t, loose, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_live','Live','ws-live')`)
	prefixScopeExec(t, loose, ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_gone','Gone','ws-gone')`)
	seedPrefixScopeCrew(t, loose, ctx, "ws_live", "crew_live", "engineering", "")
	seedPrefixScopeCrew(t, loose, ctx, "ws_gone", "crew_orphan", "design", "")
	prefixScopeExec(t, loose, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_live', 12)`)
	prefixScopeExec(t, loose, ctx, `INSERT INTO issue_counters (crew_id, next_number) VALUES ('crew_orphan', 5)`)
	// The mission arm of the UNION ALL needs an orphan too: it reads
	// missions.workspace_id directly, not through crews.
	seedPrefixScopeMission(t, loose, ctx, "ws_live", "crew_live", "ENG-12", 12)
	seedPrefixScopeMission(t, loose, ctx, "ws_gone", "crew_orphan", "DES-5", 5)
	// No cascade fires: this handle has foreign_keys OFF.
	prefixScopeExec(t, loose, ctx, `DELETE FROM workspaces WHERE id = 'ws_gone'`)
	if err := loose.Close(); err != nil {
		t.Fatalf("close loose handle: %v", err)
	}

	// Phase 2: boot the way the server boots — foreign_keys ON.
	db, err := Open("file:" + path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("the re-key refused to boot over rows whose workspace is gone: %v\n"+
			"they must be dropped by the copy, not handed to the FK checker — the failure "+
			"is a boot failure with no way in to repair it", err)
	}

	if n, ok := prefixScopeCounter(t, db.DB, ctx, "ws_live", "ENG"); !ok || n != 12 {
		t.Errorf("the live counter = %d (present=%v), want 12 — the guard dropped more than "+
			"the orphans", n, ok)
	}
	var orphans int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM issue_counters WHERE workspace_id = 'ws_gone'`).Scan(&orphans); err != nil {
		t.Fatalf("count orphan counters: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d counter row(s) for a workspace that no longer exists survived the re-key", orphans)
	}
}
