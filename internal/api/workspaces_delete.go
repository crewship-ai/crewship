package api

// Workspace delete path (#866.2). Destructive, so it layers three guards
// on top of the route-level roleManage gate: OWNER-only, a re-typed slug
// confirmation, and a refusal to delete the caller's only workspace.

import (
	"database/sql"
	"net/http"
	"time"
)

type deleteWorkspaceRequest struct {
	// ConfirmSlug must equal the workspace's slug — the type-the-slug
	// confirmation that guards against accidental deletion.
	ConfirmSlug string `json:"confirm_slug"`
}

// Delete soft-deletes a workspace and cascade-soft-deletes its crews and
// agents, removing the crew/workspace membership rows.
//
// DELETE /api/v1/workspaces/{workspaceId}
//
// Guards (all before any mutation):
//   - OWNER-only. The route sits behind roleManage (ADMIN+) in the
//     router; the destructive blast radius warrants the stricter
//     in-handler gate so a workspace ADMIN cannot nuke it.
//   - confirm_slug in the body must match the workspace slug.
//   - refuse when this is the caller's only (non-deleted) workspace —
//     they'd be left with nowhere to land.
//
// Container teardown: soft-deleted crews stop serving dispatches
// immediately (every query filters deleted_at IS NULL); their runtime
// containers are reaped by the orchestrator's idle-TTL lifecycle
// (internal/orchestrator/orchestrator_lifecycle.go), the same path crew
// deletion relies on — there is no separate teardown queue to enqueue to.
func (h *WorkspaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblemContentType(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if role != "OWNER" {
		writeProblemContentType(w, r, http.StatusForbidden, "Only the workspace owner can delete a workspace")
		return
	}

	var req deleteWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		writeProblemContentType(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Load the live workspace so we can validate the typed slug against
	// the real value (and 404 a missing/already-deleted workspace).
	var slug string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT slug FROM workspaces WHERE id = ? AND deleted_at IS NULL", workspaceID).Scan(&slug)
	if err == sql.ErrNoRows {
		writeProblemContentType(w, r, http.StatusNotFound, "Workspace not found")
		return
	}
	if err != nil {
		h.logger.Error("load workspace for delete", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if req.ConfirmSlug != slug {
		writeProblemContentType(w, r, http.StatusBadRequest, "confirm_slug does not match the workspace slug")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// The DB opens transactions with _txlock=immediate
	// (internal/database/database.go), so BeginTx acquires the write lock
	// up front. Read the "only workspace" count INSIDE this tx so it is
	// serialized with the delete — otherwise two concurrent deletes could
	// both observe another workspace and both commit, leaving the owner
	// with none.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin delete tx", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	// Refuse deleting the caller's only workspace.
	var otherCount int
	if err := tx.QueryRowContext(r.Context(), `
		SELECT COUNT(*) FROM workspaces w
		JOIN workspace_members wm ON wm.workspace_id = w.id AND wm.user_id = ?
		WHERE w.deleted_at IS NULL AND w.id != ?
	`, user.ID, workspaceID).Scan(&otherCount); err != nil {
		h.logger.Error("count other workspaces", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if otherCount == 0 {
		writeProblemContentType(w, r, http.StatusConflict, "Cannot delete your only workspace")
		return
	}

	// Cascade order: children first, join rows, then the workspace itself.
	if _, err := tx.ExecContext(r.Context(),
		"UPDATE agents SET deleted_at = ? WHERE workspace_id = ? AND deleted_at IS NULL", now, workspaceID); err != nil {
		h.logger.Error("cascade soft-delete agents", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM crew_members WHERE crew_id IN (SELECT id FROM crews WHERE workspace_id = ?)", workspaceID); err != nil {
		h.logger.Error("cascade delete crew_members", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	// Snapshot the crew ids we're about to soft-delete so we can emit a
	// crew.deleted per crew after commit (parity with single-crew delete).
	// Fully drained + closed before the next Exec on this tx connection.
	var crewIDs []string
	crewRows, err := tx.QueryContext(r.Context(),
		"SELECT id FROM crews WHERE workspace_id = ? AND deleted_at IS NULL", workspaceID)
	if err != nil {
		h.logger.Error("snapshot crews for delete broadcast", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	for crewRows.Next() {
		var id string
		if err := crewRows.Scan(&id); err != nil {
			crewRows.Close()
			h.logger.Error("scan crew id for delete broadcast", "error", err)
			writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		crewIDs = append(crewIDs, id)
	}
	crewRows.Close()
	if err := crewRows.Err(); err != nil {
		h.logger.Error("iterate crews for delete broadcast", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		"UPDATE crews SET deleted_at = ? WHERE workspace_id = ? AND deleted_at IS NULL", now, workspaceID); err != nil {
		h.logger.Error("cascade soft-delete crews", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM workspace_members WHERE workspace_id = ?", workspaceID); err != nil {
		h.logger.Error("cascade delete workspace_members", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		"UPDATE workspaces SET deleted_at = ? WHERE id = ?", now, workspaceID); err != nil {
		h.logger.Error("soft-delete workspace", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit delete tx", "error", err)
		writeProblemContentType(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Realtime: tell connected clients the workspace (and its crews) are
	// gone so open settings / roster / crew views redirect or refresh
	// without a manual reload. Best-effort, post-commit — a nil hub or a
	// send failure never affects the delete outcome.
	for _, cid := range crewIDs {
		broadcastWorkspaceEvent(h.hub, workspaceID, "crew.deleted", map[string]string{"id": cid})
	}
	broadcastWorkspaceEvent(h.hub, workspaceID, "workspace.deleted", map[string]string{
		"id":   workspaceID,
		"slug": slug,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
