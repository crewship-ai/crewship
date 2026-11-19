package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
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

// returnPrivateReason marks every row this operation writes elsewhere
// (journal, retired grant issuances), so a reader can tell them from an
// ordinary revoke.
const returnPrivateReason = "returned_to_private"

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
	// Retired grants carry no access today; they are deleted all the same so
	// nothing can revive them.
	Retired bool `json:"retired"`
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
	ReleasedEditLeases int64                    `json:"released_edit_leases"`
	// Reshare names the one route that shares this type again. Returning is
	// offered only for types that have one (see returnPrivateReshare).
	Reshare     string `json:"reshare"`
	Notice      string `json:"notice"`
	Changed     bool   `json:"changed"`
	ReviewToken string `json:"review_token,omitempty"`
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

// returnPrivateReshare is the share path that ends a private audience again,
// per supported kind. A kind with a private audience but no such path (Docs,
// native Word and Sheet: PX-10, #5617) is refused, because returning it would
// be one-way: the owner could never share it again.
var returnPrivateReshare = map[string]string{
	"workbook":         "POST /api/v1/pages/{slug}/workbook/editors with publish=true names an editor again; earlier grants, folder and links are not restored",
	"artifact_project": "POST /api/v1/pages/{slug}/artifact/share shares a version again; earlier grants, folder and links are not restored",
}

// returnPrivateMandateTables are the agent mandates bound to one Page. Every
// live one is revoked by the return, the owner's included: a mandate was
// admitted under the shared audience and is not re-evaluated here.
var returnPrivateMandateTables = []string{"gridcraft_intents", "mooring_intents", "artifact_project_message_intents", "document_iteration_intents"}

// returnPrivateLeaseFilter selects the edit lease state of anyone but the
// owner (a human as "user:<id>" or bare, an agent under its own actor).
const returnPrivateLeaseFilter = `page_id=? AND actor_id<>'' AND actor_id NOT IN (?, 'user:'||?)`

var errReturnPrivateChanged = errors.New("page changed during return to private")

// returnPrivateRefuse is every refusal of this endpoint: a stable machine
// reason next to the sentence.
func returnPrivateRefuse(w http.ResponseWriter, status int, reason, message string) {
	writeJSON(w, status, map[string]string{"error": reason + ": " + message, "reason": reason})
}

