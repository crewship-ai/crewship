package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// errFKNotInWorkspace is returned by assertFKInWorkspace when the referenced
// row is absent, soft-deleted, or belongs to a different workspace.
var errFKNotInWorkspace = errors.New("foreign key not found in workspace")

// fkScopeTables is the closed set of tables assertFKInWorkspace will probe.
// Restricting to a constant allowlist keeps the table-name interpolation below
// provably free of any user-controlled identifier (the SQL text is built only
// from these literals + parameterized id/workspace_id). ONLY tables that have
// BOTH a workspace_id and a deleted_at column belong here, because the query is
// hardcoded with `AND workspace_id = ? AND deleted_at IS NULL`. agents and
// crews qualify; projects/labels are workspace-scoped but have no deleted_at,
// and milestones are project-scoped (no workspace_id) — validating those needs
// a different query shape, so they are deliberately NOT listed (a call for them
// returns the unsupported-table error rather than a silent 500).
var fkScopeTables = map[string]struct{}{
	"agents": {},
	"crews":  {},
}

// assertFKInWorkspace verifies that row `id` in `table` exists, is not
// soft-deleted, and belongs to workspace wsID — the guard several mutating
// handlers must run before persisting a body-supplied foreign-key field
// (crew_id, label_id, project_id, assigned_agent_id, …). Without it a workspace
// member could persist a sibling-workspace id, which an unscoped read join then
// leaks back as foreign metadata, or which lands bad cross-tenant state (#1065,
// #1067). `table` is always a caller-provided constant, never user input.
//
// Returns nil when the row is in-workspace, errFKNotInWorkspace when it is
// absent/foreign (map to 400), or the underlying DB error otherwise (map to
// 500 — a transient failure is not an authorization decision).
func assertFKInWorkspace(ctx context.Context, db *sql.DB, table, id, wsID string) error {
	if id == "" || wsID == "" {
		return errFKNotInWorkspace
	}
	if _, ok := fkScopeTables[table]; !ok {
		return fmt.Errorf("assertFKInWorkspace: unsupported table %q", table)
	}
	var one int
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM "+table+" WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		id, wsID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errFKNotInWorkspace
	}
	return err
}
