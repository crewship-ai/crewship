package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// errFKNotInWorkspace is returned by assertFKInWorkspace when the referenced
// row is absent, soft-deleted, or belongs to a different workspace.
var errFKNotInWorkspace = errors.New("foreign key not found in workspace")

// fkScopeQueries is the closed set of references assertFKInWorkspace will
// probe, each mapped to the exact membership question for that table. Every
// query is a constant here and takes (id, workspace_id) as its two parameters,
// so no user-controlled identifier ever reaches the SQL text.
//
// The map replaced an allowlist of table *names* plus one hardcoded query
// shape. That shape was `AND workspace_id = ? AND deleted_at IS NULL`, which
// only agents and crews satisfy — so projects (workspace-scoped, no deleted_at)
// and milestones (project-scoped, no workspace_id at all) were documented as
// "deliberately NOT listed", and the handlers that needed them simply did not
// validate. The cross-workspace fence matrix then found six live leaks through
// exactly those columns. Carrying a per-table query costs one line each and
// removes the reason to leave a reference unchecked.
var fkScopeQueries = map[string]string{
	"agents": "SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
	"crews":  "SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
	// projects and labels are workspace-scoped but carry no deleted_at.
	"projects": "SELECT 1 FROM projects WHERE id = ? AND workspace_id = ?",
	"labels":   "SELECT 1 FROM labels WHERE id = ? AND workspace_id = ?",
	// milestones hang off a project; the tenant is reached through it.
	"milestones": `SELECT 1 FROM milestones m
	                 JOIN projects p ON p.id = m.project_id
	                WHERE m.id = ? AND p.workspace_id = ?`,
	// "users" asks the only question worth asking about a user id arriving in a
	// request body: is this person a member of the caller's workspace? A row in
	// `users` proves nothing — the users table is instance-wide.
	"users": "SELECT 1 FROM workspace_members WHERE user_id = ? AND workspace_id = ?",
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
//
// q is a rowQuerier (issue_handler.go) rather than a *sql.DB so a caller that
// already holds a transaction validates against the SAME snapshot it is about
// to INSERT into. issue_handler_create.go's neighbouring parent_issue_id and
// routine_id checks run on its tx; a project_id check reading h.db instead
// would be a different read view, which is the classic shape of a TOCTOU
// between the guard and the write it guards.
func assertFKInWorkspace(ctx context.Context, q rowQuerier, table, id, wsID string) error {
	if id == "" || wsID == "" {
		return errFKNotInWorkspace
	}
	query, ok := fkScopeQueries[table]
	if !ok {
		return fmt.Errorf("assertFKInWorkspace: unsupported table %q", table)
	}
	var one int
	err := q.QueryRowContext(ctx, query, id, wsID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return errFKNotInWorkspace
	}
	return err
}

// fkInWorkspaceOrReject is assertFKInWorkspace wired to an HTTP response: it
// returns true when the reference is in-workspace, and otherwise writes the
// right status (400 for a foreign/absent id, 500 for a database failure — a
// transient error is not an authorization decision) and returns false.
//
// It exists so a call site is one `if !… { return }`. The reason the six leaks
// the fence matrix found went unfixed for so long is that each one needed six
// lines of near-identical query + error mapping, and that is exactly the tax
// that gets skipped under deadline.
func fkInWorkspaceOrReject(w http.ResponseWriter, r *http.Request, q rowQuerier, logger *slog.Logger,
	table, field, id, wsID string) bool {
	if err := assertFKInWorkspace(r.Context(), q, table, id, wsID); err != nil {
		if errors.Is(err, errFKNotInWorkspace) {
			writeProblem(w, r, http.StatusBadRequest, field+" does not exist in this workspace")
			return false
		}
		internalError(w, r, logger, "validate "+field, err)
		return false
	}
	return true
}