// ReturnPageToPrivate is PX-11: the human owner returns a shared artifact to
// their personal space with the same Page ID. A dry run shows the accesses
// that end; the confirmation, bound to that review, atomically sets the
// private audience, leaves the folder, deletes every page grant (recorded as
// a retired issuance so no restore revives it), revokes every public link and
// every live agent mandate, releases other actors' edit leases, and audits the
// change. The authority epoch advances through the canonical Page triggers.
// After commit, the Page channel subscriptions of former readers end at once.
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
		returnPrivateRefuse(w, 409, "crew_owned", "a crew-owned page names no person whose personal space it could return to")
		return
	}
	if rec.OwnerUserID != viewer.UserID {
		replyError(w, 403, "only the page's owner may return it to their personal space")
		return
	}
	if rec.PrivateAudience {
		returnPrivateRefuse(w, 409, "already_private", "this page already has a private audience")
		return
	}
	if rec.PagesVersion != *req.PagesVersion {
		returnPrivateRefuse(w, 409, "stale_version", "pages_version changed; preview again")
		return
	}
	var kind string
	if err := tx.QueryRowContext(ctx, returnPrivateKindSQL, rec.ID, wsID).Scan(&kind); err != nil {
		replyInternalError(w, h.logger, "classify page for return to private", err)
		return
	}
	switch kind {
	case "document", "word", "sheet":
		returnPrivateRefuse(w, 409, "no_reshare_path", "a "+kind+" page has no path to be shared again yet (PX-10, #5617), so returning it would be one-way; only workbooks and HTML artifact projects can be returned today")
		return
	case "":
		returnPrivateRefuse(w, 409, "unsupported_type", "only workbooks and HTML artifact projects can be returned to private; Design, Slides, Mooring JSON documents, dashboards and plain pages have no private audience")
		return
	}
	epoch, err := h.catalogReviewEpoch(r, rec.ID)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "page not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read authority epoch for return", err)
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
	leases, err := returnPrivateForeignLeases(ctx, tx, rec.ID, rec.OwnerUserID)
	if err != nil {
		replyInternalError(w, h.logger, "read edit leases for return", err)
		return
	}
	out := pageReturnPrivateWire{DryRun: req.DryRun, PageID: rec.ID, Page: rec.Slug, Kind: kind, PagesVersion: rec.PagesVersion,
		Before: before, After: after, Changes: pageAccessChanges(before, after),
		RemovedGrants: make([]pageReturnPrivateGrant, 0, len(grants)), RevokedPublicLinks: links, RevokedMandates: mandates,
		ReleasedEditLeases: leases, Reshare: returnPrivateReshare[kind], Notice: returnPrivateNotice}
	for _, g := range grants {
		out.RemovedGrants = append(out.RemovedGrants, pageReturnPrivateGrant{g.SubjectType, g.SubjectID, h.grantSubjectLabel(ctx, wsID, g.SubjectType, g.SubjectID), g.Level, g.RetiredAt.Valid})
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
			returnPrivateRefuse(w, 409, "stale_review", "review is stale; preview again")
			return
		}
		notify, closed, err = h.commitReturnToPrivate(ctx, tx, wsID, UserFromContext(ctx), rec, kind, out, grants)
		if errors.Is(err, errReturnPrivateChanged) {
			returnPrivateRefuse(w, 409, "changed", "page changed during return; preview again")
			return
		}
		if err != nil {
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

// returnPrivateForeignLeases counts the edit lease and lease requests held by
// anyone but the owner.
func returnPrivateForeignLeases(ctx context.Context, tx *sql.Tx, page, owner string) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM mooring_edit_leases WHERE `+returnPrivateLeaseFilter+`)+(SELECT count(*) FROM mooring_edit_lease_requests WHERE `+returnPrivateLeaseFilter+`)`, page, owner, owner, page, owner, owner).Scan(&n)
	return n, err
}

// returnPrivateExecOne runs a statement that must change exactly one row. A
// different count is a concurrent change (409); a database error stays one.
func returnPrivateExecOne(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errReturnPrivateChanged
	}
	return nil
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
	if err := returnPrivateExecOne(ctx, tx, `UPDATE pages SET private_audience=1, folder_id=NULL, pages_version=pages_version+1, updated_at=?
 WHERE id=? AND workspace_id=? AND pages_version=? AND private_audience=0 AND owner_user_id=? AND owner_crew_id IS NULL`, stamp, rec.ID, wsID, rec.PagesVersion, actor.ID); err != nil {
		return nil, nil, err
	}
	var notify []func()
	type receipt struct {
		id     string
		native []any
	}
	var receipts []receipt
	// Journal entries written here describe a Page that is private from this
	// transaction on: no slug, name, email or subject label, only identifiers.
	appendEntry := func(e journal.Entry) error {
		e.ID = generateCUID()
		publish, err := appendAuthorizationAudit(ctx, tx, h.journal, e)
		if err != nil {
			return err
		}
		native, err := pageGrantEventFrame(ctx, tx, e.ID)
		if err != nil {
			return err
		}
		notify = append(notify, publish)
		receipts = append(receipts, receipt{e.ID, native})
		return nil
	}
	for _, g := range grants {
		retiredAt := stamp
		if g.RetiredAt.Valid {
			retiredAt = g.RetiredAt.String
		}
		// Remember the issuance as retired, so a restored backup that still
		// carries the row meets the tombstone instead of reviving it.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO page_grant_retired_issuances(workspace_id,page_id,subject_type,subject_id,level,granted_by_user_id,granted_at,retired_at,reason) VALUES(?,?,?,?,?,?,?,?,?)`,
			wsID, rec.ID, g.SubjectType, g.SubjectID, g.Level, g.GrantedBy, g.GrantedAt, retiredAt, returnPrivateReason); err != nil {
			return nil, nil, err
		}
		if err := returnPrivateExecOne(ctx, tx, `DELETE FROM page_grants WHERE page_id=? AND subject_type=? AND subject_id=? AND level=?`, rec.ID, g.SubjectType, g.SubjectID, g.Level); err != nil {
			return nil, nil, err
		}
		previous := g
		if err := appendEntry(journal.Entry{
			WorkspaceID: wsID, Type: journal.EntryPageGrantRemoved, Severity: journal.SeverityInfo, ActorType: journal.ActorUser, ActorID: actor.ID,
			Summary: "Page grant removed: the page returned to its owner's private audience",
			Payload: map[string]any{"page_id": rec.ID, "subject_type": g.SubjectType, "subject_id": g.SubjectID, "level": g.Level,
				"actor_user_id": actor.ID, "reason": returnPrivateReason, "previous_grant": &previous, "grant": nil},
		}); err != nil {
			return nil, nil, err
		}
	}
	for _, link := range out.RevokedPublicLinks {
		if err := returnPrivateExecOne(ctx, tx, `UPDATE page_public_tokens SET revoked_at=? WHERE id=? AND page_id=? AND revoked_at IS NULL`, stamp, link.ID, rec.ID); err != nil {
			return nil, nil, err
		}
		if err := appendEntry(journal.Entry{
			WorkspaceID: wsID, Type: journalPageLinkRevoked, Severity: journal.SeverityNotice, ActorType: journal.ActorUser, ActorID: actor.ID,
			Summary: "Public link revoked: the page returned to its owner's private audience",
			Payload: map[string]any{"page_id": rec.ID, "token_id": link.ID, "actor_user_id": actor.ID, "reason": returnPrivateReason},
		}); err != nil {
			return nil, nil, err
		}
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
	// Other actors' edit lease state ends; the lease row keeps its generation
	// as the fence, exactly as an ordinary release does.
	res, err := tx.ExecContext(ctx, `DELETE FROM mooring_edit_lease_requests WHERE `+returnPrivateLeaseFilter, rec.ID, rec.OwnerUserID, rec.OwnerUserID)
	if err != nil {
		return nil, nil, err
	}
	released, err := res.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if res, err = tx.ExecContext(ctx, `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0 WHERE `+returnPrivateLeaseFilter, rec.ID, rec.OwnerUserID, rec.OwnerUserID); err != nil {
		return nil, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if released+n != out.ReleasedEditLeases {
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
		"revoked_public_links": len(out.RevokedPublicLinks), "revoked_mandates": revoked, "released_edit_leases": out.ReleasedEditLeases,
		"lost_access_subjects": len(out.Changes),
	})
	audit := [8]string{generateCUID(), wsID, actor.ID, auditActionReturnedToPrivate, "page", rec.ID, string(metadata), stamp}
	if err := returnPrivateExecOne(ctx, tx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, audit[0], audit[1], audit[2], audit[3], audit[4], audit[5], audit[6], audit[7]); err != nil {
		return nil, nil, err
	}
	if err := appendEntry(journal.Entry{
		WorkspaceID: wsID, Type: journalArtifactReturnedToPrivate, Severity: journal.SeverityNotice, ActorType: journal.ActorUser, ActorID: actor.ID,
		Summary: "Artifact returned to its owner's personal space",
		Payload: map[string]any{"page_id": rec.ID, "owner_user_id": rec.OwnerUserID, "kind": kind, "audience_before": "shared", "audience_after": "private",
			"from_folder_id": fromFolder, "removed_grants": len(grants), "revoked_public_links": len(out.RevokedPublicLinks), "revoked_mandates": revoked},
	}); err != nil {
		return nil, nil, err
	}

	// Close open workbook sessions of everyone the private audience excludes.
	current, err := h.loadPage(ctx, wsID, "@"+rec.ID)
	if err != nil {
		return nil, nil, err
	}
	closedSessions, err := h.closeRevokedWorkbookSessions(ctx, tx, wsID, current)
	if err != nil {
		return nil, nil, err
	}

	// Postconditions, after every write: a later trigger must not have
	// changed an earlier audit receipt or undone the mutation.
	for _, receipt := range receipts {
		native, err := pageGrantEventFrame(ctx, tx, receipt.id)
		if err != nil {
			return nil, nil, err
		}
		if !reflect.DeepEqual(native, receipt.native) {
			return nil, nil, errors.New("return to private audit did not survive unchanged")
		}
	}
	var stored [8]any
	if err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at FROM audit_logs WHERE id=?`, audit[0]).Scan(&stored[0], &stored[1], &stored[2], &stored[3], &stored[4], &stored[5], &stored[6], &stored[7]); err != nil {
		return nil, nil, err
	}
	if !privateAuditRowMatches(stored, audit) {
		return nil, nil, errors.New("return to private audit row did not survive unchanged")
	}
	if current, err = h.loadPage(ctx, wsID, "@"+rec.ID); err != nil {
		return nil, nil, err
	}
	if !current.PrivateAudience || current.FolderID != "" || current.OwnerUserID != rec.OwnerUserID || current.OwnerCrewID != "" || current.PagesVersion != rec.PagesVersion+1 || current.Slug != rec.Slug {
		return nil, nil, errReturnPrivateChanged
	}
	var left int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM page_grants WHERE page_id=? AND retired_at IS NULL)+(SELECT count(*) FROM page_public_tokens WHERE page_id=? AND revoked_at IS NULL)`, rec.ID, rec.ID).Scan(&left); err != nil {
		return nil, nil, err
	}
	if left != 0 {
		return nil, nil, errReturnPrivateChanged
	}
	mandates, err := returnPrivateLiveMandates(ctx, tx, rec.ID)
	if err != nil {
		return nil, nil, err
	}
	leases, err := returnPrivateForeignLeases(ctx, tx, rec.ID, rec.OwnerUserID)
	if err != nil {
		return nil, nil, err
	}
	if mandates != 0 || leases != 0 {
		return nil, nil, errReturnPrivateChanged
	}
	return notify, closedSessions, nil
}

