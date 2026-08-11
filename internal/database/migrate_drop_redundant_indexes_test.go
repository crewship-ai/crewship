package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// An index whose columns are a leading prefix of a longer index on the same
// table can never be chosen by the planner — SQLite already uses the longer
// one for any prefix of its columns — but it is still maintained on every
// write. On SQLite that maintenance happens while holding the single
// database-wide write lock, so redundant indexes on hot tables are a direct
// tax on how many agents can write concurrently.
//
// The migration removes the eleven that had accumulated. These tests pin the
// removal, prove the coverage that justified it still exists, and — the part
// that keeps paying — fail if anyone reintroduces the shape.

// indexShape is one index as the planner sees it: which columns, in order,
// and (for a partial index) the predicate that decides which rows it holds.
type indexShape struct {
	name    string
	table   string
	cols    []string
	unique  bool
	partial string // normalised WHERE clause, "" when not partial
}

// coversPrefixOf reports whether a's columns are a leading prefix of b's AND
// both admit the same rows. The partial-predicate equality is what stops this
// from reporting a false positive: an index restricted to a different subset
// of rows covers nothing, however its columns line up.
func (a indexShape) coversPrefixOf(b indexShape) bool {
	if a.name == b.name || a.table != b.table {
		return false
	}
	if a.partial != b.partial {
		return false
	}
	if len(a.cols) > len(b.cols) {
		return false
	}
	for i, c := range a.cols {
		if b.cols[i] != c {
			return false
		}
	}
	return true
}

func (a indexShape) String() string {
	s := fmt.Sprintf("%s(%s)", a.name, strings.Join(a.cols, ", "))
	if a.partial != "" {
		s += " WHERE " + a.partial
	}
	return s
}

// readIndexShapes reflects every explicitly-created index in the database.
// Indexes SQLite created itself for UNIQUE/PRIMARY KEY constraints (origin
// "u"/"pk") are excluded: they are not ours to drop.
func readIndexShapes(t *testing.T, db *DB) []indexShape {
	t.Helper()

	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	var out []indexShape
	for _, table := range tables {
		ir, err := db.Query(fmt.Sprintf(`PRAGMA index_list(%q)`, table))
		if err != nil {
			t.Fatalf("index_list(%s): %v", table, err)
		}
		type listRow struct {
			name   string
			unique bool
			origin string
		}
		var listed []listRow
		for ir.Next() {
			var seq int
			var name, origin string
			var unique, partial int
			if err := ir.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
				ir.Close()
				t.Fatalf("scan index_list(%s): %v", table, err)
			}
			if origin != "c" { // "c" == created by CREATE INDEX
				continue
			}
			listed = append(listed, listRow{name, unique == 1, origin})
		}
		ir.Close()

		for _, li := range listed {
			shape := indexShape{name: li.name, table: table, unique: li.unique}

			cr, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%q)`, li.name))
			if err != nil {
				t.Fatalf("index_info(%s): %v", li.name, err)
			}
			expression := false
			for cr.Next() {
				var seqno, cid int
				var col *string
				if err := cr.Scan(&seqno, &cid, &col); err != nil {
					cr.Close()
					t.Fatalf("scan index_info(%s): %v", li.name, err)
				}
				if col == nil {
					// An expression index: SQLite reports no column name, so
					// prefix comparison is not answerable. Skip it entirely.
					expression = true
					break
				}
				shape.cols = append(shape.cols, *col)
			}
			cr.Close()
			if expression || len(shape.cols) == 0 {
				continue
			}

			var ddl *string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, li.name).Scan(&ddl); err != nil {
				t.Fatalf("read ddl for %s: %v", li.name, err)
			}
			if ddl != nil {
				if i := strings.Index(strings.ToLower(*ddl), " where "); i >= 0 {
					shape.partial = strings.Join(strings.Fields((*ddl)[i+len(" where "):]), " ")
				}
			}
			out = append(out, shape)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// TestRedundantIndexPolicy covers both halves: the eleven that were removed,
// and the invariant that nothing has grown back into the same shape.
func TestRedundantIndexPolicy(t *testing.T) {
	t.Parallel()
	// One migrated schema for both checks: migrateChainSetup runs the whole
	// ~200-migration chain per call, and this is two reads of one schema.
	db := migrateChainSetup(t)

	t.Run("the eleven redundant indexes are gone", func(t *testing.T) {
		droppedRedundantIndexes(t, db)
	})
	t.Run("no redundant index remains", func(t *testing.T) {
		schemaHasNoRedundantIndexes(t, db)
	})
}

// droppedRedundantIndexes pins the eleven removals AND the covering index each
// one relied on. Asserting the survivor matters as much as asserting the
// removal: dropping the narrow index is only safe while the wider one is there
// to answer the same queries.
func droppedRedundantIndexes(t *testing.T, db *DB) {
	t.Helper()
	tests := []struct {
		dropped   string
		coveredBy string
	}{
		{"idx_crew_member_user", "idx_crew_member_user_crew"},
		{"idx_chat_workspace", "idx_chats_ws_created"},
		{"idx_chat_agent", "idx_chats_agent_activity"},
		{"idx_assignment_to", "idx_assignment_to_status"},
		{"idx_credential_workspace", "idx_credential_type_provider"},
		{"idx_peer_conv_crew", "idx_peer_conv_crew_created"},
		{"idx_mission_workspace", "idx_missions_ws_created"},
		{"idx_workflow_templates_ws", "idx_workflow_templates_name_ws"},
		{"idx_pipelines_workspace", "idx_pipelines_workspace_status"},
		{"idx_attachments_workspace", "idx_attachments_blob"},
		{"idx_journal_trace_id", "idx_journal_trace"},
	}

	present := map[string]bool{}
	for _, s := range readIndexShapes(t, db) {
		present[s.name] = true
	}

	for _, tc := range tests {
		t.Run(tc.dropped, func(t *testing.T) {
			if present[tc.dropped] {
				t.Errorf("%s still exists; the migration did not drop it", tc.dropped)
			}
			if !present[tc.coveredBy] {
				t.Errorf("%s is gone, and so is %s which was supposed to cover it — the queries that used %s now have no index",
					tc.dropped, tc.coveredBy, tc.dropped)
			}
		})
	}
}

// schemaHasNoRedundantIndexes is the regression guard, and the reason
// this file earns its keep after the migration ships. It re-derives the
// redundancy from the live schema rather than from a list, so a NEW composite
// index added next year that happens to subsume an existing narrow one fails
// here instead of quietly costing every write.
//
// If this fails on an index you just added, the fix is usually to delete the
// older narrow index in the same migration — not to add an exception.
func schemaHasNoRedundantIndexes(t *testing.T, db *DB) {
	t.Helper()
	shapes := readIndexShapes(t, db)
	var findings []string
	for _, a := range shapes {
		if a.unique {
			// A UNIQUE index enforces a constraint. A wider non-unique index
			// starting with the same columns does not replace it, so this is
			// never redundancy.
			continue
		}
		for _, b := range shapes {
			if !a.coversPrefixOf(b) {
				continue
			}
			findings = append(findings, fmt.Sprintf(
				"  %-24s %s\n    is fully covered by %s\n    → the planner can never choose it, but every write to %s still maintains it",
				a.table+":", a, b, a.table))
			break
		}
	}

	if len(findings) > 0 {
		t.Errorf("%d redundant index(es) in the migrated schema:\n%s",
			len(findings), strings.Join(findings, "\n"))
	}
}
