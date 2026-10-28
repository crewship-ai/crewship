package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

type pageFolderReviewRef struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	ACLVersion int64  `json:"acl_version"`
}

type pageFolderReviewWire struct {
	DryRun       bool                     `json:"dry_run"`
	PageID       string                   `json:"page_id"`
	Page         string                   `json:"page"`
	PagesVersion int64                    `json:"pages_version"`
	FromFolder   *pageFolderReviewRef     `json:"from_folder"`
	ToFolder     pageFolderReviewRef      `json:"to_folder"`
	Before       pageAccessImpactSnapshot `json:"before"`
	After        pageAccessImpactSnapshot `json:"after"`
	Changes      []pageAccessImpactChange `json:"changes"`
	Changed      bool                     `json:"changed"`
	ReviewToken  string                   `json:"review_token,omitempty"`
}

func (h *PageHandler) reviewFolderMove(w http.ResponseWriter, r *http.Request, req *pageFolderMoveRequest) {
	if req.PagesVersion == nil || req.ACLVersion == nil || *req.PagesVersion < 0 || *req.PagesVersion >= 9007199254740991 || *req.ACLVersion < 0 || (req.DryRun && (req.Confirm || req.ReviewToken != "")) || (!req.DryRun && (!req.Confirm || req.ReviewToken == "")) {
		replyError(w, 400, "pages_version, acl_version and either dry_run or confirm with review_token are required")
		return
	}
	if _, ok := h.catalogReviewViewer(w, r); !ok {
		return
	}
	r, read, err := h.startMetadataRead(r, true)
	if err != nil {
		replyInternalError(w, h.logger, "begin reviewed folder move", err)
		return
	}
	defer read.tx.Rollback()
	viewer, ok := h.catalogReviewViewer(w, r)
	if !ok {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	f, acl, ok := h.folderOrNotFound(w, r, wsID, viewer)
	if !ok {
		return
	}
	rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, req.Page)
	if !ok {
		return
	}
	if (!h.isPageOwner(r.Context(), wsID, viewer.UserID, rec) && !canRole(viewer.Role, "manage")) || !workbookPageOwnerOnly(rec, viewer.UserID) || !mayArrangeFolder(viewer, f, acl) {
		replyError(w, 403, "moving requires Page control and target folder permission")
		return
	}
	if folderFenceConflict(rec, f, *req.PagesVersion, *req.ACLVersion) != "" {
		replyError(w, 409, "page or folder permissions changed; preview again")
		return
	}
	epoch, err := h.catalogReviewEpoch(r, rec.ID)
	if err != nil {
		replyError(w, 404, "page not found")
		return
	}
	before, err := h.catalogAccessSnapshot(r.Context(), wsID, rec, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "read access before move", err)
		return
	}
	afterRecord := *rec
	afterRecord.FolderID = f.ID
	after, err := h.catalogAccessSnapshot(r.Context(), wsID, &afterRecord, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "read access after move", err)
		return
	}
	out := pageFolderReviewWire{DryRun: req.DryRun, PageID: rec.ID, Page: rec.Slug, PagesVersion: rec.PagesVersion, ToFolder: pageFolderReviewRef{f.ID, f.Slug, f.ACLVersion}, Before: before, After: after, Changes: pageAccessChanges(before, after)}
	if rec.FolderID != "" {
		previous, err := h.loadFolderByID(r.Context(), rec.FolderID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "read previous folder", err)
			return
		}
		if previous != nil {
			out.FromFolder = &pageFolderReviewRef{previous.ID, previous.Slug, previous.ACLVersion}
		}
	}
	// Hash the complete host result before viewer-safe withholding. Hidden ACL
	// membership is never returned merely because the caller may file a Page.
	reviewed := out
	reviewed.DryRun = false
	token, err := pageReviewToken(struct {
		Actor, Workspace string
		Page             *pageRecord
		Epoch            pageCatalogEpoch
		Impact           pageFolderReviewWire
	}{viewer.UserID, wsID, rec, epoch, reviewed})
	if err != nil {
		replyInternalError(w, h.logger, "prepare folder review", err)
		return
	}
	if !mayAdministerFolder(viewer, f) {
		out.Before = pageAccessImpactSnapshot{Subjects: []pageAccessSubjectWire{}, Withheld: true}
		out.After = pageAccessImpactSnapshot{Subjects: []pageAccessSubjectWire{}, Withheld: true}
		out.Changes = []pageAccessImpactChange{}
	}
	// DryRun is response metadata, not part of the reviewed mutation identity.
	if req.DryRun {
		out.ReviewToken = token
	} else {
		if !validPageReviewToken(req.ReviewToken, token) {
			replyError(w, 409, "review is stale; preview again")
			return
		}
		if rec.FolderID != f.ID {
			expectedEpoch, valid := epochAfterFolderMove(epoch)
			if !valid {
				replyError(w, 409, "authority epoch unavailable")
				return
			}
			res, err := read.tx.ExecContext(r.Context(), folderMoveStatement, f.ID, rec.ID, rec.PagesVersion, f.ID, f.ACLVersion, viewer.UserID)
			if err != nil {
				replyInternalError(w, h.logger, "confirmed folder move", err)
				return
			}
			if n, _ := res.RowsAffected(); n != 1 {
				replyError(w, 409, "page or folder changed; preview again")
				return
			}
			current, err := h.loadPage(r.Context(), wsID, "@"+rec.ID)
			if err != nil || current.FolderID != f.ID || current.PagesVersion != rec.PagesVersion+1 || current.OwnerUserID != rec.OwnerUserID || current.OwnerCrewID != rec.OwnerCrewID || current.PrivateAudience != rec.PrivateAudience {
				replyError(w, 409, "page changed during move")
				return
			}
			currentViewer, ok := h.catalogReviewViewer(w, r)
			if !ok {
				return
			}
			finalEpoch, err := h.catalogReviewEpoch(r, rec.ID)
			if err != nil || finalEpoch != expectedEpoch {
				replyError(w, 409, "authority changed during move; preview again")
				return
			}
			currentFolder, currentACL, ok := h.folderOrNotFound(w, r, wsID, currentViewer)
			if !ok {
				return
			}
			if currentFolder.ID != f.ID || currentFolder.ACLVersion != f.ACLVersion || !mayArrangeFolder(currentViewer, currentFolder, currentACL) || (!h.isPageOwner(r.Context(), wsID, currentViewer.UserID, current) && !canRole(currentViewer.Role, "manage")) {
				replyError(w, 409, "folder authority changed during move")
				return
			}
			actual, err := h.catalogAccessSnapshot(r.Context(), wsID, current, currentViewer)
			expectedJSON, _ := json.Marshal(after)
			actualJSON, _ := json.Marshal(actual)
			if err != nil || string(actualJSON) != string(expectedJSON) {
				replyError(w, 409, "access impact changed during move")
				return
			}
			out.PagesVersion++
			out.Changed = true
		}
	}
	if !h.finishMetadataRead(w, r, read) {
		return
	}
	if out.Changed {
		from := ""
		if out.FromFolder != nil {
			from = out.FromFolder.Slug
		}
		afterRecord.PagesVersion = out.PagesVersion
		h.publishFolderMembership(r.Context(), wsID, UserFromContext(r.Context()), &afterRecord, "moved", from, f.Slug)
	}
	writeJSON(w, 200, out)
}
