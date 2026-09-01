package backup

// A bundle column the target schema does not have is dropped from the INSERT
// and, until #2034, was dropped in silence. These tests pin the reporting, not
// a change of behaviour: the row still lands (or does not) exactly as before,
// but the restore now says what it left behind.
//
// The scenario driving them is the real one from #2034. #1797 re-keyed
// issue_counters from `crew_id` to `(workspace_id, prefix)`, so a bundle taken
// before that migration carries `{crew_id, next_number}` into a table that has
// neither. `crew_id` is dropped, the statement degenerates to
//
//	INSERT OR IGNORE INTO issue_counters (next_number) VALUES (?)
//
// and workspace_id / prefix are NOT NULL with no default — a constraint
// violation that OR IGNORE swallows. Nothing errored, nothing was inserted,
// and nothing said so.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newDroppedColumnTargetDB is a target instance carrying the POST-#1797
// issue_counters shape, plus the two parent tables a bundle row needs to
// satisfy its foreign keys. Deliberately production-shaped on the columns
// that matter: workspace_id and prefix are NOT NULL with no default, which
// is what turns a dropped column into a swallowed constraint violation.
func newDroppedColumnTargetDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/target.db?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY, name TEXT, slug TEXT
		);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id),
			name TEXT, slug TEXT, issue_prefix TEXT
		);
		CREATE TABLE issue_counters (
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			prefix TEXT NOT NULL,
			next_number INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (workspace_id, prefix)
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// preRekeyDump is a bundle as a pre-#1797 instance would have written it.
func preRekeyDump() *DBDump {
	return &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			"crews": {{
				"id": "c_1", "workspace_id": "ws_1",
				"name": "Engineering", "slug": "engineering", "issue_prefix": "ENG",
			}},
			// The old shape: keyed on the crew, with no idea of a workspace
			// or a prefix.
			"issue_counters": {{"crew_id": "c_1", "next_number": int64(42)}},
		},
	}
}

