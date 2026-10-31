package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pages"
)

// PageLimitAdminHandler lets an instance administrator read and set the
// maximum number of pages a workspace may hold. Nothing here deletes a page.
type PageLimitAdminHandler struct {
	r *Router
}

type pageLimitView struct {
	MaxPagesPerWorkspace int `json:"max_pages_per_workspace"`
	DefaultValue         int `json:"default"`
	Min                  int `json:"min"`
	Max                  int `json:"max"`
}

func pageLimitOf(n int) pageLimitView {
	return pageLimitView{MaxPagesPerWorkspace: n, DefaultValue: pages.DefaultMaxPagesPerWorkspace, Min: pages.MinPagesPerWorkspaceLimit, Max: pages.MaxPagesPerWorkspaceLimit}
}

// Get is GET /api/v1/admin/instance/page-limit.
func (h *PageLimitAdminHandler) Get(w http.ResponseWriter, req *http.Request) {
	n, err := pages.Limit(req.Context(), h.r.db)
	if err != nil {
		internalError(w, req, h.r.logger, "load page limit", err)
		return
	}
	writeJSON(w, http.StatusOK, pageLimitOf(n))
}

// Put is PUT /api/v1/admin/instance/page-limit:
//
//	{ "max_pages_per_workspace": 250 }
//
// Lowering the cap below a workspace's current page count only refuses new
// pages there; existing pages are untouched.
func (h *PageLimitAdminHandler) Put(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Max *int `json:"max_pages_per_workspace"`
	}
	dec := json.NewDecoder(io.LimitReader(req.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ctx := req.Context()
	tx, err := h.r.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(w, req, h.r.logger, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	before, after, err := pages.SetLimit(ctx, tx, body.Max)
	if errors.Is(err, pages.ErrPageLimitValue) {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		internalError(w, req, h.r.logger, "set page limit", err)
		return
	}
	if err = auditInstance(ctx, req, tx, "instance.page_limit_set", "page_limit", "", "", map[string]any{
		"max_pages_per_workspace_from": before, "max_pages_per_workspace_to": after,
	}); err != nil {
		internalError(w, req, h.r.logger, "audit", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, req, h.r.logger, "commit", err)
		return
	}
	writeJSON(w, http.StatusOK, pageLimitOf(after))
}
