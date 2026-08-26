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

// TestRestoreDump_PreRekeyIssueCountersIsCounted is #2034 itself, at the
// RestoreDump level. It asserts the drop is REPORTED, and — just as
// deliberately — that the row is still lost: this change makes the failure
// visible, it does not repair the counter. Mapping crew_id onto
// (workspace_id, prefix) is a separate fix with its own hazard, because two
// crews sharing an effective prefix merge into one counter and the merge has
// to take the MAX (see the backfill in
// migrations/20260820125000_issue_counters_prefix_scope.sql). Writing the
// wrong one back would leave the allocator handing out identifiers that
// already exist, which is worse than the row being absent — an absent row is
// re-seeded above the identifiers that restored alongside it.
func TestRestoreDump_PreRekeyIssueCountersIsCounted(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	stats, err := RestoreDumpTx(context.Background(), db, preRekeyDump(), func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Behaviour unchanged: the counter row did not land, and no error said so.
	var counters int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issue_counters`).Scan(&counters); err != nil {
		t.Fatalf("count counters: %v", err)
	}
	if counters != 0 {
		t.Fatalf("issue_counters rows = %d, want 0 — this test documents the "+
			"drop; if the row now lands, the transform landed too and this "+
			"test needs rewriting, not deleting", counters)
	}

	// What changed: the restore now says a column was thrown away.
	if stats.ColumnsDropped != 1 {
		t.Errorf("ColumnsDropped = %d, want 1 (issue_counters.crew_id has no "+
			"column on the target and was silently discarded)", stats.ColumnsDropped)
	}
	want := []DroppedColumn{{Table: "issue_counters", Column: "crew_id", Rows: 1}}
	if !sameDroppedColumns(stats.DroppedColumns, want) {
		t.Errorf("DroppedColumns = %+v, want %+v", stats.DroppedColumns, want)
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
			"issue_counters": {
				{"crew_id": "c_1", "next_number": int64(42)},
				{"crew_id": "c_2", "next_number": int64(7)},
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

	total, dropped, err := InspectDroppedColumns(ctx, db, preRekeyDump())
	if err != nil {
		t.Fatalf("InspectDroppedColumns: %v", err)
	}
	if total != 1 {
		t.Errorf("inspect total = %d, want 1", total)
	}
	want := []DroppedColumn{{Table: "issue_counters", Column: "crew_id", Rows: 1}}
	if !sameDroppedColumns(dropped, want) {
		t.Errorf("inspect = %+v, want %+v", dropped, want)
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
}

// TestInspectDroppedColumns_SkipsTablesTheTargetLacks separates the two kinds
// of skew. A whole table missing from the target is already handled (and
// already reported through RowsSeen vs RowsInserted); counting each of its
// columns as a dropped column would bury the one-column case this exists to
// surface.
func TestInspectDroppedColumns_SkipsTablesTheTargetLacks(t *testing.T) {
	db := newDroppedColumnTargetDB(t)

	total, dropped, err := InspectDroppedColumns(context.Background(), db, &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			// labels is in BackupTables but not in this target's schema.
			"labels": {{"id": "l_1", "workspace_id": "ws_1", "name": "bug"}},
		},
	})
	if err != nil {
		t.Fatalf("InspectDroppedColumns: %v", err)
	}
	if total != 0 || dropped != nil {
		t.Errorf("absent table reported as dropped columns: %d %+v", total, dropped)
	}
}

// TestRestoreBackup_PreRekeyBundleSurfacesTheSkew drives the whole runner
// against the REAL migrated schema, which is the only place the post-#1797
// issue_counters definition can come from without a hand-copy that could
// drift. The bundle's dump.json is written by hand because no current
// instance can produce the old shape any more.
//
// Both halves matter: the operator-facing warning (an API handler with a nil
// Logger is how #1716's dropped filesystems stayed quiet) and the structured
// fields on RestoreResult, which is what the API response and the CLI read.
func TestRestoreBackup_PreRekeyBundleSurfacesTheSkew(t *testing.T) {
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
		// The dry run is where an operator can still do something about it,
		// so it reports the skew too — same reason the security-level clamp
		// is reported there (#1603).
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