// TestRestoreDump_PreRekeyIssueCountersMigrates is #2034's narrow fix, at the
// RestoreDump level. Earlier this asserted the drop was merely REPORTED and
// the row still lost — that was the state after #2108 landed the general
// "counted, reported skip" half of #2034 but not the issue_counters-specific
// transform. This is the transform: migrateIssueCounterRows resolves c_1's
// workspace and effective prefix from the crews row the SAME bundle carries
// (crews restores before issue_counters — BackupTables order) and the row
// lands under the new key instead of being dropped.
func TestRestoreDump_PreRekeyIssueCountersMigrates(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	stats, err := RestoreDumpTx(context.Background(), db, preRekeyDump(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The fix: the counter landed under its new key.
	var workspaceID, prefix string
	var next int64
	err = db.QueryRow(`SELECT workspace_id, prefix, next_number FROM issue_counters`).
		Scan(&workspaceID, &prefix, &next)
	if err != nil {
		t.Fatalf("issue_counters after restore: %v", err)
	}
	if workspaceID != "ws_1" || prefix != "ENG" || next != 42 {
		t.Errorf("issue_counters row = (%s, %s, %d), want (ws_1, ENG, 42)", workspaceID, prefix, next)
	}

	// crew_id is no longer a dropped column: it was translated, not thrown
	// away.
	if stats.ColumnsDropped != 0 {
		t.Errorf("ColumnsDropped = %d, want 0 — crew_id was migrated, not dropped: %+v", stats.ColumnsDropped, stats.DroppedColumns)
	}
	if stats.IssueCountersMigrated != 1 {
		t.Errorf("IssueCountersMigrated = %d, want 1", stats.IssueCountersMigrated)
	}
	if stats.RowsInserted != 3 { // workspaces + crews + the migrated counter
		t.Errorf("RowsInserted = %d, want 3", stats.RowsInserted)
	}
}

// TestRestoreDump_PreRekeyIssueCounterUnresolvedCrewIsDropped covers the
// other half: a crew_id the target cannot resolve at all (not in this
// bundle, not already on the target) is not something this transform can
// honestly place. It must fall through to the ordinary column whitelist —
// dropped, and counted — rather than inventing a workspace for it.
func TestRestoreDump_PreRekeyIssueCounterUnresolvedCrewIsDropped(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			// No "crews" row for c_ghost at all — this is the crew whose
			// deletion (along with all its issues) is #2034's stated
			// worst case.
			"issue_counters": {{"crew_id": "c_ghost", "next_number": int64(9)}},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&counters); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if counters != 0 {
		t.Errorf("issue_counters rows = %d, want 0 — an unresolvable crew must not be guessed at", counters)
	}
	if stats.IssueCountersMigrated != 0 {
		t.Errorf("IssueCountersMigrated = %d, want 0", stats.IssueCountersMigrated)
	}
	want := []DroppedColumn{{Table: "issue_counters", Column: "crew_id", Rows: 1}}
	if !sameDroppedColumns(stats.DroppedColumns, want) {
		t.Errorf("DroppedColumns = %+v, want %+v", stats.DroppedColumns, want)
	}
}

// TestRestoreDump_PreRekeyIssueCountersMergeTakesMax is the hazard the
// transform's doc comment calls out by name: two crews that share an
// effective prefix must collapse onto the HIGHER next_number, never the
// lower and never first-wins. Writing back the lower value would leave the
// allocator re-issuing identifiers that already exist — worse than the
// counter being absent, which the allocator self-heals from missions data.
func TestRestoreDump_PreRekeyIssueCountersMergeTakesMax(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			"crews": {
				// "engineering" and "engine" both derive ENG from their
				// slug's first three letters — the exact collision
				// migrations/20260820125000_issue_counters_prefix_scope.sql
				// exists to describe.
				{"id": "c_1", "workspace_id": "ws_1", "name": "Engineering", "slug": "engineering", "issue_prefix": "ENG"},
				{"id": "c_2", "workspace_id": "ws_1", "name": "Engine Room", "slug": "engine", "issue_prefix": "ENG"},
			},
			"issue_counters": {
				{"crew_id": "c_1", "next_number": int64(5)},
				{"crew_id": "c_2", "next_number": int64(42)},
			},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&counters); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if counters != 1 {
		t.Fatalf("issue_counters rows = %d, want 1 — one merged row for the shared prefix", counters)
	}
	var next int64
	if err := db.QueryRow(`SELECT next_number FROM issue_counters WHERE workspace_id = 'ws_1' AND prefix = 'ENG'`).Scan(&next); err != nil {
		t.Fatalf("query merged counter: %v", err)
	}
	if next != 42 {
		t.Errorf("merged next_number = %d, want 42 (the MAX of the two crews' counters)", next)
	}
	if stats.IssueCountersMigrated != 2 {
		t.Errorf("IssueCountersMigrated = %d, want 2 (two bundle rows folded into one)", stats.IssueCountersMigrated)
	}
}

// TestRestoreDump_PreRekeyIssueCountersPreservesLowercasePrefix pins that an
// explicit crews.issue_prefix restores VERBATIM, not upper-cased.
//
// validIssuePrefixRe permits lowercase (`^[A-Za-z0-9_-]{1,16}$`), and the
// runtime allocator's lookup (nextIssueIdentifierTx, crewIssuePrefix in
// internal/api/issue_create_core.go) returns an explicit issue_prefix
// verbatim and does an exact `WHERE prefix = ?` match — it never
// upper-cases. The pre-fix transform upper-cased the explicit-prefix branch
// (but NOT its own SQL fallback three lines below it, nor the slug-fallback
// branch here), so a crew with issue_prefix="eng" landed its migrated
// counter under "ENG" — a key the allocator can never look up by this
// crew's real prefix. That reproduces #2034's own stated worst case for
// exactly the crew the fix exists to save: all its issues were deleted
// before the backup, so on restore it reseeds from missions (none) and
// restarts at 1, reissuing "eng-1".."eng-42" on top of identifiers that
// already exist.
func TestRestoreDump_PreRekeyIssueCountersPreservesLowercasePrefix(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			"crews": {{
				"id": "c_1", "workspace_id": "ws_1",
				"name": "Engineering", "slug": "engineering", "issue_prefix": "eng",
			}},
			"issue_counters": {{"crew_id": "c_1", "next_number": int64(42)}},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	var prefix string
	var next int64
	if err := db.QueryRow(`SELECT prefix, next_number FROM issue_counters WHERE workspace_id = 'ws_1'`).
		Scan(&prefix, &next); err != nil {
		t.Fatalf("migrated counter: %v", err)
	}
	if prefix != "eng" {
		t.Errorf("migrated prefix = %q, want %q (verbatim, not upper-cased) — "+
			"the allocator's lookup is an exact match against crews.issue_prefix", prefix, "eng")
	}
	if next != 42 {
		t.Errorf("migrated next_number = %d, want 42", next)
	}
	if stats.IssueCountersMigrated != 1 {
		t.Errorf("IssueCountersMigrated = %d, want 1", stats.IssueCountersMigrated)
	}
}

