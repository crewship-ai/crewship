package api

import (
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type pageArchiveRequest struct {
	DryRun       bool   `json:"dry_run,omitempty"`
	Confirm      bool   `json:"confirm,omitempty"`
	PagesVersion *int64 `json:"pages_version"`
	ReviewToken  string `json:"review_token,omitempty"`
}

type pageArchiveState struct {
	Archived bool `json:"archived"`
}
type pageArchiveImpact struct {
	RemovedFromDefaultLists bool `json:"removed_from_default_lists"`
	AccessChanged           bool `json:"access_changed"`
	ContentDeleted          bool `json:"content_deleted"`
}
type pageArchiveWire struct {
	DryRun       bool              `json:"dry_run"`
	PageID       string            `json:"page_id"`
	Page         string            `json:"page"`
	PagesVersion int64             `json:"pages_version"`
	Before       pageArchiveState  `json:"before"`
	After        pageArchiveState  `json:"after"`
	Impact       pageArchiveImpact `json:"impact"`
	Changed      bool              `json:"changed"`
	ReviewToken  string            `json:"review_token,omitempty"`
}

// ArchivePage changes only catalog discovery state, never content or access.
func (h *PageHandler) ArchivePage(w http.ResponseWriter, r *http.Request) {
	h.setPageArchived(w, r, true)
}
func (h *PageHandler) UnarchivePage(w http.ResponseWriter, r *http.Request) {
	h.setPageArchived(w, r, false)
}

func (h *PageHandler) setPageArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	// Reject agent/internal credentials before beginning a writer or parsing input.
	if _, ok := h.catalogReviewViewer(w, r); !ok {
		return
	}
	var req pageArchiveRequest
	if !decodePageCatalogReview(w, r, &req, "dry_run", "confirm", "pages_version", "review_token") {
		return
	}
	if req.PagesVersion == nil || *req.PagesVersion < 0 || *req.PagesVersion >= 9007199254740991 || (req.DryRun && (req.Confirm || req.ReviewToken != "")) || (!req.DryRun && (!req.Confirm || req.ReviewToken == "")) {
		replyError(w, 400, "pages_version and either dry_run or confirm with review_token are required")
		return
	}
	// Preview and mutation both use one current, fenced authorization snapshot.
	r, read, err := h.startMetadataRead(r, true)
	if err != nil {
		replyInternalError(w, h.logger, "begin archive", err)
		return
	}
	defer read.tx.Rollback()
	viewer, ok := h.catalogReviewViewer(w, r)
	if !ok {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, r.PathValue("slug"))
	if !ok {
		return
	}
	if !h.mayAdministerGrants(r.Context(), wsID, viewer.UserID, viewer.Role, rec) || !workbookPageOwnerOnly(rec, viewer.UserID) {
		replyError(w, 403, "archiving is initiated by the page owner or an authorized workspace admin")
		return
	}
	if rec.PagesVersion != *req.PagesVersion {
		replyError(w, 409, "pages_version changed; preview again")
		return
	}
	epoch, err := h.catalogReviewEpoch(r, rec.ID)
	if err != nil {
		replyError(w, 404, "page not found")
		return
	}
	out := pageArchiveWire{DryRun: req.DryRun, PageID: rec.ID, Page: rec.Slug, PagesVersion: rec.PagesVersion, Before: pageArchiveState{Archived: rec.ArchivedAt != ""}, After: pageArchiveState{Archived: archived}, Impact: pageArchiveImpact{RemovedFromDefaultLists: archived}}
	token, err := pageReviewToken(struct {
		Actor, Workspace string
		Page             *pageRecord
		Epoch            pageCatalogEpoch
		Archived         bool
	}{viewer.UserID, wsID, rec, epoch, archived})
	if err != nil {
		replyInternalError(w, h.logger, "prepare archive review", err)
		return
	}
	if req.DryRun {
		out.ReviewToken = token
	} else {
		if !validPageReviewToken(req.ReviewToken, token) {
			replyError(w, 409, "review is stale; preview again")
			return
		}
		if out.Before.Archived != archived {
			var timestamp any
			if archived {
				timestamp = tsformat.Format(time.Now())
			}
			res, err := read.tx.ExecContext(r.Context(), `UPDATE pages SET archived_at=?,pages_version=pages_version+1 WHERE workspace_id=? AND id=? AND pages_version=?`, timestamp, wsID, rec.ID, rec.PagesVersion)
			if err != nil {
				replyInternalError(w, h.logger, "archive Page", err)
				return
			}
			if n, _ := res.RowsAffected(); n != 1 {
				replyError(w, 409, "page changed; preview again")
				return
			}
			current, err := h.loadPage(r.Context(), wsID, "@"+rec.ID)
			finalEpoch, eerr := h.catalogReviewEpoch(r, rec.ID)
			if err != nil || eerr != nil || current.PagesVersion != rec.PagesVersion+1 || (current.ArchivedAt != "") != archived || finalEpoch != epoch || !h.mayAdministerGrants(r.Context(), wsID, viewer.UserID, viewer.Role, current) {
				replyError(w, 409, "page authority changed; preview again")
				return
			}
			out.PagesVersion++
			out.Changed = true
		}
	}
	if !h.finishMetadataRead(w, r, read) {
		return
	}
	writeJSON(w, 200, out)
}
