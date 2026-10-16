package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
)

var pageIdentityID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type pageIdentityWire struct {
	PageID string `json:"page_id"`
	Slug   string `json:"slug"`
}

// ResolvePageIdentity resolves an immutable Page identity to its current slug.
// Lookup and every reach path share one read snapshot. A slug hint never
// authorizes or retargets a link; hidden identities reveal no canonical slug.
func (h *PageHandler) ResolvePageIdentity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil {
		replyError(w, http.StatusUnauthorized, "human authentication required")
		return
	}
	id := r.PathValue("pageId")
	if !pageIdentityID.MatchString(id) {
		replyError(w, http.StatusBadRequest, "invalid page identity")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "begin page identity read", err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	if err := validateContentHumanCredential(r, tx, "pages:read"); err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, errContentCredentialScope) {
			status = http.StatusForbidden
		}
		replyError(w, status, "current human credential and pages:read scope required")
		return
	}
	workspace := WorkspaceIDFromContext(r.Context())
	role, err := h.liveContentHumanRole(r.Context(), workspace, user.ID)
	if err != nil {
		replyError(w, http.StatusForbidden, "active trusted workspace membership required")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), ctxRole, role))
	var slug string
	err = tx.QueryRowContext(r.Context(), `SELECT slug FROM pages WHERE workspace_id=? AND id=?`, workspace, id).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve page identity", err)
		return
	}
	rec, err := h.loadPage(r.Context(), workspace, slug)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && rec.ID != id) {
		replyError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load page identity", err)
		return
	}
	// Page reach uses panel ownership, without loading or returning panel data.
	panels, err := h.assetPagePanels(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load identity panel reach", err)
		return
	}
	viewer, err := h.loadViewer(r.Context(), workspace, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load identity viewer", err)
		return
	}
	reachable, err := h.canSeePage(r.Context(), workspace, rec, panels, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "resolve identity reach", err)
		return
	}
	if !reachable {
		replyError(w, http.StatusNotFound, "page not found")
		return
	}
	writeJSON(w, http.StatusOK, pageIdentityWire{PageID: rec.ID, Slug: rec.Slug})
}
