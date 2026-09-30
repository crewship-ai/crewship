package api

import (
	"net/http"
)

func (h *AgentHandler) restrictedProfileAuthorized(w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	ok, err := canEditAgent(r.Context(), h.db, user.ID, RoleFromContext(r.Context()), r.PathValue("agentId"))
	if err != nil || !ok {
		replyError(w, http.StatusForbidden, "agent edit permission required")
		return false
	}
	exists, err := agentExists(r.Context(), h.db, r.PathValue("agentId"), WorkspaceIDFromContext(r.Context()))
	if err != nil || !exists {
		replyError(w, http.StatusNotFound, "Agent not found")
		return false
	}
	return true
}
func (h *AgentHandler) GetRestrictedProfile(w http.ResponseWriter, r *http.Request) {
	if !h.restrictedProfileAuthorized(w, r) {
		return
	}
	var profile string
	if err := h.db.QueryRowContext(r.Context(), `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, r.PathValue("agentId"), WorkspaceIDFromContext(r.Context())).Scan(&profile); err != nil {
		replyError(w, http.StatusServiceUnavailable, "profile unavailable")
		return
	}
	writeJSON(w, http.StatusOK, restrictedAgentProfileResponse{Profile: profile})
}
func (h *AgentHandler) UpdateRestrictedProfile(w http.ResponseWriter, r *http.Request) {
	if !h.restrictedProfileAuthorized(w, r) {
		return
	}
	var body struct {
		Profile string `json:"profile"`
	}
	if err := readJSON(r, &body); err != nil || (body.Profile != "disabled" && body.Profile != "responses_text" && body.Profile != "native_api_key") {
		replyError(w, http.StatusBadRequest, "profile must be disabled, responses_text or native_api_key")
		return
	}
	// Credential/prompt/model policy remains separately enforced during admission.
	// Profile changes atomically revoke all currently bound provider attempts.
	if _, err := h.db.ExecContext(r.Context(), `UPDATE agents SET restricted_execution_profile=? WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, body.Profile, r.PathValue("agentId"), WorkspaceIDFromContext(r.Context())); err != nil {
		replyError(w, http.StatusServiceUnavailable, "profile unavailable")
		return
	}
	writeJSON(w, http.StatusOK, restrictedAgentProfileResponse{Profile: body.Profile})
}
