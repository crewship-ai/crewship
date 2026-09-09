package api

import (
	"context"
	"net/http"
)

type pageLeaseKey struct{}

func (h *PageHandler) pageLease(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if h.projectStore == nil || r.Context().Value(pageLeaseKey{}) == h.projectStore {
		return func() {}, true
	}
	release, err := h.projectStore.Lease(r.Context(), WorkspaceIDFromContext(r.Context()), false)
	if err != nil {
		h.logger.Warn("acquire Page storage lease", "error", err)
		replyError(w, 503, "Page storage is busy; try again shortly")
		return nil, false
	}
	*r = *r.WithContext(context.WithValue(r.Context(), pageLeaseKey{}, h.projectStore))
	return release, true
}
