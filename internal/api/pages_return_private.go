package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/ws"
)

// journalArtifactReturnedToPrivate records the narrowing direction of PX-11.
// The widening direction is artifact.private_output_published (workbook) and
// artifact.project_shared (HTML artifact project).
const journalArtifactReturnedToPrivate journal.EntryType = "artifact.returned_to_private"

const auditActionReturnedToPrivate = "artifact.returned_to_private"

// returnPrivateNotice is shown with every preview and confirmation: the server
// can stop future reads, never recall a copy someone already has.
const returnPrivateNotice = "copies already downloaded, exported or read cannot be recalled"

type pageReturnPrivateRequest struct {
	DryRun       bool   `json:"dry_run,omitempty"`
	Confirm      bool   `json:"confirm,omitempty"`
	PagesVersion *int64 `json:"pages_version"`
	ReviewToken  string `json:"review_token,omitempty"`
}

type pageReturnPrivateGrant struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label,omitempty"`
	Level       string `json:"level"`
}

type pageReturnPrivateLink struct {
	ID        string `json:"id"`
	ExpiresAt string `json:"expires_at"`
}

type pageReturnPrivateWire struct {
	DryRun             bool                     `json:"dry_run"`
	PageID             string                   `json:"page_id"`
	Page               string                   `json:"page"`
	Kind               string                   `json:"kind"`
	PagesVersion       int64                    `json:"pages_version"`
	FromFolder         *pageFolderReviewRef     `json:"from_folder"`
	Before             pageAccessImpactSnapshot `json:"before"`
	After              pageAccessImpactSnapshot `json:"after"`
	Changes            []pageAccessImpactChange `json:"changes"`
	RemovedGrants      []pageReturnPrivateGrant `json:"removed_grants"`
	RevokedPublicLinks []pageReturnPrivateLink  `json:"revoked_public_links"`
	RevokedMandates    int64                    `json:"revoked_mandates"`
	Notice             string                   `json:"notice"`
	Changed            bool                     `json:"changed"`
	ReviewToken        string                   `json:"review_token,omitempty"`
}

// returnPrivateKindSQL classifies the Page by the content that carries a
// private audience today. Anything else (Design, Slides, Mooring JSON,
// dashboards with panels, plain Pages) is ” and refused.
const returnPrivateKindSQL = `SELECT CASE
 WHEN EXISTS(SELECT 1 FROM page_panels WHERE page_id=p.id) THEN ''
 WHEN EXISTS(SELECT 1 FROM slides_heads WHERE page_id=p.id) THEN ''
 WHEN EXISTS(SELECT 1 FROM artifact_version_heads WHERE page_id=p.id AND workspace_id=p.workspace_id AND content_type='design') THEN ''
 WHEN EXISTS(SELECT 1 FROM craft_native_documents WHERE page_id=p.id AND workspace_id=p.workspace_id AND application_id='wordcraft') THEN 'word'
 WHEN EXISTS(SELECT 1 FROM craft_native_documents WHERE page_id=p.id AND workspace_id=p.workspace_id AND application_id='gridcraft') THEN 'sheet'
 WHEN EXISTS(SELECT 1 FROM gridcraft_heads WHERE page_id=p.id AND workspace_id=p.workspace_id) THEN 'workbook'
 WHEN EXISTS(SELECT 1 FROM documents WHERE page_id=p.id AND workspace_id=p.workspace_id) THEN 'document'
 WHEN EXISTS(SELECT 1 FROM artifact_version_heads WHERE page_id=p.id AND workspace_id=p.workspace_id AND content_type='artifact_project') THEN 'artifact_project'
 ELSE '' END FROM pages p WHERE p.id=? AND p.workspace_id=?`

// returnPrivateMandateTables are the agent mandates bound to one Page. Every
// live one is revoked by the return, the owner's included: a mandate was
// admitted under the shared audience and is not re-evaluated here.
var returnPrivateMandateTables = []string{"gridcraft_intents", "mooring_intents", "artifact_project_message_intents", "document_iteration_intents"}

const returnPrivateTypesRefusal = "unsupported_type: only Docs documents, workbooks, native Word and Sheet documents and HTML artifact projects have a private audience to return to"

