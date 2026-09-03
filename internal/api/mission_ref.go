package api

import (
	"context"
	"database/sql"
	"strings"
)

// resolveMissionRef turns what a person or a page has in hand — a mission id
// or an issue identifier such as ENG-4 — into the mission id the journal and
// runs filters bind to. The lookup is workspace-scoped, so one tenant's
// identifier never resolves to another's row.
//
// A reference that resolves to nothing is returned as typed, so it still
// filters on the id column: a typo matches nothing rather than silently
// widening to the whole workspace, and an id whose row is gone keeps
// pointing at its history. Empty in, empty out. A lookup error is treated
// the same way — the caller's filter stays as typed.
func resolveMissionRef(ctx context.Context, db *sql.DB, workspaceID, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || db == nil {
		return ref
	}
	var id string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM missions WHERE workspace_id = ? AND (id = ? OR identifier = ?) LIMIT 1`,
		workspaceID, ref, ref).Scan(&id)
	if err != nil {
		return ref
	}
	return id
}
