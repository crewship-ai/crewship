package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// A foreign key whose child column is not the leading column of an index turns
// every parent DELETE into a full scan of the child table, once per deleted
// parent row, while holding SQLite's single write lock. Migration
// 20260810154153 indexes the ones that matter.
//
// "The ones that matter" is a judgement, and judgements rot. These tests pin
// both halves of it: the indexes that were added must exist and must actually
// lead with the FK column, and the deliberate exclusions must stay excluded
// for the reason they were excluded — not drift into "nobody looked again".

// hotForeignKeys is the set the migration indexes, as (table, column).
var hotForeignKeys = [][2]string{
	{"credential_audit", "agent_id"},
	{"attachments", "uploaded_by_agent_id"},
	{"peer_card_audit", "agent_id"},
	{"port_exposures", "agent_id"},
	{"port_exposures", "crew_id"},
	{"port_exposures", "chat_id"},
	{"approvals_queue", "crew_id"},
	{"approvals_queue", "mission_id"},
	{"checkpoints", "crew_id"},
	{"checkpoints", "fork_of"},
	{"memory_health_snapshots", "crew_id"},
	{"message_feedback", "chat_id"},
	{"eval_runs", "baseline_mission_id"},
	{"eval_runs", "candidate_mission_id"},
	{"mission_code_links", "credential_id"},
	{"mission_comment_mentions", "assignment_id"},
}

// leadingIndexedColumns returns every column that is the FIRST column of some
// index on the table (including the implicit rowid/PK index). Leading is the
// only position that helps a foreign key check — an index on (status, crew_id)
// does nothing for a delete cascading on crew_id.
func leadingIndexedColumns(t *testing.T, db *DB, table string) map[string]string {
	t.Helper()
	out := map[string]string{}

	rows, err := db.Query(fmt.Sprintf(`PRAGMA index_list(%q)`, table))
	if err != nil {
		t.Fatalf("index_list(%s): %v", table, err)
	}
	var names []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			t.Fatalf("scan index_list(%s): %v", table, err)
		}
		names = append(names, name)
	}
	rows.Close()

	for _, name := range names {
		ir, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, name))
		if err != nil {
			t.Fatalf("index_info(%s): %v", name, err)
		}
		first := true
		for ir.Next() {
			var seqno, cid int
			var col *string
			if err := ir.Scan(&seqno, &cid, &col); err != nil {
				ir.Close()
				t.Fatalf("scan index_info(%s): %v", name, err)
			}
			if first && col != nil {
				if _, seen := out[*col]; !seen {
					out[*col] = name
				}
			}
			first = false
		}
		ir.Close()
	}

	// An INTEGER PRIMARY KEY / rowid alias is indexed by definition.
	tr, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer tr.Close()
	for tr.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt *string
		if err := tr.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if pk > 0 {
			if _, seen := out[name]; !seen {
				out[name] = "(primary key)"
			}
		}
	}
	return out
}

// hotForeignKeysAreIndexed proves each one leads an index. Asserting the
// index NAME would pass on an index over the wrong columns; asserting the
// leading column is what the query planner and the FK check actually care
// about.
func TestForeignKeyIndexPolicy(t *testing.T) {
	t.Parallel()
	// One migrated schema for all three checks. migrateChainSetup runs the
	// whole ~200-migration chain every call and this package already pays for
	// dozens of those, so three separate top-level tests here would be three
	// more full chains for what is one read of the same schema.
	db := migrateChainSetup(t)

	t.Run("hot foreign keys are indexed", func(t *testing.T) {
		hotForeignKeysAreIndexed(t, db)
	})
	t.Run("user foreign keys stay unindexed", func(t *testing.T) {
		userForeignKeysStayUnindexed(t, db)
	})
	t.Run("unindexed count has not regressed", func(t *testing.T) {
		unindexedForeignKeyCount(t, db)
	})
}

