package backup

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ScopeReconciliation is the create-time answer to "is this bundle
// everything?" — the half of #2009 that TableRowCounts cannot give, because
// those counts come from the same in-memory dump the payload is written
// from.
//
// It is derived a second, independent way, inside the dump's own read
// snapshot: for every exported table, the rows the SCHEMA says belong to the
// workspace — rows whose own workspace_id names it, or whose single-column
// foreign key points at a row the dump exported from a workspace-owned
// parent — are compared against the rows the hand-written scope filter
// actually exported. A filter that walks a nullable column (#1973) or a
// cross-workspace author leaves reachable rows behind, and they are counted
// here. Additive manifest field: nil on bundles written before it existed and
// on scopes that do not run it (crew, instance).
type ScopeReconciliation struct {
	// Checked is true when the reconciliation ran for this bundle.
	Checked bool `json:"checked" yaml:"checked"`
	// SkipReason says why it did not run when Checked is false. A failure to
	// reconcile never fails the backup itself.
	SkipReason string `json:"skip_reason,omitempty" yaml:"skip_reason,omitempty"`
	// Tables is how many exported tables had a schema path to reconcile.
	Tables int `json:"tables" yaml:"tables"`
	// Shortfalls lists every table where the source held workspace rows the
	// dump did not export. Empty on a complete bundle.
	Shortfalls []ScopeShortfall `json:"shortfalls,omitempty" yaml:"shortfalls,omitempty"`
}

// ScopeShortfall is one table's gap between what the schema reaches and what
// the dump exported.
type ScopeShortfall struct {
	Table string `json:"table" yaml:"table"`
	// Reachable is how many source rows the schema ties to the workspace.
	Reachable int `json:"reachable" yaml:"reachable"`
	// Missing is how many of those the dump did not export.
	Missing int `json:"missing" yaml:"missing"`
}

// scopeHubTables are global tables a workspace bundle narrows on purpose:
// they are shared across workspaces, so a foreign key into an exported user
// or skill does not make the referencing row this workspace's (a skill
// authored by an exported user may belong to another workspace's agents).
// Rows of these tables are still checked as children; they just do not
// propagate reachability to others.
var scopeHubTables = map[string]bool{"users": true, "skills": true}

type dumpScopeFilter struct {
	where string
	args  []any
}

