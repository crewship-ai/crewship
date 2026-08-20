package backup

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// A workspace-scoped table must never be filtered through a NULLABLE foreign
// key, and this is the guard that makes that structural rather than remembered.
//
// DiscoverScopedTables walks reverse foreign keys and keeps the SHORTEST path
// to a workspace-scoped table. Nothing in that walk asks whether the column it
// chose can be NULL — and a filter on a nullable column silently omits every
// row where it is. Not "fails": omits. The backup succeeds, the bundle
// verifies, and the rows are simply not in it.
//
// That is not hypothetical. page_panel_data carries producer_run_id, nullable
// by design because a script, a container agent and an inbound webhook have no
// run; it is one hop from pipeline_runs and two from its own page, so the walk
// preferred it. A workspace whose pages were fed by cron backed up with ZERO
// payload rows and restored to pages reading never_produced.
//
// The fix is usually not an explicit case in workspaceFilterSQL but the walk
// itself: DiscoverScopedTables now runs a first pass restricted to NOT NULL
// foreign keys, so a longer total path beats a shorter lossy one, and a table
// only falls back to a nullable hop when no other hop exists. #1973 emptied
// the allowlist this test used to carry — it is now a guard with no
// exceptions, which is the only kind worth having.
func TestScopedFilters_NeverTraverseANullableFK(t *testing.T) {
	db := openMigratedDBCov(t)
	ctx := context.Background()

	scoped, err := DiscoverScopedTables(ctx, db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	var offenders []string
	for _, st := range scoped {
		// The walk's answer for a table is only ever USED when that table
		// rides bundles: DumpWorkspace reads it for BackupTables entries, and
		// ReplaceWorkspaceContents takes `include, _ :=` from
		// CategoriseScopedTables and discards the rest. For an excluded table
		// the filter is computed and thrown away, so its shape cannot lose a
		// row — nothing selects or deletes through it.
		//
		// Today that exempts exactly two: keeper_aux_settings
		// (IntentExcludeOperational — instance-global evaluator wiring, one row
		// per Keeper slot for the whole server, reached only through an
		// optional credential_id) and keeper_requests (IntentExcludeRuntime —
		// per-instance governance decisions, whose only FKs out are a nullable
		// credential_id and a nullable requesting_agent_id). Neither has any
		// NOT NULL route to a workspace to prefer, and neither needs one.
		//
		// This is a structural exemption, not an allowlist: flip either table
		// to IntentInclude and this guard starts failing on it the same day,
		// which is the day it would start losing rows.
		if BackupTableIntent[st.Name] != IntentInclude {
			continue
		}
		// A table with an explicit filter has already had this decision made
		// by a human; the walk's answer for it is not used.
		if _, _, handled := workspaceFilterSQL(st.Name, "ws"); handled {
			continue
		}
		filter, _ := st.WorkspaceScopeFilter("ws")
		col := leadingFilterColumn(filter)
		if col == "" {
			continue
		}
		// A nullable workspace_id filtered DIRECTLY is not this bug. There,
		// NULL means the row belongs to no workspace — a global template, an
		// instance-wide setting — and excluding it from a workspace bundle is
		// the correct answer, not a silent loss. What this test is about is an
		// INDIRECT path: a row that does belong to the workspace, reached
		// through a column that happens to be NULL for it.
		if col == "workspace_id" {
			continue
		}
		notNull, err := columnIsNotNull(ctx, db, st.Name, col)
		if err != nil {
			t.Fatalf("inspect %s.%s: %v", st.Name, col, err)
		}
		if !notNull {
			offenders = append(offenders, fmt.Sprintf(
				"  %s is scoped through NULLABLE %s.%s\n    filter: %s",
				st.Name, st.Name, col, filter))
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("these tables would silently omit every row whose scoping column is NULL:\n%s\n\n"+
			"Give the table a NOT NULL foreign key the walk can prefer, or an explicit case in "+
			"workspaceFilterSQL routing through a NOT NULL parent. Do NOT relax this test and do NOT "+
			"add an exception list: the failure it prevents is a backup that succeeds and restores "+
			"less than it was given.", strings.Join(offenders, "\n"))
	}
}

// leadingFilterColumn pulls the column name out of the `"col" IN (SELECT …)`
// shape WorkspaceScopeFilter produces. A filter of another shape returns "" and
// is skipped rather than guessed at.
func leadingFilterColumn(filter string) string {
	filter = strings.TrimSpace(filter)
	if !strings.HasPrefix(filter, `"`) {
		return ""
	}
	end := strings.Index(filter[1:], `"`)
	if end <= 0 {
		return ""
	}
	return filter[1 : end+1]
}

func columnIsNotNull(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return notNull == 1, nil
		}
	}
	return false, rows.Err()
}