// ReturnPageToPrivate is PX-11: the human owner returns a shared artifact to
// their personal space with the same Page ID. A dry run shows the accesses
// that end; the confirmation, bound to that review, atomically sets the
// private audience, leaves the folder, deletes every page grant, revokes every
// public link and every live agent mandate, and audits the change. The
// authority epoch advances through the canonical Page triggers. Grants are
// deleted, not retired, so a later share starts from nothing.
func (h *PageHandler) ReturnPageToPrivate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.catalogReviewViewer(w, r); !ok {
		return
	}
	var req pageReturnPrivateRequest
	if !decodePageCatalogReview(w, r, &req, "dry_run", "confirm", "pages_version", "review_token") {
		return
	}
	if req.PagesVersion == nil || *req.PagesVersion < 0 || *req.PagesVersion >= 9007199254740991 || (req.DryRun && (req.Confirm || req.ReviewToken != "")) || (!req.DryRun && (!req.Confirm || req.ReviewToken == "")) {
		replyError(w, 400, "pages_version and either dry_run or confirm with review_token are required")
		return
	}
	// Preview and mutation both read one current snapshot in the writer.
	r, read, err := h.startMetadataRead(r, true)
	if err != nil {
		replyInternalError(w, h.logger, "begin return to private", err)
		return
	}
	defer read.tx.Rollback()
	tx := read.tx
	ctx := r.Context()
	viewer, ok := h.catalogReviewViewer(w, r)
	if !ok {
		return
	}
	wsID := WorkspaceIDFromContext(ctx)
	rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, r.PathValue("slug"))
	if !ok {
		return
	}
	if rec.OwnerCrewID != "" || rec.OwnerUserID == "" {
		replyError(w, 409, "crew_owned: a crew-owned page names no person whose personal space it could return to")
		return
	}
	if rec.OwnerUserID != viewer.UserID {
		replyError(w, 403, "only the page's owner may return it to their personal space")
		return
	}
	if rec.PrivateAudience {
		replyError(w, 409, "already_private: this page already has a private audience")
		return
	}
	if rec.PagesVersion != *req.PagesVersion {
		replyError(w, 409, "pages_version changed; preview again")
		return
	}
	var kind string
	if err := tx.QueryRowContext(ctx, returnPrivateKindSQL, rec.ID, wsID).Scan(&kind); err != nil {
		replyInternalError(w, h.logger, "classify page for return to private", err)
		return
	}
	if kind == "" {
		replyError(w, 409, returnPrivateTypesRefusal)
		return
	}
	epoch, err := h.catalogReviewEpoch(r, rec.ID)
	if err != nil {
		replyError(w, 404, "page not found")
		return
	}
	before, err := h.catalogAccessSnapshot(ctx, wsID, rec, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "read access before return", err)
		return
	}
	afterRecord := *rec
	afterRecord.PrivateAudience = true
	afterRecord.FolderID = ""
	after, err := h.catalogAccessSnapshot(ctx, wsID, &afterRecord, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "read access after return", err)
		return
	}
	grants, err := readPageGrantSubject(ctx, tx, rec.ID, "", "")
	if err != nil {
		replyInternalError(w, h.logger, "read page grants for return", err)
		return
	}
	links, err := returnPrivateLiveLinks(ctx, tx, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read public links for return", err)
		return
	}
	mandates, err := returnPrivateLiveMandates(ctx, tx, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read mandates for return", err)
		return
	}
	out := pageReturnPrivateWire{DryRun: req.DryRun, PageID: rec.ID, Page: rec.Slug, Kind: kind, PagesVersion: rec.PagesVersion,
		Before: before, After: after, Changes: pageAccessChanges(before, after),
		RemovedGrants: make([]pageReturnPrivateGrant, 0, len(grants)), RevokedPublicLinks: links, RevokedMandates: mandates, Notice: returnPrivateNotice}
	for _, g := range grants {
		out.RemovedGrants = append(out.RemovedGrants, pageReturnPrivateGrant{g.SubjectType, g.SubjectID, h.grantSubjectLabel(ctx, wsID, g.SubjectType, g.SubjectID), g.Level})
	}
	if rec.FolderID != "" {
		previous, err := h.loadFolderByID(ctx, rec.FolderID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "read folder before return", err)
			return
		}
		if previous != nil {
			out.FromFolder = &pageFolderReviewRef{previous.ID, previous.Slug, previous.ACLVersion}
		}
	}
	reviewed := out
	reviewed.DryRun = false
	token, err := pageReviewToken(struct {
		Operation, Actor, Workspace string
		Page                        *pageRecord
		Epoch                       pageCatalogEpoch
		Grants                      []pageGrantSnapshot
		Impact                      pageReturnPrivateWire
	}{"return-private", viewer.UserID, wsID, rec, epoch, grants, reviewed})
	if err != nil {
		replyInternalError(w, h.logger, "prepare return review", err)
		return
	}
	var notify []func()
	var closed ws.WorkbookSessionClosures
	if req.DryRun {
		out.ReviewToken = token
	} else {
		if !validPageReviewToken(req.ReviewToken, token) {
			replyError(w, 409, "review is stale; preview again")
			return
		}
		notify, closed, err = h.commitReturnToPrivate(ctx, tx, wsID, UserFromContext(ctx), rec, kind, out, grants)
		if err != nil {
			if errors.Is(err, errReturnPrivateChanged) {
				replyError(w, 409, "page changed during return; preview again")
				return
			}
			replyInternalError(w, h.logger, "return page to private", err)
			return
		}
		out.PagesVersion++
		out.Changed = true
	}
	if !h.finishMetadataRead(w, r, read) {
		return
	}
	if out.Changed {
		for _, publish := range notify {
			publish()
		}
		h.publishReturnToPrivate(context.WithoutCancel(r.Context()), wsID, rec, &afterRecord, out, closed)
	}
	writeJSON(w, 200, out)
}

