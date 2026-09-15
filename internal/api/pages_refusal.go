package api

import (
	"fmt"
	"net/http"
)

// refusePageAction conceals an unreachable page after an action has already
// been denied. It must not gate successful actions: producer/agent authority
// need not imply human read authority. missing must match this route's 404.
func (h *PageHandler) refusePageAction(w http.ResponseWriter, r *http.Request, rec *pageRecord, forbidden, missing string) {
	h.refusePageResponse(w, r, rec, http.StatusForbidden, forbidden, missing)
}

func (h *PageHandler) refusePageResponse(w http.ResponseWriter, r *http.Request, rec *pageRecord, status int, forbidden, missing string) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, status, forbidden)
		return
	}
	ws := WorkspaceIDFromContext(r.Context())
	viewer, err := h.loadViewer(r.Context(), ws, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve denied page viewer", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), ws, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve denied page panels", err)
		return
	}
	reachable, err := h.canSeePage(r.Context(), ws, rec, panels, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "resolve denied page reach", err)
		return
	}
	if !reachable {
		if missing == "" {
			missing = fmt.Sprintf("page %q not found", rec.Slug)
		}
		replyError(w, http.StatusNotFound, missing)
		return
	}
	replyError(w, status, forbidden)
}
