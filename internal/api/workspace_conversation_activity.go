package api

import (
	"github.com/crewship-ai/crewship/internal/groupchat"
	"net/http"
)

func (h *WorkspaceConversationsHandler) Activity(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	settings, err := h.store.Activity(r.Context(), ws, u, r.PathValue("conversationId"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
func (h *WorkspaceConversationsHandler) SetActivity(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var body struct {
		Issues   *bool `json:"issues"`
		Routines *bool `json:"routines"`
	}
	if !conversationBody(w, r, &body) {
		return
	}
	if body.Issues == nil || body.Routines == nil {
		replyError(w, http.StatusBadRequest, "issues and routines are required")
		return
	}
	in := groupchat.ActivitySettings{Issues: *body.Issues, Routines: *body.Routines}
	if err := h.store.SetActivity(r.Context(), ws, u, r.PathValue("conversationId"), in); err != nil {
		h.fail(w, err)
		return
	}
	h.invalidateAudience(r, ws, u, r.PathValue("conversationId"), "")
	writeJSON(w, http.StatusOK, in)
}
