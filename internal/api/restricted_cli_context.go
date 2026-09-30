package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

// restrictedRunProfile reveals only an authenticated user's operation mode for
// this exact target. A denied target and a missing target have the same response.
func (r *Router) restrictedRunProfile(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	agent := req.PathValue("agentId")
	if user == nil || workspace == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	store := access.Store{DB: r.db}
	m, err := store.Membership(req.Context(), user.ID, workspace)
	var one int
	if err == nil {
		err = r.db.QueryRowContext(req.Context(), `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, agent, workspace).Scan(&one)
	}
	if err == nil && m.Mode == "restricted" {
		err = store.Check(req.Context(), user.ID, workspace, access.Right{Kind: "agent", ID: agent, Operation: "run"})
	}
	if err != nil {
		replyError(w, http.StatusNotFound, "execution unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": m.Mode})
}

// createRestrictedCLIContext is a run operation, not a chat grant. Its private
// audience, principal, agent and CLI origin are reconstructed by the host.
func (r *Router) createRestrictedCLIContext(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	agent := req.PathValue("agentId")
	if user == nil || workspace == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var body map[string]any
	req.Body = http.MaxBytesReader(w, req.Body, 256)
	decoder := json.NewDecoder(req.Body)
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body == nil || len(body) != 0 {
		replyError(w, http.StatusBadRequest, "empty context body required")
		return
	}
	id := generateCUID()
	result, err := r.db.ExecContext(req.Context(), `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,origin)
 SELECT ?,a.workspace_id,a.id,wm.user_id,'private','CLI' FROM agents a
 JOIN workspace_members wm ON wm.workspace_id=a.workspace_id AND wm.user_id=? AND wm.access_mode='restricted'
 JOIN workspaces w ON w.id=a.workspace_id AND w.deleted_at IS NULL
 JOIN access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation='run'
 WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL`, id, user.ID, agent, workspace)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "execution unavailable")
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		replyError(w, http.StatusNotFound, "execution unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}