// returnPrivateLostUsers are the people whose effective access the review
// showed changing: every member a grant, folder or crew reached appears as
// its own user row in the access snapshot. Users named by a removed grant are
// added in case the snapshot withheld them.
func returnPrivateLostUsers(out pageReturnPrivateWire, owner string) []string {
	users := []string{}
	seen := map[string]bool{owner: true, pageAccessWithheld: true, "": true}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			users = append(users, id)
		}
	}
	for _, c := range out.Changes {
		if c.SubjectType == pageSubjectUser {
			add(c.SubjectID)
		}
	}
	for _, g := range out.RemovedGrants {
		if g.SubjectType == pageSubjectUser {
			add(g.SubjectID)
		}
	}
	return users
}

// publishReturnToPrivate ends live access right after commit. Former
// readers' Page channel subscriptions (and HTTP run-stream observers) are
// re-checked and ended now, not on the 30 s sweep; a workbook also gets its
// editor-session closure frames. Each former reader then gets
// page.access_recheck on their own channel, sent directly rather than through
// the bounded fan-out queue that may drop it. The workspace event carries
// nothing about a private Page.
func (h *PageHandler) publishReturnToPrivate(ctx context.Context, wsID string, rec, after *pageRecord, out pageReturnPrivateWire, closed ws.WorkbookSessionClosures) {
	if h.hub != nil {
		if rec.HasWorkbook {
			h.hub.RecheckWorkbookAccess(ctx, rec.ID, closed)
		}
		h.hub.RevokePageSubscribers(ctx, rec.ID)
		sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		for _, user := range returnPrivateLostUsers(out, rec.OwnerUserID) {
			if err := h.hub.BroadcastChannelContext(sendCtx, "user", user, pageAccessRecheckEvent, map[string]any{"page_id": rec.ID}); err != nil {
				if h.logger != nil {
					h.logger.Warn("return to private: access recheck not delivered", "page_id", rec.ID, "error", err)
				}
				break
			}
		}
		cancel()
	}
	if out.FromFolder != nil {
		h.publishFolderMembership(ctx, wsID, UserFromContext(ctx), after, "removed", out.FromFolder.Slug, "")
		return
	}
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{})
}
