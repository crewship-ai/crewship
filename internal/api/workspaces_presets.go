package api

import (
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

// Catalog and preview match the existing administrative policy GET credential
// boundary. Read-only does not supply a missing workspace administration scope.
func (h *WorkspaceHandler) GetAccessPresets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	caller := UserFromContext(r.Context())
	if caller == nil || !canScope(r.Context(), "workspace:admin") {
		replyError(w, 403, "Workspace administration required")
		return
	}
	workspace, ok := memberAccessWorkspace(r)
	if !ok {
		replyError(w, 404, "Preset catalog unavailable")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	defer tx.Rollback()
	catalog, err := (access.Store{DB: h.db}).PresetCatalogIn(r.Context(), tx, caller.ID, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"presets": catalog})
}
