package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type restrictedPageInput struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}
type restrictedPageAction struct {
	Intent  string                    `json:"intent_hash"`
	Panel   string                    `json:"panel_id"`
	ID      string                    `json:"id"`
	Label   string                    `json:"label"`
	Inputs  []restrictedPageInput     `json:"inputs"`
	Confirm *pages.PanelActionConfirm `json:"confirm,omitempty"`
}
type restrictedPage struct {
	Slug        string                 `json:"slug"`
	Name        string                 `json:"name"`
	Publication int64                  `json:"publication,omitempty"`
	Actions     []restrictedPageAction `json:"actions"`
}

// RestrictedCatalog exposes only executable call declarations, with no panel
// producer payloads, draft sources, fixed params or input default values.
func (h *PageHandler) RestrictedCatalog(w http.ResponseWriter, req *http.Request) {
	user := UserFromContext(req.Context())
	restricted, err := restrictedActor(req, h.db)
	if err != nil || user == nil || !restricted {
		replyError(w, 404, "Pages unavailable")
		return
	}
	if h.restrictedWorkflow == nil || h.restrictedWorkflow() == nil {
		replyError(w, 503, "Page execution unavailable")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	viewer, err := h.loadViewer(req.Context(), workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load Page viewer", err)
		return
	}
	index, err := h.loadPageIndex(req.Context(), workspace, viewer, pagesInWorkspace(workspace))
	if err != nil {
		replyInternalError(w, h.logger, "load Page directory", err)
		return
	}
	result := []restrictedPage{}
	for _, candidate := range index.rows {
		rec := candidate.rec
		var raw string
		if err = h.db.QueryRowContext(req.Context(), `SELECT spec_json FROM pages WHERE id=? AND workspace_id=?`, rec.ID, workspace).Scan(&raw); err != nil {
			replyInternalError(w, h.logger, "load Page declaration", err)
			return
		}
		publication := int64(0)
		if rec.HasProject || rec.PublicationVersion > 0 {
			err = h.db.QueryRowContext(req.Context(), `SELECT l.version FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version WHERE l.page_id=? AND l.published=1 AND p.spec_json=?`, rec.ID, raw).Scan(&publication)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				replyInternalError(w, h.logger, "load Page publication", err)
				return
			}
		}
		var doc pages.Document
		if json.Unmarshal([]byte(raw), &doc) != nil {
			continue
		}
		entry := restrictedPage{Slug: rec.Slug, Name: rec.Name, Publication: publication, Actions: []restrictedPageAction{}}
		for _, panel := range doc.Spec.Panels {
			if _, _, err = h.resolvePanelForCaller(req.Context(), workspace, user.ID, rec.Slug, panel.ID); err != nil {
				continue
			}
			for i := range panel.Actions {
				action := &panel.Actions[i]
				if action.Kind != pages.ActionCall {
					continue
				}
				pipelineID, _, _, lookupErr := h.lookupActionRoutine(req.Context(), workspace, action.Routine)
				if lookupErr != nil {
					continue
				}
				authority := pipeline.PageActionInvocation{PageID: rec.ID, PanelID: panel.ID, ActionID: action.ID, PipelineID: pipelineID, ActionDigest: pipeline.PageActionDigest(action), Publication: publication}
				fingerprint, fingerprintErr := h.restrictedWorkflow().PageActionFingerprint(req.Context(), user.ID, workspace, authority)
				if fingerprintErr != nil {
					continue
				}
				wire := restrictedPageAction{Intent: fingerprint, Panel: panel.ID, ID: action.ID, Label: action.Label, Confirm: action.Confirm, Inputs: []restrictedPageInput{}}
				for _, input := range action.Inputs {
					wire.Inputs = append(wire.Inputs, restrictedPageInput{Name: input.Name, Label: input.Label, Type: input.EffectiveType(), Required: input.Required && input.Default == "", Options: input.Options})
				}
				entry.Actions = append(entry.Actions, wire)
			}
		}
		if len(entry.Actions) > 0 {
			result = append(result, entry)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(result)))
	writeJSON(w, http.StatusOK, result)
}