// TestRestoreDump_PreRekeyIssueCountersMissionsHighWaterMark pins the second
// arm of the migration's collapse
// (migrations/20260820125000_issue_counters_prefix_scope.sql takes MAX over a
// UNION ALL of the per-crew counters AND the high-water mark already minted
// under that prefix in missions): a bundled counter that is present but too
// low must still be raised to at least what missions.identifier already
// proves was handed out.
//
// The two failure modes are not symmetric. An ABSENT counter self-heals —
// nextIssueIdentifierTx reseeds it from missions on first use (see
// internal/api/issue_create_core.go:150). A PRESENT counter that is too low
// never does: the allocator takes the `next_number + 1` UPDATE branch
// forever. Crew A (prefix ENG) minted ENG-1..ENG-40 and was deleted — its
// counter row is gone with it (issue_counters carries no ON DELETE CASCADE
// post-#1797, but pre-#1797 it did), yet its missions remain. Crew B shares
// the same effective prefix and is the pre-#1797 collision victim wedged at
// next_number=1 — crew B is the only one left in the bundle's crews/
// issue_counters tables, exactly like #2034's "crew whose issues were
// deleted" case, except here ANOTHER crew's missions still occupy the
// namespace. Folding only the bundled counter writes back 1: the very next
// allocation is ENG-2, which already exists, so the allocator's UPDATE
// increments, the INSERT is rejected by idx_mission_workspace_identifier,
// and — because the counter upsert and the mission insert share one
// transaction — the rejection rolls the increment back too. The crew can
// never file an issue again; before this fix the row was simply dropped and
// the allocator self-healed to ENG-41.
func TestRestoreDump_PreRekeyIssueCountersMissionsHighWaterMark(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			// Only crew B is in the bundle. Crew A, which actually minted
			// the high identifiers, was deleted before the backup — its
			// counter cascaded away with it, but the missions it created
			// did not.
			"crews": {{
				"id": "c_b", "workspace_id": "ws_1",
				"name": "Crew B", "slug": "crewb", "issue_prefix": "ENG",
			}},
			"issue_counters": {{"crew_id": "c_b", "next_number": int64(1)}},
			"missions": {{
				"id": "m_1", "workspace_id": "ws_1", "identifier": "ENG-40", "number": int64(40),
			}},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	var next int64
	if err := db.QueryRow(`SELECT next_number FROM issue_counters WHERE workspace_id = 'ws_1' AND prefix = 'ENG'`).
		Scan(&next); err != nil {
		t.Fatalf("migrated counter: %v", err)
	}
	if next != 40 {
		t.Errorf("migrated next_number = %d, want 40 (the missions high-water mark, not the bundled counter's 1) — "+
			"a counter written back too low can never self-heal", next)
	}
	if stats.IssueCountersMigrated != 1 {
		t.Errorf("IssueCountersMigrated = %d, want 1", stats.IssueCountersMigrated)
	}
}

