package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/access"
)

func restrictedDirectory(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	restricted, err := (access.Store{DB: db}).HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	if !restricted {
		return false
	}
	workspace := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("agentId")
	rows, err := db.QueryContext(r.Context(), `SELECT a.id,a.name,a.slug,a.agent_role,EXISTS(SELECT 1 FROM access_grants cg WHERE cg.member_id=wm.id AND cg.agent_id=a.id AND cg.resource_kind='agent' AND cg.operation='chat') FROM agents a JOIN workspace_members wm ON wm.workspace_id=a.workspace_id AND wm.user_id=? JOIN access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation='discover' WHERE a.workspace_id=? AND a.deleted_at IS NULL AND (?='' OR a.id=?) AND (?='' OR a.name LIKE ? OR a.slug LIKE ?) ORDER BY a.slug LIMIT 500`, user.ID, workspace, id, id, r.URL.Query().Get("search"), "%"+r.URL.Query().Get("search")+"%", "%"+r.URL.Query().Get("search")+"%")
	if err != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, name, slug, role string
		var canChat bool
		if rows.Scan(&id, &name, &slug, &role, &canChat) != nil {
			replyError(w, http.StatusInternalServerError, "directory unavailable")
			return true
		}
		result = append(result, map[string]any{"id": id, "name": name, "slug": slug, "agent_role": role, "current_user_can_chat": canChat})
	}
	if rows.Err() != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	if id != "" {
		if len(result) != 1 {
			replyError(w, http.StatusNotFound, "Agent not found")
		} else {
			writeJSON(w, http.StatusOK, result[0])
		}
		return true
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(result)))
	writeJSON(w, http.StatusOK, result)
	return true
}
func (h *WorkspaceHandler) restrictedWorkspaceDirectory(w http.ResponseWriter, r *http.Request) bool {
	db := h.db
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	restricted, err := (access.Store{DB: db}).HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	if !restricted {
		return false
	}
	rows, err := db.QueryContext(r.Context(), `SELECT w.id,w.name,w.slug,wm.role,wm.access_mode FROM workspaces w JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=? WHERE w.deleted_at IS NULL ORDER BY w.slug`, user.ID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, name, slug, role, mode string
		if rows.Scan(&id, &name, &slug, &role, &mode) != nil {
			replyError(w, http.StatusInternalServerError, "directory unavailable")
			return true
		}
		result = append(result, map[string]any{"id": id, "name": name, "slug": slug, "current_user_role": role, "currentUserRole": role, "currentUserAccessMode": mode, "restricted_session": true})
	}
	if rows.Err() != nil {
		replyError(w, http.StatusInternalServerError, "directory unavailable")
		return true
	}
	rows.Close()
	visible := make([]map[string]any, 0, len(result))
	for _, row := range result {
		surfaces, e := h.memberRestrictedSurfaces(r.Context(), user.ID, row["id"].(string))
		if e != nil {
			// Driver/database infrastructure failures are global, even if a
			// different workspace happens not to touch the damaged table.
			var databaseError *sqlite.Error
			if errors.As(e, &databaseError) || errors.Is(e, r.Context().Err()) && r.Context().Err() != nil {
				replyError(w, 500, "directory unavailable")
				return true
			}
			if h.logger != nil {
				h.logger.Warn("omit workspace with unavailable navigation projection")
			}
			continue
		}
		row["restricted_surfaces"] = surfaces
		visible = append(visible, row)
	}
	if len(result) > 0 && len(visible) == 0 {
		replyError(w, 500, "directory unavailable")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, visible)
	return true
}
