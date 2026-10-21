package api

import (
	"encoding/json"
	"errors"
	"io"
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

func (h *WorkspaceHandler) PreviewAccessPreset(w http.ResponseWriter, r *http.Request) {
	actor, user, workspace, ok := h.memberAccessTarget(w, r)
	if !ok {
		return
	}
	var selection access.PresetRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if d.Decode(&selection) != nil || d.Decode(new(any)) != io.EOF || selection.MembershipID != r.PathValue("memberId") || selection.Revision < 1 {
		replyError(w, 400, "Provide the current membership_id, revision and explicit preset selection")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	defer tx.Rollback()
	preview, err := (access.Store{DB: h.db}).PlanPresetIn(r.Context(), tx, actor, user, workspace, selection)
	if errors.Is(err, access.ErrPresetInvalid) {
		replyError(w, 400, "Invalid preset or reissue selection; target must already be restricted")
		return
	}
	if errors.Is(err, access.ErrConflict) {
		replyError(w, 409, "Member policy changed; request a fresh preview")
		return
	}
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	writeJSON(w, 200, preview)
}
