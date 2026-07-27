package backup_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestBackupIntent_TotalOverRealSchema is the guard #1487 asks for: it walks
// EVERY table in the real, fully-migrated schema and asserts each one is a
// deliberate backup decision — either in BackupTableIntent (a "back it up /
// exclude it" call) or in NonBackedUpTables (an explicit "not workspace bundle
// data" call).
//
// Why this and not the existing checks:
//   - CategoriseScopedTables / ErrDiscoveryDrift only sees tables the reverse-FK
//     walk from `workspaces` reaches. A table with a workspace_id COLUMN but no
//     FOREIGN KEY into the chain is invisible to it — and that is exactly how
//     #1437 and #1444 lost data: a new table nobody classified, silently
//     dropped from every bundle.
//   - The other intent tests (intent_test.go) run against a synthetic schema or
//     only check tables already in the map, so a brand-new unregistered table
//     never fails them.
//
// This test enumerates sqlite_master directly, so a new migration's table is
// unaccounted the moment it lands, and the build goes red with instructions.
func TestBackupIntent_TotalOverRealSchema(t *testing.T) {
	db := openMigratedDB(t)

	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var unaccounted []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		if isMechanicalTable(name) {
			continue
		}
		if !tableIsClassified(name) {
			unaccounted = append(unaccounted, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Fatalf("these tables in the real schema are not classified for backup: %v\n\n"+
			"Every table must be a deliberate decision. Add each to ONE of:\n"+
			"  • BackupTableIntent (intent.go) — IntentInclude (also add to BackupTables\n"+
			"    in dbdump.go, FK-safe order) or IntentExclude{Operational,Runtime}; or\n"+
			"  • NonBackedUpTables (intent.go) — if it is not workspace bundle data.\n"+
			"If it carries workspace_id/crew_id and holds durable user content, it almost\n"+
			"certainly belongs in BackupTableIntent, not the deny-list (see #1437, #1444).",
			unaccounted)
	}
}

// tableIsClassified reports whether a non-mechanical table has a deliberate
// backup decision — a BackupTableIntent entry (back up / exclude) or a
// NonBackedUpTables entry (not workspace bundle data).
func tableIsClassified(name string) bool {
	if _, ok := backup.BackupTableIntent[name]; ok {
		return true
	}
	_, denied := backup.NonBackedUpTables[name]
	return denied
}

// isMechanicalTable reports whether a table is SQLite/engine bookkeeping rather
// than an application table that needs a backup decision: the sqlite_* internal
// tables (incl. sqlite_stat*), the _migrations version ledger, and FTS5 shadow
// tables (…_fts and its …_fts_data/_idx/_docsize/_config shards), whose content
// is derived from their base table and rebuilt on demand. No application table
// in this schema contains "_fts", so the substring match is safe.
func isMechanicalTable(name string) bool {
	switch {
	case strings.HasPrefix(name, "sqlite_"):
		return true
	case name == "_migrations":
		return true
	case strings.Contains(name, "_fts"):
		return true
	default:
		return false
	}
}

// TestTotalityGuard_FailsOnUnclassifiedTable is the guard-of-the-guard: it
// proves the classification predicate the real-schema test relies on actually
// rejects an unknown table (a passing guard is worthless if its check can never
// fail). A known deny-listed name and a known IntentInclude name must classify;
// a fabricated one must not.
func TestTotalityGuard_FailsOnUnclassifiedTable(t *testing.T) {
	if tableIsClassified("definitely_not_a_real_table_xyz") {
		t.Error("predicate classified a fabricated table — the guard could never fail")
	}
	if !tableIsClassified("journal_chain_checkpoints") {
		t.Error("expected the IntentInclude table journal_chain_checkpoints to classify")
	}
	if !tableIsClassified("users") {
		t.Error("expected the deny-listed table users to classify")
	}
	// A mechanical table is skipped before the predicate runs — assert the
	// skip rule recognises real shadow/bookkeeping names and not a real table.
	for _, m := range []string{"sqlite_stat1", "_migrations", "journal_entries_fts_data"} {
		if !isMechanicalTable(m) {
			t.Errorf("expected %q to be treated as mechanical (skipped)", m)
		}
	}
	if isMechanicalTable("journal_entries") {
		t.Error("journal_entries must NOT be skipped as mechanical")
	}
}

// TestNonBackedUpTables_DisjointFromIntent pins that a table is never both
// "back up" and "do not back up" — a contradiction that would make the totality
// guard's decision ambiguous and silently mask a wrong call.
func TestNonBackedUpTables_DisjointFromIntent(t *testing.T) {
	for name := range backup.NonBackedUpTables {
		if _, ok := backup.BackupTableIntent[name]; ok {
			t.Errorf("table %q is in BOTH BackupTableIntent and NonBackedUpTables — "+
				"pick one: back it up, or don't", name)
		}
	}
}