// reconcileWorkspaceScopeTx runs the independent derivation described on
// ScopeReconciliation against dump, inside the same transaction the dump was
// read in, so concurrent writes cannot appear as shortfalls. filters holds the
// WHERE clause each exported table was dumped with; a parent's filter is only
// ever used to name the parent rows, never to decide which children belong.
func reconcileWorkspaceScopeTx(ctx context.Context, tx *sql.Tx, workspaceID string, dump *DBDump, filters map[string]dumpScopeFilter) (*ScopeReconciliation, error) {
	out := &ScopeReconciliation{Checked: true}
	for _, table := range BackupTables {
		rows, dumped := dump.Tables[table]
		if !dumped || table == "workspaces" || !sqlIdentifierRe.MatchString(table) {
			continue
		}
		cols, pk, err := tableColumnsAndKey(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		if len(pk) == 0 {
			continue // no stable identity to compare rows by
		}
		var preds []string
		var args []any
		if cols["workspace_id"] {
			preds = append(preds, "workspace_id = ?")
			args = append(args, workspaceID)
		}
		fks, err := singleColumnFKs(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		for _, fk := range fks {
			parent, ok := filters[fk.parent]
			if !ok || scopeHubTables[fk.parent] || !sqlIdentifierRe.MatchString(fk.parent) ||
				!sqlIdentifierRe.MatchString(fk.from) || !sqlIdentifierRe.MatchString(fk.to) {
				continue
			}
			preds = append(preds, fmt.Sprintf("%s IN (SELECT %s FROM %s WHERE %s)",
				quoteIdent(fk.from), quoteIdent(fk.to), quoteIdent(fk.parent), parent.where))
			args = append(args, parent.args...)
		}
		if len(preds) == 0 {
			continue
		}
		out.Tables++
		have := make(map[string]bool, len(rows))
		for _, r := range rows {
			have[rowKey(pk, func(c string) any { return r[c] })] = true
		}
		quoted := make([]string, len(pk))
		for i, c := range pk {
			quoted[i] = quoteIdent(c)
		}
		q := fmt.Sprintf("SELECT %s FROM %s WHERE (%s)", strings.Join(quoted, ", "), quoteIdent(table), strings.Join(preds, ") OR ("))
		res, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("backup: reconcile scope of %s: %w", table, err)
		}
		reachable, missing := 0, 0
		for res.Next() {
			raw := make([]any, len(pk))
			ptrs := make([]any, len(pk))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := res.Scan(ptrs...); err != nil {
				_ = res.Close()
				return nil, fmt.Errorf("backup: reconcile scope of %s: %w", table, err)
			}
			reachable++
			vals := make(map[string]any, len(pk))
			for i, c := range pk {
				vals[c] = normalizeScan(raw[i])
			}
			if !have[rowKey(pk, func(c string) any { return vals[c] })] {
				missing++
			}
		}
		err = res.Err()
		_ = res.Close()
		if err != nil {
			return nil, fmt.Errorf("backup: reconcile scope of %s: %w", table, err)
		}
		if missing > 0 {
			out.Shortfalls = append(out.Shortfalls, ScopeShortfall{Table: table, Reachable: reachable, Missing: missing})
		}
	}
	return out, nil
}

// ScopeReconciliation returns DumpWorkspace's independent completeness
// derivation for this dump, or nil for dumps that do not run it.
func (d *DBDump) ScopeReconciliation() *ScopeReconciliation {
	if d == nil {
		return nil
	}
	return d.scope
}

// forTables returns the reconciliation restricted to tables still present in
// dump — a custom bundle drops whole categories after the dump is read, and a
// table it chose to leave out is not a shortfall.
func (r *ScopeReconciliation) forTables(dump *DBDump) *ScopeReconciliation {
	if r == nil || dump == nil {
		return r
	}
	out := &ScopeReconciliation{Checked: r.Checked, SkipReason: r.SkipReason, Tables: r.Tables}
	for _, s := range r.Shortfalls {
		if _, ok := dump.Tables[s.Table]; ok {
			out.Shortfalls = append(out.Shortfalls, s)
		}
	}
	return out
}

func rowKey(pk []string, get func(string) any) string {
	var b strings.Builder
	for _, c := range pk {
		fmt.Fprintf(&b, "%T:%v\x00", get(c), get(c))
	}
	return b.String()
}

// tableColumnsAndKey returns the column set and the primary-key columns in
// key order.
func tableColumnsAndKey(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, []string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, nil, fmt.Errorf("backup: table_info(%s): %w", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	type keyCol struct {
		name string
		pos  int
	}
	var keys []keyCol
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, nil, fmt.Errorf("backup: scan table_info(%s): %w", table, err)
		}
		cols[name] = true
		if pk > 0 {
			keys = append(keys, keyCol{name, pk})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].pos < keys[j].pos })
	pk := make([]string, len(keys))
	for i, k := range keys {
		pk[i] = k.name
	}
	return cols, pk, nil
}

type fkEdge struct{ from, parent, to string }

// singleColumnFKs lists the table's single-column foreign keys. Composite
// keys are skipped: none of the scoped tables reach a workspace through one.
func singleColumnFKs(ctx context.Context, tx *sql.Tx, table string) ([]fkEdge, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("backup: foreign_key_list(%s): %w", table, err)
	}
	defer rows.Close()
	byID := map[int][]fkEdge{}
	for rows.Next() {
		var (
			id, seq                   int
			parent, from              string
			to                        sql.NullString
			onUpdate, onDelete, match sql.NullString
		)
		if err := rows.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return nil, fmt.Errorf("backup: scan foreign_key_list(%s): %w", table, err)
		}
		target := to.String
		if target == "" {
			target = "id" // REFERENCES parent with no column names the parent's key
		}
		byID[id] = append(byID[id], fkEdge{from: from, parent: parent, to: target})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []fkEdge
	for _, edges := range byID {
		if len(edges) == 1 {
			out = append(out, edges[0])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out, nil
}
