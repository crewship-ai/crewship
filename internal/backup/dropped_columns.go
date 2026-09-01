package backup

// Counting the columns a restore throws away (#2034).
//
// RestoreDump whitelists every bundle column against the target schema: a name
// the target does not have is dropped from the INSERT and the row goes in
// without it. That is the right default — it is what lets a bundle from a
// newer Crewship restore into an older one at all, and every migration this
// project had written until #1797 was additive, so the dropped column was
// always a column the target genuinely did not need.
//
// #1797 was not additive. It re-keyed issue_counters from `crew_id` to
// `(workspace_id, prefix)`, and a bundle taken before it carries rows the new
// table has no key for. `crew_id` is dropped, the statement degenerates to
// `INSERT OR IGNORE INTO issue_counters (next_number) VALUES (?)`, the NOT NULL
// on the two key columns is violated, and OR IGNORE swallows that too. The
// restore reports success having landed nothing.
//
// The point of this file is NOT to guess what the operator meant. Rewriting a
// bundle's rows into a shape the current schema likes is a per-table judgement
// with real downside — for issue_counters specifically, writing a counter that
// is too LOW is worse than writing none at all, because the allocator re-seeds
// an absent counter above the identifiers that restored alongside it but takes
// a present one at its word. The point is that the restore should say what it
// did. #1437, #1444 and #1973 were all this same shape: a restore that was
// quietly incomplete and had no way to tell anyone.
//
// So: count it, name it, carry it out through RestoreStats → RestoreResult →
// the API response and the CLI, and warn. Behaviour is unchanged; silence is
// not.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// maxDroppedColumnsReported bounds the per-(table, column) detail carried out
// of a restore. dump.json is attacker-controlled on a tampered bundle and can
// name any number of distinct columns, so the sample is capped and nothing
// beyond the cap is retained. The COUNT is always exact.
const maxDroppedColumnsReported = 20

// DroppedColumn is one (table, column) pair the bundle carried and the target
// schema does not have, with the number of bundle rows that carried it.
//
// A pair rather than a per-row record on purpose: the operator's question is
// "which columns did this restore not understand", and one line per row would
// bury it under a table-sized repetition of the same fact.
type DroppedColumn struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Rows   int    `json:"rows"`
}

// droppedColumnKey is the map key behind the tally. A struct rather than a
// joined string so a column name containing the separator cannot alias
// another pair.
type droppedColumnKey struct {
	table  string
	column string
}

// droppedColumnTally accumulates drops during a restore pass. The zero value
// is ready to use.
type droppedColumnTally struct {
	total int
	rows  map[droppedColumnKey]int
	// order preserves first-encounter order, which is BackupTables order
	// then column name — the order the insert loop itself walks. Recorded
	// explicitly because map iteration is not ordered, and a report that
	// reshuffles between two restores of the same bundle cannot be diffed.
	order []droppedColumnKey
}

// record notes one discarded value. Called once per (row, column).
func (t *droppedColumnTally) record(table, column string) {
	t.total++
	k := droppedColumnKey{table: table, column: column}
	if _, seen := t.rows[k]; !seen {
		if len(t.order) >= maxDroppedColumnsReported {
			// Past the cap: keep counting, stop retaining. The map does not
			// grow, so a bundle naming a million columns costs a counter.
			return
		}
		if t.rows == nil {
			t.rows = make(map[droppedColumnKey]int, maxDroppedColumnsReported)
		}
		t.order = append(t.order, k)
	}
	t.rows[k]++
}

// result renders the tally: the exact total, and the bounded breakdown.
func (t *droppedColumnTally) result() (int, []DroppedColumn) {
	if t.total == 0 {
		return 0, nil
	}
	out := make([]DroppedColumn, 0, len(t.order))
	for _, k := range t.order {
		out = append(out, DroppedColumn{Table: k.table, Column: k.column, Rows: t.rows[k]})
	}
	return t.total, out
}

// tallyDroppedColumns records every key of row that allowed does not contain.
// Shared by the committed restore and the dry-run inspection so the two cannot
// count the same bundle differently — the dry run's whole value is that it
// predicts the real thing.
//
// keys must already be sorted; the caller sorts them anyway to keep the
// generated SQL deterministic.
func tallyDroppedColumns(t *droppedColumnTally, table string, keys []string, allowed map[string]bool) {
	for _, k := range keys {
		if !allowed[k] {
			t.record(table, k)
		}
	}
}