func hotForeignKeysAreIndexed(t *testing.T, db *DB) {
	t.Helper()
	for _, fk := range hotForeignKeys {
		table, col := fk[0], fk[1]
		t.Run(table+"."+col, func(t *testing.T) {
			leading := leadingIndexedColumns(t, db, table)
			if idx, ok := leading[col]; !ok {
				t.Errorf("%s.%s does not lead any index — every DELETE of a parent row full-scans %s to enforce the constraint",
					table, col, table)
			} else if idx == "" {
				t.Errorf("%s.%s resolved to an empty index name", table, col)
			}
		})
	}
}

// userForeignKeysStayUnindexed pins the biggest deliberate exclusion, so
// it stays a decision rather than an oversight.
//
// Nothing in the tree hard-deletes a `users` row — there is no
// `DELETE FROM users` in non-test Go — so an index on a column referencing
// users is pure write cost with no delete to accelerate. If that changes, this
// test should fail and be replaced by indexes, which is exactly the prompt we
// want at that moment.
func userForeignKeysStayUnindexed(t *testing.T, db *DB) {
	t.Helper()
	// A representative sample, not the full list: enough that a blanket
	// "index every FK" sweep trips over it.
	excluded := [][2]string{
		{"attachments", "uploaded_by_user_id"},
		{"message_reactions", "user_id"},
		{"peer_card_audit", "actor_user_id"},
		{"workspace_files", "created_by"},
	}

	for _, fk := range excluded {
		table, col := fk[0], fk[1]
		leading := leadingIndexedColumns(t, db, table)
		if idx, ok := leading[col]; ok {
			t.Errorf("%s.%s is now indexed (%s). That is only worth its write cost if `users` rows are hard-deleted — "+
				"check for `DELETE FROM users` in non-test Go. If one exists, update this test and index the rest of the "+
				"users-referencing columns too; if not, drop the index.", table, col, idx)
		}
	}
}

// unindexedForeignKeyCount is a coarse ratchet. It does not
// demand that every FK be indexed — most should not be — but it does make the
// total visible, so a future migration that quietly adds ten unindexed foreign
// keys to hot tables shows up as a number moving rather than as nothing.
func unindexedForeignKeyCount(t *testing.T, db *DB) {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	sort.Strings(tables)

	var unindexed []string
	for _, table := range tables {
		fr, err := db.Query(fmt.Sprintf(`PRAGMA foreign_key_list(%q)`, table))
		if err != nil {
			t.Fatalf("foreign_key_list(%s): %v", table, err)
		}
		var cols []string
		for fr.Next() {
			var id, seq int
			var parent, from, to, onUp, onDel, match string
			if err := fr.Scan(&id, &seq, &parent, &from, &to, &onUp, &onDel, &match); err != nil {
				fr.Close()
				t.Fatalf("scan fk(%s): %v", table, err)
			}
			cols = append(cols, from)
		}
		fr.Close()
		if len(cols) == 0 {
			continue
		}
		leading := leadingIndexedColumns(t, db, table)
		for _, c := range cols {
			if _, ok := leading[c]; !ok {
				unindexed = append(unindexed, table+"."+c)
			}
		}
	}

	// 48 before this migration, 16 indexed by it, leaving 32. The 33rd is
	// waitpoint_trust_grants.pipeline_id, from v20260809120000: it arrived
	// after this ratchet was set and it stays unindexed on purpose, by the
	// same rule as the rest — `pipelines` is soft-deleted (deleted_at), and
	// no `DELETE FROM pipelines` exists in non-test Go, so there is no parent
	// delete for an index to accelerate.
	//
	// The remainder are the documented exclusions: parents that are never
	// hard-deleted (`users`) or child tables that do not grow (settings rows,
	// small config tables).
	const want = 33
	if len(unindexed) != want {
		sort.Strings(unindexed)
		t.Errorf("unindexed foreign key columns = %d, want %d.\n"+
			"Going UP means a new table added foreign keys nobody sized; going DOWN means indexes were added "+
			"outside 20260810154153 and this ratchet should be updated deliberately.\n%s",
			len(unindexed), want, "  "+strings.Join(unindexed, "\n  "))
	}
}
