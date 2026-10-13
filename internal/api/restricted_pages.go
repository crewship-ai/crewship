package api

import (
	"context"
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
		replyRestrictedRuntimeUnavailable(w)
		return
	}
	result, err := h.restrictedPageCatalog(req.Context(), user.ID, WorkspaceIDFromContext(req.Context()), h.restrictedWorkflow().PageActionFingerprint)
	if err != nil {
		replyInternalError(w, h.logger, "load Page directory", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Total-Count", strconv.Itoa(len(result)))
	writeJSON(w, http.StatusOK, result)
}

// restrictedPageCatalog is shared by executable catalogs and runtime-independent
// navigation authority. It never invokes actions or grants execution authority.
func (h *PageHandler) restrictedPageCatalog(ctx context.Context, user, workspace string, fingerprint func(context.Context, string, string, pipeline.PageActionInvocation) (string, error), firstOnly ...bool) ([]restrictedPage, error) {
	viewer, err := h.loadViewer(ctx, workspace, user)
	if err != nil {
		return nil, err
	}
	index, err := h.loadPageIndex(ctx, workspace, viewer, pagesInWorkspace(workspace))
	if err != nil {
		return nil, err
	}
	result := []restrictedPage{}
	for _, candidate := range index.rows {
		rec := candidate.rec
		var raw string
		if err = h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id=? AND workspace_id=?`, rec.ID, workspace).Scan(&raw); err != nil {
			return nil, err
		}
		publication := int64(0)
		if rec.HasProject || rec.PublicationVersion > 0 {
			err = h.db.QueryRowContext(ctx, `SELECT l.version FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version WHERE l.page_id=? AND l.published=1 AND p.spec_json=?`, rec.ID, raw).Scan(&publication)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		var doc pages.Document
		if json.Unmarshal([]byte(raw), &doc) != nil {
			continue
		}
		entry := restrictedPage{Slug: rec.Slug, Name: rec.Name, Publication: publication, Actions: []restrictedPageAction{}}
		for _, panel := range doc.Spec.Panels {
			if _, _, err = h.resolvePanelForCaller(ctx, workspace, user, rec.Slug, panel.ID); err != nil {
				continue
			}
			for i := range panel.Actions {
				action := &panel.Actions[i]
				if action.Kind != pages.ActionCall {
					continue
				}
				pipelineID, _, _, lookupErr := h.lookupActionRoutine(ctx, workspace, action.Routine)
				if lookupErr != nil {
					continue
				}
				authority := pipeline.PageActionInvocation{PageID: rec.ID, PanelID: panel.ID, ActionID: action.ID, PipelineID: pipelineID, ActionDigest: pipeline.PageActionDigest(action), Publication: publication}
				fingerprint, fingerprintErr := fingerprint(ctx, user, workspace, authority)
				if fingerprintErr != nil {
					continue
				}
				wire := restrictedPageAction{Intent: fingerprint, Panel: panel.ID, ID: action.ID, Label: action.Label, Confirm: action.Confirm, Inputs: []restrictedPageInput{}}
				for _, input := range action.Inputs {
					wire.Inputs = append(wire.Inputs, restrictedPageInput{Name: input.Name, Label: input.Label, Type: input.EffectiveType(), Required: input.Required && input.Default == "", Options: input.Options})
				}
				entry.Actions = append(entry.Actions, wire)
				if len(firstOnly) > 0 && firstOnly[0] {
					return append(result, entry), nil
				}
			}
		}
		if len(entry.Actions) > 0 {
			result = append(result, entry)
		}
	}

	return result, nil
}
