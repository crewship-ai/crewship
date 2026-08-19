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
// The fix for such a table is an explicit case in workspaceFilterSQL routing
// through a NOT NULL parent. This test names the ones that must be listed
// there, so the next table with a convenient nullable shortcut fails here
// rather than in somebody's restore.
func TestScopedFilters_NeverTraverseANullableFK(t *testing.T) {
	db := openMigratedDBCov(t)
	ctx := context.Background()

	scoped, err := DiscoverScopedTables(ctx, db)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	// Tables that already had this shape before the guard existed. Each is a
	// real suspect, none is this branch's doing, and none is safe to "fix"
	// without understanding what its NULL means — which is exactly the work
	// #1973 is for. Listed rather than skipped silently, so the debt is
	// visible and a NEW table still fails.
	//
	// Do not add a row here to make a test pass. A new table with this shape
	// is a backup that loses rows; give it an explicit case in
	// workspaceFilterSQL instead.
	preExisting := map[string]string{
		"issue_counters":      "crew_id is NULL for workspace-level counters (#1973)",
		"keeper_aux_settings": "reached through an optional credential (#1973)",
		"keeper_requests":     "reached through an optional credential/agent (#1973)",
		"mission_tasks":       "assigned_agent_id is NULL until a task is claimed (#1973)",
		"crew_mcp_servers":    "workspace_mcp_server_id is NULL for crew-local servers (#1973)",
	}

	var offenders []string
	for _, st := range scoped {
		if _, known := preExisting[st.Name]; known {
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
			"Add an explicit case to workspaceFilterSQL routing each through a NOT NULL parent. "+
			"Do NOT relax this test: the failure it prevents is a backup that succeeds and "+
			"restores less than it was given.", strings.Join(offenders, "\n"))
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
