package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

func (r *Router) restrictedOutcomes(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	out, err := (access.Store{DB: r.db}).OutcomesForChat(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()), req.PathValue("chatId"))
	if err != nil {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
