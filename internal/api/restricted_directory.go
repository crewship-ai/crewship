package api

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/access"
)

func restrictedDirectory(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	restricted, err := (access.Store{DB: db}).HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
		return true
	}
	if !restricted {
		return false
	}
	workspace := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("agentId")
	rows, err := db.QueryContext(r.Context(), `SELECT a.id,a.name,a.slug,a.agent_role FROM agents a JOIN workspace_members wm ON wm.workspace_id=a.workspace_id AND wm.user_id=? JOIN access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation='discover' WHERE a.workspace_id=? AND a.deleted_at IS NULL AND (?='' OR a.id=?) AND (?='' OR a.name LIKE ? OR a.slug LIKE ?) ORDER BY a.slug LIMIT 500`, user.ID, workspace, id, id, r.URL.Query().Get("search"), "%"+r.URL.Query().Get("search")+"%", "%"+r.URL.Query().Get("search")+"%")
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
		return true
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var id, name, slug, role string
		if rows.Scan(&id, &name, &slug, &role) != nil {
			replyError(w, http.StatusServiceUnavailable, "directory unavailable")
			return true
		}
		result = append(result, map[string]string{"id": id, "name": name, "slug": slug, "agent_role": role})
	}
	if rows.Err() != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
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
func restrictedWorkspaceDirectory(db *sql.DB, w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	restricted, err := (access.Store{DB: db}).HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
		return true
	}
	if !restricted {
		return false
	}
	rows, err := db.QueryContext(r.Context(), `SELECT w.id,w.name,w.slug,wm.role FROM workspaces w JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=? WHERE w.deleted_at IS NULL ORDER BY w.slug`, user.ID)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
		return true
	}
	defer rows.Close()
	result := []map[string]string{}
	for rows.Next() {
		var id, name, slug, role string
		if rows.Scan(&id, &name, &slug, &role) != nil {
			replyError(w, http.StatusServiceUnavailable, "directory unavailable")
			return true
		}
		result = append(result, map[string]string{"id": id, "name": name, "slug": slug, "current_user_role": role})
	}
	if rows.Err() != nil {
		replyError(w, http.StatusServiceUnavailable, "directory unavailable")
		return true
	}
	writeJSON(w, http.StatusOK, result)
	return true
}