// TestRestoreBackup_PreRekeyBundleForkedRestoreLandsInNewWorkspace pins the
// --as-workspace / --as-crew case: RemapIDs regenerates crews.id BEFORE
// migrateIssueCounterRows runs, but issue_counters.crew_id has no declared
// FOREIGN KEY on a post-#1797 target (the column does not exist in that
// schema at all), so without issue_counters registered in virtualForeignKeys
// the bundle row's crew_id is left pointing at the crew's OLD id.
//
// crewIssueScopesFromDump then misses (it is keyed by the crews table's
// NEW, already-remapped ids), migrateIssueCounterRows falls back to its tx
// query, and on the realistic fork scenario — --as-workspace beside a
// still-present source workspace on the SAME instance — that fallback finds
// a crew that genuinely exists under the old id: the SOURCE crew. The
// migrated counter then lands under the SOURCE's workspace_id instead of
// the fork's, while RestoreStats.IssueCountersMigrated still reports
// success.
func TestRestoreBackup_PreRekeyBundleForkedRestoreLandsInNewWorkspace(t *testing.T) {
	ctx := context.Background()

	db := openMigratedDBCov(t)
	// The source workspace/crew this bundle was taken from, still present
	// on the SAME instance — the documented --as-workspace use case (fork
	// beside the original, not replace it).
	sourceWsID, sourceCrewID := seedCovWorkspace(t, db, "fork_src")
	if _, err := db.ExecContext(ctx,
		`UPDATE crews SET issue_prefix = 'ENG' WHERE id = ?`, sourceCrewID); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	dumpJSON := fmt.Sprintf(`{
		"workspace_id": %[1]q,
		"tables": {
			"workspaces": [{"id": %[1]q, "name": "Source", "slug": "cov-fork_src"}],
			"crews": [{"id": %[2]q, "workspace_id": %[1]q, "name": "Engineering", "slug": "crew-fork_src", "issue_prefix": "ENG"}],
			"issue_counters": [{"crew_id": %[2]q, "next_number": 42}]
		}
	}`, sourceWsID, sourceCrewID)
	bundle := writeRawBundle(t, t.TempDir(), &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Now().UTC(),
		CreatedBy:         Actor{UserID: "u_cov"},
	}, buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: []byte(dumpJSON)}}),
		WriteBundleOptions{NoEncrypt: true}, "")

	result, err := RestoreBackup(ctx, db, RestoreOptions{
		Path:        bundle,
		Actor:       covAdminActor(),
		AsWorkspace: "cov-fork_dst",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	if result.RestoredWorkspaceID == "" {
		t.Fatalf("restore reported no RestoredWorkspaceID; nothing to verify")
	}
	if result.RestoredWorkspaceID == sourceWsID {
		t.Fatalf("--as-workspace did not remap the workspace id (still %s)", sourceWsID)
	}
	if result.IssueCountersMigrated != 1 {
		t.Fatalf("IssueCountersMigrated = %d, want 1", result.IssueCountersMigrated)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issue_counters WHERE workspace_id = ?`, sourceWsID).Scan(&count); err != nil {
		t.Fatalf("count source counters: %v", err)
	}
	if count != 0 {
		t.Errorf("the migrated counter landed under the SOURCE workspace (%s): "+
			"a forked restore must not write into the workspace it was forked FROM", sourceWsID)
	}

	var workspaceID, prefix string
	var next int64
	if err := db.QueryRowContext(ctx,
		`SELECT workspace_id, prefix, next_number FROM issue_counters WHERE prefix = 'ENG'`).
		Scan(&workspaceID, &prefix, &next); err != nil {
		t.Fatalf("migrated counter: %v", err)
	}
	if workspaceID != result.RestoredWorkspaceID {
		t.Errorf("migrated counter workspace_id = %q, want %q (the FORKED workspace)", workspaceID, result.RestoredWorkspaceID)
	}
	if next != 42 {
		t.Errorf("migrated next_number = %d, want 42", next)
	}
}

// TestRestoreDump_DroppedColumnsAggregateAcrossRowsAndTables checks the
// accounting: one entry per (table, column) pair carrying its own row count,
// a total that counts every discarded value, and a deterministic order —
// BackupTables order, then column name, which is the order the insert loop
// itself walks in.
func TestRestoreDump_DroppedColumnsAggregateAcrossRowsAndTables(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			"crews": {
				// Two unknown columns on one row, and one of them again on
				// the next: the pair count and the row count are different
				// numbers and both are worth having.
				{"id": "c_1", "workspace_id": "ws_1", "slug": "a", "retired_at": "2026-01-01", "old_flag": 1},
				{"id": "c_2", "workspace_id": "ws_1", "slug": "b", "retired_at": "2026-01-02"},
			},
			// Unresolvable crew_ids on purpose — this test is about the
			// generic column-whitelist accounting across two tables, not
			// about migrateIssueCounterRows, so these must stay in the
			// "dropped" bucket rather than migrating out of it. See
			// TestRestoreDump_PreRekeyIssueCountersMigrates for the
			// resolvable case.
			"issue_counters": {
				{"crew_id": "c_missing_1", "next_number": int64(42)},
				{"crew_id": "c_missing_2", "next_number": int64(7)},
			},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.ColumnsDropped != 5 {
		t.Errorf("ColumnsDropped = %d, want 5 (crews: 2+1, issue_counters: 1+1)", stats.ColumnsDropped)
	}
	if stats.IssueCountersMigrated != 0 {
		t.Errorf("IssueCountersMigrated = %d, want 0 — both crew_ids are unresolvable", stats.IssueCountersMigrated)
	}
	want := []DroppedColumn{
		{Table: "crews", Column: "old_flag", Rows: 1},
		{Table: "crews", Column: "retired_at", Rows: 2},
		{Table: "issue_counters", Column: "crew_id", Rows: 2},
	}
	if !sameDroppedColumns(stats.DroppedColumns, want) {
		t.Errorf("DroppedColumns = %+v, want %+v", stats.DroppedColumns, want)
	}
}

// TestRestoreDump_CleanBundleReportsNoDrops is the other half of the guard: a
// bundle whose columns all exist must report nothing. A counter that is never
// zero is a warning operators learn to ignore.
func TestRestoreDump_CleanBundleReportsNoDrops(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	stats, err := RestoreDumpTx(context.Background(), db, &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces":     {{"id": "ws_1", "name": "Acme", "slug": "acme"}},
			"issue_counters": {{"workspace_id": "ws_1", "prefix": "ENG", "next_number": int64(42)}},
		},
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.ColumnsDropped != 0 || stats.DroppedColumns != nil {
		t.Errorf("clean bundle reported drops: %d %+v", stats.ColumnsDropped, stats.DroppedColumns)
	}
	if stats.RowsInserted != 2 {
		t.Errorf("RowsInserted = %d, want 2 — the new-shape counter must still land", stats.RowsInserted)
	}
}

// TestRestoreDump_DroppedColumnSampleIsBounded pins the split between the
// count and the sample. dump.json is attacker-controlled input on a tampered
// bundle, so the number of DISTINCT unknown column names it can name is
// unbounded; the per-pair detail is capped and the total stays exact.
func TestRestoreDump_DroppedColumnSampleIsBounded(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	row := map[string]any{"id": "ws_1", "name": "Acme", "slug": "acme"}
	const junk = maxDroppedColumnsReported + 7
	for i := 0; i < junk; i++ {
		row[fmt.Sprintf("junk_%02d", i)] = i
	}
	stats, err := RestoreDumpTx(context.Background(), db, &DBDump{
		WorkspaceID: "ws_1",
		Tables:      map[string][]map[string]any{"workspaces": {row}},
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.ColumnsDropped != junk {
		t.Errorf("ColumnsDropped = %d, want %d — the count must be exact even when the sample is not", stats.ColumnsDropped, junk)
	}
	if len(stats.DroppedColumns) != maxDroppedColumnsReported {
		t.Errorf("len(DroppedColumns) = %d, want %d", len(stats.DroppedColumns), maxDroppedColumnsReported)
	}
	// The row itself still landed: its known columns were all present.
	if stats.RowsInserted != 1 {
		t.Errorf("RowsInserted = %d, want 1", stats.RowsInserted)
	}
}

// TestInspectDroppedColumns_MatchesTheRestore is what makes the dry run worth
// running: it must report the same skew the committed restore would, computed
// against the same target, without writing anything.
func TestInspectDroppedColumns_MatchesTheRestore(t *testing.T) {
	ctx := context.Background()
	db := newDroppedColumnTargetDB(t)

	total, dropped, migrated, err := InspectDroppedColumns(ctx, db, preRekeyDump())
	if err != nil {
		t.Fatalf("InspectDroppedColumns: %v", err)
	}
	// crew_id is no longer reported as dropped: the crew resolves (it is
	// in the same bundle) and migrateIssueCounterRows translates the row
	// instead of losing it. See TestRestoreDump_PreRekeyIssueCountersMigrates.
	if total != 0 {
		t.Errorf("inspect total = %d, want 0 — the row migrates, it does not drop", total)
	}
	if len(dropped) != 0 {
		t.Errorf("inspect = %+v, want none", dropped)
	}
	if migrated != 1 {
		t.Errorf("inspect migrated = %d, want 1", migrated)
	}

	// Inspection is read-only.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&rows); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if rows != 0 {
		t.Errorf("InspectDroppedColumns wrote %d workspace row(s); it must not write at all", rows)
	}

	// And it agrees with the real thing, which is the whole point.
	stats, err := RestoreDumpTx(ctx, db, preRekeyDump(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.ColumnsDropped != total || !sameDroppedColumns(stats.DroppedColumns, dropped) {
		t.Errorf("dry-run inspection (%d %+v) disagrees with the restore (%d %+v)",
			total, dropped, stats.ColumnsDropped, stats.DroppedColumns)
	}
	if stats.IssueCountersMigrated != migrated {
		t.Errorf("dry-run inspection migrated=%d disagrees with the restore migrated=%d", migrated, stats.IssueCountersMigrated)
	}
}

// TestInspectDroppedColumns_SkipsTablesTheTargetLacks separates the two kinds
// of skew. A whole table missing from the target is already handled (and
// already reported through RowsSeen vs RowsInserted); counting each of its
// columns as a dropped column would bury the one-column case this exists to
// surface.
func TestInspectDroppedColumns_SkipsTablesTheTargetLacks(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	total, dropped, migrated, err := InspectDroppedColumns(context.Background(), db, &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			// labels is in BackupTables but not in this target's schema.
			"labels": {{"id": "l_1", "workspace_id": "ws_1", "name": "bug"}},
		},
	})
	if err != nil {
		t.Fatalf("InspectDroppedColumns: %v", err)
	}
	if total != 0 || dropped != nil || migrated != 0 {
		t.Errorf("absent table reported as dropped columns: %d %+v migrated=%d", total, dropped, migrated)
	}
}

// TestRestoreBackup_PreRekeyBundleMigrates drives the whole runner against
// the REAL migrated schema, which is the only place the post-#1797
// issue_counters definition can come from without a hand-copy that could
// drift. The bundle's dump.json is written by hand because no current
// instance can produce the old shape any more.
//
// This is #2034's fix end to end: the crew is IN the bundle, so
// migrateIssueCounterRows resolves its workspace and effective prefix and
// the counter lands instead of being dropped. Both halves matter — the
// operator-facing note (an API handler with a nil Logger is how #1716's
// dropped filesystems stayed quiet) and the structured field on
// RestoreResult, which is what the API response and the CLI read.
func TestRestoreBackup_PreRekeyBundleMigrates(t *testing.T) {
	ctx := context.Background()

	dumpJSON := []byte(`{
		"workspace_id": "ws_prerekey",
		"tables": {
			"workspaces": [{"id": "ws_prerekey", "name": "Pre Rekey", "slug": "pre-rekey"}],
			"crews": [{"id": "c_prerekey", "workspace_id": "ws_prerekey", "name": "Engineering", "slug": "engineering"}],
			"issue_counters": [{"crew_id": "c_prerekey", "next_number": 42}]
		}
	}`)
	bundle := writeRawBundle(t, t.TempDir(), &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Now().UTC(),
		CreatedBy:         Actor{UserID: "u_cov"},
	}, buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}}),
		WriteBundleOptions{NoEncrypt: true}, "")

	for _, tc := range []struct {
		name   string
		dryRun bool
		verb   string
	}{
		// The dry run is where an operator can still act on schema skew, so
		// it reports the migration too — same reason the security-level
		// clamp is reported there (#1603).
		{name: "dry run", dryRun: true, verb: "would be migrated"},
		{name: "committed", dryRun: false, verb: "migrated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged []string
			result, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
				Path:   bundle,
				Actor:  covAdminActor(),
				DryRun: tc.dryRun,
				Logger: func(msg string) { logged = append(logged, msg) },
			})
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if result.ColumnsDropped != 0 {
				t.Fatalf("ColumnsDropped = %d, want 0 — crew_id was migrated, not dropped: %+v", result.ColumnsDropped, result.DroppedColumns)
			}
			if result.IssueCountersMigrated != 1 {
				t.Fatalf("IssueCountersMigrated = %d, want 1", result.IssueCountersMigrated)
			}
			joined := strings.Join(logged, "\n")
			if !strings.Contains(joined, "issue_counters") || !strings.Contains(joined, tc.verb) {
				t.Errorf("operator note missing %q / %q from:\n%s", "issue_counters", tc.verb, joined)
			}
		})
	}
}

// TestRestoreBackup_PreRekeyBundleUnresolvedCrewSurfacesTheSkew is the other
// side at the runner level: a pre-#1797 counter whose crew is NOT in the
// bundle (the crew and all its issues were deleted before the backup — the
// worst case #2034 names) cannot be migrated, and must still surface as a
// counted, reported skip rather than a silent drop.
func TestRestoreBackup_PreRekeyBundleUnresolvedCrewSurfacesTheSkew(t *testing.T) {
	ctx := context.Background()

	dumpJSON := []byte(`{
		"workspace_id": "ws_prerekey",
		"tables": {
			"workspaces": [{"id": "ws_prerekey", "name": "Pre Rekey", "slug": "pre-rekey"}],
			"issue_counters": [{"crew_id": "c_gone", "next_number": 42}]
		}
	}`)
	bundle := writeRawBundle(t, t.TempDir(), &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         time.Now().UTC(),
		CreatedBy:         Actor{UserID: "u_cov"},
	}, buildPayloadTarZst(t, []payloadEntry{{name: "db/dump.json", body: dumpJSON}}),
		WriteBundleOptions{NoEncrypt: true}, "")

	for _, tc := range []struct {
		name   string
		dryRun bool
		verb   string
	}{
		{name: "dry run", dryRun: true, verb: "would be dropped"},
		{name: "committed", dryRun: false, verb: "were dropped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged []string
			result, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
				Path:   bundle,
				Actor:  covAdminActor(),
				DryRun: tc.dryRun,
				Logger: func(msg string) { logged = append(logged, msg) },
			})
			if err != nil {
				t.Fatalf("RestoreBackup: %v", err)
			}
			if result.ColumnsDropped != 1 {
				t.Fatalf("ColumnsDropped = %d, want 1", result.ColumnsDropped)
			}
			if result.IssueCountersMigrated != 0 {
				t.Fatalf("IssueCountersMigrated = %d, want 0 — c_gone cannot be resolved", result.IssueCountersMigrated)
			}
			want := []DroppedColumn{{Table: "issue_counters", Column: "crew_id", Rows: 1}}
			if !sameDroppedColumns(result.DroppedColumns, want) {
				t.Fatalf("DroppedColumns = %+v, want %+v", result.DroppedColumns, want)
			}
			joined := strings.Join(logged, "\n")
			if !strings.Contains(joined, "issue_counters.crew_id") || !strings.Contains(joined, tc.verb) {
				t.Errorf("operator warning missing %q / %q from:\n%s", "issue_counters.crew_id", tc.verb, joined)
			}
		})
	}
}

// TestRestoreBackup_SameSchemaRoundTripDropsNothing is the other side of the
// guard, and the reason the warning is worth reading when it does fire: a
// bundle taken and restored on the CURRENT schema must report zero. If this
// ever goes non-zero, some table is losing a column on an ordinary restore
// today and the name in the failure message is where to look.
//
// Read it for what it is, though, and do not over-trust it. DumpWorkspace
// emits `SELECT *`, so on a target carrying the same migrations the column
// sets match by construction — this cannot fail unless the DUMP side starts
// synthesising a key that is not a real column (a computed field, a rename
// applied at dump time). That is a genuine regression to guard, and it is the
// only one this guards. It is not a survey of the schema: only the tables
// seedCovWorkspace populates carry rows, and a table with no rows is never
// walked. "Zero drops today" is therefore a statement about the dumper, not
// evidence that every table in BackupTables round-trips cleanly.
func TestRestoreBackup_SameSchemaRoundTripDropsNothing(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDBCov(t)
	wsID, _ := seedCovWorkspace(t, source, "nodrops")
	created, err := CreateBackup(ctx, source, CreateOptions{
		Scope:       ScopeWorkspace,
		WorkspaceID: wsID,
		OutputDir:   t.TempDir(),
		Actor:       covAdminActor(),
		NoEncrypt:   true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	result, err := RestoreBackup(ctx, openMigratedDBCov(t), RestoreOptions{
		Path:  created.Path,
		Actor: covAdminActor(),
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.ColumnsDropped != 0 {
		t.Errorf("a same-schema round trip dropped %d value(s): %+v — "+
			"every column a dump writes should exist on a target running the "+
			"same migrations", result.ColumnsDropped, result.DroppedColumns)
	}
}

// sameDroppedColumns compares reported drops including order — the order is
// part of the contract, because a report that reshuffles between runs cannot
// be diffed across two restores of the same bundle.
func sameDroppedColumns(got, want []DroppedColumn) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
