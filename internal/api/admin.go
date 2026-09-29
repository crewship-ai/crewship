package api

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// AdminHandler provides owner-only administrative endpoints for workspace statistics and user management.
type AdminHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewAdminHandler creates an AdminHandler with the given database and logger.
func NewAdminHandler(db *sql.DB, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{db: db, logger: logger}
}

// Stats returns aggregate counts (workspaces, users, agents, running) for the current workspace.
// GET /api/v1/admin/stats — requires ADMIN+ (OWNER or ADMIN).
func (h *AdminHandler) Stats(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}

	type stats struct {
		Workspaces int `json:"workspaces"`
		Users      int `json:"users"`
		// Crews is what the licence caps first (max_crews), so the admin
		// overview reads it against that ceiling rather than as a bare count.
		Crews   int `json:"crews"`
		Agents  int `json:"agents"`
		Running int `json:"running"`
	}

	// Scope stats to the current workspace to prevent cross-workspace data leakage
	wsID := WorkspaceIDFromContext(r.Context())
	var s stats
	queries := []struct {
		sql  string
		args []any
		dest *int
	}{
		{"SELECT 1", nil, &s.Workspaces}, // always 1 (the current workspace)
		{"SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ?", []any{wsID}, &s.Users},
		{"SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", []any{wsID}, &s.Crews},
		{"SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL", []any{wsID}, &s.Agents},
		// Running = traces with run.started but no terminal entry yet.
		// The NOT EXISTS subquery is workspace-scoped so a terminal
		// emitted in another workspace can't suppress this workspace's
		// running count if trace_ids ever collide.
		{`SELECT COUNT(DISTINCT trace_id) FROM journal_entries je1
			WHERE je1.workspace_id = ? AND je1.entry_type = 'run.started'
			AND NOT EXISTS (
				SELECT 1 FROM journal_entries je2
				WHERE je2.workspace_id = je1.workspace_id
				AND je2.trace_id = je1.trace_id
				AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
			)`, []any{wsID}, &s.Running},
	}
	for _, q := range queries {
		if err := h.db.QueryRowContext(r.Context(), q.sql, q.args...).Scan(q.dest); err != nil {
			h.logger.Error("stats query", "sql", q.sql, "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	writeJSON(w, http.StatusOK, s)
}

// ListUsers and ListWorkspaces live in admin_people.go, with the per-user
// session and lockout actions that go with them.
