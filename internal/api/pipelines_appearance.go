package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// A routine's icon and colour.
//
// Separate from Save on purpose. Save rewrites definition_json,
// recomputes definition_hash, can mint a new version and re-runs the
// governance risk classifier — none of which should happen because
// somebody picked a different colour. This writes two columns.
//
// The values are opaque to the server: `icon` is a crew-icon name and
// `color` a gradient-palette id, both owned by the web UI's icon kit.
// Validating them here would mean the server holding a copy of that
// list and rejecting a routine every time the kit gained an icon, so it
// bounds the length and otherwise trusts the caller. The worst case is
// a routine that renders its fallback icon, which is what an unset
// value already does.

type appearanceBody struct {
	Icon  *string `json:"icon"`
	Color *string `json:"color"`
}

// maxAppearanceValue bounds each field. Icon names and palette ids are
// short slugs; anything longer is a client bug or an attempt to use the
// column as storage.
const maxAppearanceValue = 64

func (h *PipelineHandler) SetAppearance(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	const maxBody = 1 << 12
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		replyError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var body appearanceBody
	if err := json.Unmarshal(raw, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	// Absent fields keep what is stored; an explicit "" clears it. That
	// distinction is why the fields are pointers — without it there is
	// no way to say "leave the colour alone" and "remove the colour"
	// with the same shape.
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}

	icon := p.Icon
	if body.Icon != nil {
		icon = strings.TrimSpace(*body.Icon)
	}
	color := p.Color
	if body.Color != nil {
		color = strings.TrimSpace(*body.Color)
	}
	if len(icon) > maxAppearanceValue || len(color) > maxAppearanceValue {
		replyError(w, http.StatusBadRequest, "icon and color must be 64 characters or fewer")
		return
	}

	if err := h.store.SetAppearance(r.Context(), workspaceID, slug, icon, color); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		h.logger.Error("set appearance", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "failed to set appearance")
		return
	}

	updated, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if err != nil {
		// The write landed; failing the response here would have the
		// caller retry a change that already applied.
		writeJSON(w, http.StatusOK, map[string]string{"icon": icon, "color": color})
		return
	}
	writeJSON(w, http.StatusOK, toPipelineResponse(updated, false))
}
