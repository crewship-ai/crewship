package api

import (
	"github.com/crewship-ai/crewship/internal/groupchat"
	"net/http"
)

func (h *WorkspaceConversationsHandler) Continue(w http.ResponseWriter, r *http.Request) {
	ws, u, ok := h.identity(w, r)
	if !ok {
		return
	}
	var in groupchat.ContinueInput
	if !conversationBody(w, r, &in) {
		return
	}
	room, created, err := h.store.Continue(r.Context(), ws, u, r.PathValue("conversationId"), in)
	if err != nil {
		h.fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		h.invalidateAudience(r, ws, u, room.ID, "")
	}
	writeJSON(w, status, room)
}