// InspectDroppedColumns reports what a restore of dump into db WOULD discard,
// without writing anything. Used by the dry run, which is the last point at
// which an operator can do something about schema skew other than read about
// it afterwards.
//
// A table the target lacks entirely is skipped rather than counted column by
// column: that is a different kind of skew, it is already visible as the gap
// between RowsSeen and RowsInserted, and folding it in here would bury the
// one-column case under a hundred-column one.
//
// An error probing the schema is returned, not swallowed. If the target cannot
// answer PRAGMA table_info then the restore this dry run is predicting cannot
// work either, and reporting "no skew found" because the question could not be
// asked is the exact failure mode this file exists to remove.
//
// The fourth return is how many issue_counters rows the same pre-#1797
// transform the committed restore runs (migrateIssueCounterRows, #2034) would
// migrate. It is computed here, not read off ColumnsDropped, because a row
// that migrates successfully must NOT show up as a dropped crew_id — the dry
// run's whole value is that it predicts the real thing, and the real thing
// does not drop that row.
func InspectDroppedColumns(ctx context.Context, db *sql.DB, dump *DBDump) (int, []DroppedColumn, int, error) {
	if db == nil || dump == nil {
		return 0, nil, 0, nil
	}
	// A transaction, not the bare *sql.DB, because tableColumns and
	// tableExistsTx are tx-bound — the same helpers the committed path uses,
	// so the two answers come from one definition of "does this column
	// exist". Rolled back unconditionally: nothing here writes.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("backup: inspect dropped columns: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var tally droppedColumnTally
	issueCountersMigrated := 0
	for _, table := range BackupTables {
		rows, ok := dump.Tables[table]
		if !ok || len(rows) == 0 {
			continue
		}
		exists, err := tableExistsTx(ctx, tx, table)
		if err != nil {
			return 0, nil, 0, fmt.Errorf("backup: probe %s: %w", table, err)
		}
		if !exists {
			continue
		}
		allowed, err := tableColumns(ctx, tx, table)
		if err != nil {
			return 0, nil, 0, fmt.Errorf("backup: columns of %s: %w", table, err)
		}
		if table == issueCountersTable && allowed["workspace_id"] && allowed["prefix"] && !allowed["crew_id"] {
			// Dry run only reports IssueCountersMigrated (the operator
			// message) — the collapsed count feeds rows_inserted_shortfalls,
			// which does not exist on a dry run (nothing was inserted).
			migratedRows, n, _, err := migrateIssueCounterRows(ctx, tx, rows, dump.Tables["crews"], dump.Tables["missions"], dump.Tables["workspaces"])
			if err != nil {
				return 0, nil, 0, fmt.Errorf("backup: migrate issue_counters rows: %w", err)
			}
			rows = migratedRows
			issueCountersMigrated += n
		}
		for _, row := range rows {
			keys := make([]string, 0, len(row))
			for k := range row {
				keys = append(keys, k)
			}
			sortStrings(keys)
			tallyDroppedColumns(&tally, table, keys, allowed)
		}
	}
	total, dropped := tally.result()
	return total, dropped, issueCountersMigrated, nil
}

// warnDroppedColumns emits the operator-facing warning. Shared by the dry-run
// and the committed path so the two cannot describe the same bundle
// differently — the same arrangement warnSecurityLevelClamps uses, and for the
// same reason.
func warnDroppedColumns(logger func(string), dropped []DroppedColumn, total int, dryRun bool) {
	if total == 0 || logger == nil {
		return
	}
	verb := "were dropped"
	if dryRun {
		verb = "would be dropped"
	}
	details := make([]string, 0, len(dropped))
	counted := 0
	for _, d := range dropped {
		counted += d.Rows
		details = append(details, fmt.Sprintf("%s.%s (%d row(s))", d.Table, d.Column, d.Rows))
	}
	more := ""
	if total > counted {
		more = fmt.Sprintf(" (+%d more value(s) in columns not listed)", total-counted)
	}
	logger(fmt.Sprintf(
		"WARNING: %d value(s) in this bundle %s because the target schema has no such column: %s%s. "+
			"This is schema skew — the bundle was written against a different schema than this instance runs. "+
			"Rows whose remaining columns satisfy the table still landed WITHOUT those values; rows that needed a dropped column to satisfy a NOT NULL or a primary key did not land at all, and `INSERT OR IGNORE` did not report it. "+
			"Check each table named above before treating this restore as complete.",
		total, verb, strings.Join(details, "; "), more))
}