var errReturnPrivateChanged = errors.New("page changed during return to private")

func returnPrivateLiveLinks(ctx context.Context, tx *sql.Tx, page string) ([]pageReturnPrivateLink, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,expires_at FROM page_public_tokens WHERE page_id=? AND revoked_at IS NULL ORDER BY id`, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pageReturnPrivateLink{}
	for rows.Next() {
		var l pageReturnPrivateLink
		if err := rows.Scan(&l.ID, &l.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func returnPrivateLiveMandates(ctx context.Context, tx *sql.Tx, page string) (int64, error) {
	var total int64
	for _, table := range returnPrivateMandateTables {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE page_id=? AND revoked_at IS NULL`, page).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// commitReturnToPrivate performs the confirmed mutation inside the caller's
// writer transaction and checks its final state before the caller commits.
func (h *PageHandler) commitReturnToPrivate(ctx context.Context, tx *sql.Tx, wsID string, actor *AuthUser, rec *pageRecord, kind string, out pageReturnPrivateWire, grants []pageGrantSnapshot) ([]func(), ws.WorkbookSessionClosures, error) {
	if rec.HasWorkbook {
		// Serialize with workbook writers, as a workbook share does.
		if _, err := tx.ExecContext(ctx, `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
			return nil, nil, err
		}
	}
	stamp := tsformat.Format(time.Now())
	// One statement changes audience and placement; the canonical Page
	// triggers advance the workspace and Page authority epochs.
	res, err := tx.ExecContext(ctx, `UPDATE pages SET private_audience=1, folder_id=NULL, pages_version=pages_version+1, updated_at=?
 WHERE id=? AND workspace_id=? AND pages_version=? AND private_audience=0 AND owner_user_id=? AND owner_crew_id IS NULL`, stamp, rec.ID, wsID, rec.PagesVersion, actor.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, errReturnPrivateChanged
	}
	var notify []func()
	for _, g := range grants {
		if err := execOneRow(ctx, tx, `DELETE FROM page_grants WHERE page_id=? AND subject_type=? AND subject_id=? AND level=?`, rec.ID, g.SubjectType, g.SubjectID, g.Level); err != nil {
			return nil, nil, errReturnPrivateChanged
		}
		var panels []string
		if g.PanelIDs.Valid {
			_ = json.Unmarshal([]byte(g.PanelIDs.String), &panels)
		}
		previous := g
		entry := pageGrantChangeEntry(journal.EntryPageGrantRemoved, wsID, actor, rec, g.SubjectType, g.SubjectID, h.grantSubjectLabel(ctx, wsID, g.SubjectType, g.SubjectID), g.Level, panels)
		entry.ID = generateCUID()
		entry.Payload["previous_grant"], entry.Payload["grant"] = &previous, nil
		entry.Payload["reason"] = "returned_to_private"
		publish, err := appendAuthorizationAudit(ctx, tx, h.journal, entry)
		if err != nil {
			return nil, nil, err
		}
		notify = append(notify, publish)
	}
	for _, link := range out.RevokedPublicLinks {
		if err := execOneRow(ctx, tx, `UPDATE page_public_tokens SET revoked_at=? WHERE id=? AND page_id=? AND revoked_at IS NULL`, stamp, link.ID, rec.ID); err != nil {
			return nil, nil, errReturnPrivateChanged
		}
		publish, err := appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			WorkspaceID: wsID, Type: journalPageLinkRevoked, Severity: journal.SeverityNotice, ActorType: journal.ActorUser, ActorID: actor.ID,
			Summary: fmt.Sprintf("%s revoked a public link on page %s", actor.Email, rec.Slug),
			Payload: map[string]any{"page": rec.Slug, "page_id": rec.ID, "token_id": link.ID, "actor_user_id": actor.ID, "reason": "returned_to_private"},
		})
		if err != nil {
			return nil, nil, err
		}
		notify = append(notify, publish)
	}
	var revoked int64
	for _, table := range returnPrivateMandateTables {
		res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET revoked_at=? WHERE page_id=? AND revoked_at IS NULL`, stamp, rec.ID)
		if err != nil {
			return nil, nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		revoked += n
	}
	if revoked != out.RevokedMandates {
		return nil, nil, errReturnPrivateChanged
	}
	fromFolder := ""
	if out.FromFolder != nil {
		fromFolder = out.FromFolder.ID
	}
	// Identifiers and counts only: no name, content or removed subject.
	metadata, _ := json.Marshal(map[string]any{
		"owner_user_id": rec.OwnerUserID, "kind": kind, "audience_before": "shared", "audience_after": "private",
		"from_folder_id": fromFolder, "pages_version": rec.PagesVersion + 1, "removed_grants": len(grants),
		"revoked_public_links": len(out.RevokedPublicLinks), "revoked_mandates": revoked, "lost_access_subjects": len(out.Changes),
	})
	if err := execOneRow(ctx, tx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), wsID, actor.ID, auditActionReturnedToPrivate, "page", rec.ID, string(metadata), stamp); err != nil {
		return nil, nil, err
	}
	publish, err := appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
		WorkspaceID: wsID, Type: journalArtifactReturnedToPrivate, Severity: journal.SeverityNotice, ActorType: journal.ActorUser, ActorID: actor.ID,
		Summary: "Artifact returned to its owner's personal space",
		Payload: map[string]any{"page_id": rec.ID, "owner_user_id": rec.OwnerUserID, "kind": kind, "audience_before": "shared", "audience_after": "private",
			"from_folder_id": fromFolder, "removed_grants": len(grants), "revoked_public_links": len(out.RevokedPublicLinks), "revoked_mandates": revoked},
	})
	if err != nil {
		return nil, nil, err
	}
	notify = append(notify, publish)

	current, err := h.loadPage(ctx, wsID, "@"+rec.ID)
	if err != nil {
		return nil, nil, err
	}
	if !current.PrivateAudience || current.FolderID != "" || current.OwnerUserID != rec.OwnerUserID || current.OwnerCrewID != "" || current.PagesVersion != rec.PagesVersion+1 || current.Slug != rec.Slug {
		return nil, nil, errReturnPrivateChanged
	}
	var left int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM page_grants WHERE page_id=?)+(SELECT count(*) FROM page_public_tokens WHERE page_id=? AND revoked_at IS NULL)`, rec.ID, rec.ID).Scan(&left); err != nil {
		return nil, nil, err
	}
	if left != 0 {
		return nil, nil, errReturnPrivateChanged
	}
	if mandates, err := returnPrivateLiveMandates(ctx, tx, rec.ID); err != nil || mandates != 0 {
		return nil, nil, errors.Join(err, errReturnPrivateChanged)
	}
	// Close open workbook sessions of everyone the private audience excludes.
	closedSessions, err := h.closeRevokedWorkbookSessions(ctx, tx, wsID, current)
	if err != nil {
		return nil, nil, err
	}
	return notify, closedSessions, nil
}

// publishReturnToPrivate tells live clients after commit. Every former
// subject gets an access recheck; open workbook sessions were closed in the
// transaction. The workspace event carries nothing about a private Page.
func (h *PageHandler) publishReturnToPrivate(ctx context.Context, wsID string, rec, after *pageRecord, out pageReturnPrivateWire, closed ws.WorkbookSessionClosures) {
	users := []string{}
	seen := map[string]bool{rec.OwnerUserID: true}
	rows, err := h.db.QueryContext(ctx, `SELECT user_id FROM workspace_members WHERE workspace_id=?`, wsID)
	if err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil && !seen[id] {
				seen[id] = true
				users = append(users, id)
			}
		}
		rows.Close()
	}
	publishUserAccessRecheck(h.hub, h.logger, users, map[string]any{"page_id": rec.ID})
	if rec.HasWorkbook && h.hub != nil {
		h.hub.RecheckWorkbookAccess(ctx, rec.ID, closed)
	}
	if out.FromFolder != nil {
		h.publishFolderMembership(ctx, wsID, UserFromContext(ctx), after, "removed", out.FromFolder.Slug, "")
		return
	}
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{})
}
