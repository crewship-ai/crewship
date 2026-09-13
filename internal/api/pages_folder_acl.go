package api

// Pages folders — inherited permissions, F3′ of
// docs/prd/pages-folder-permissions-linux-model-2026-09-13.md (issue #2533).
//
// A folder has permissions `subject → can view / can edit` where the subject
// is a user, a crew, or everyone in the workspace, and they apply CONTINUOUSLY
// to every page that is in the folder right now (§1): filing a page in a
// shared folder shares it, taking it out unshares it, and a page's own grants
// can add to that but never take from it. Two rights, and the second implies
// the first (§3/2):
//
//	r  can view   see the folder, the pages in it, open them
//	w  can edit   rename the folder, its icon and colour; remove a page from
//	              it; edit the pages inside (`write`, with #2502's whole-
//	              document check); and NOT: move a page elsewhere, delete
//	              the folder, change the ACL, produce, or see a sealed panel.
//
// The Linux picture — directory, owning crew as owner, admin as root, crew as
// group, workspace as others — is a guide, not a specification. Two things
// differ on purpose: inheritance is live, not a default ACL copied at
// creation; and `w` on the directory edits the files in it, which POSIX does
// not do. A panel owned by another crew stays sealed, like a file with its
// own bits the directory cannot override (§1).
//
// THE ONE RULE THAT DIFFERS FROM PAGE GRANTS, stated once so it is not read
// as an omission: a folder ACL entry has NO issuer check (§3/5). A page grant
// is authority delegated by one human and is re-evaluated against that
// human's standing at every use (pages_grants_authz.go). A folder is an
// organisational object with a crew for an owner; its manager is
// interchangeable, and "the sharing vanished when Petr left" would be a
// surprise, not security. set_by_user_id is audit — ON DELETE SET NULL — and
// nothing in this file reads it to decide anything. The entries are loaded
// by loadFolderACLIn (all of a workspace, one statement) or loadFolderACL (one
// folder), never through loadPageGrantRecordsIn, and the two readers are kept
// apart so neither can inherit the other's rule by accident.
//
// Who may do what (§4), the table this file's refusals quote:
//
//	read the ACL             admin; the owning crew's MANAGER+
//	set / remove an entry    admin; the owning crew's MANAGER+
//	rename, icon, colour     admin; the owning crew's MANAGER+; a `w` holder
//	delete (empty) folder    admin; the owning crew's MANAGER+
//	add / move a page        page owner or admin INITIATES, and admin,
//	                         target crew's MANAGER+ or a `w` holder on the
//	                         target ACCEPTS — `w` alone never takes a page
//	remove a page            page owner; admin; a `w` holder on the folder —
//	                         deliberate: it takes the inherited access away
//	                         from everyone else, and the right's description
//	                         says so
//	edit a page inside       as before, plus a `w` holder (with #2502)
//	see the sharing label    whoever sees the folder: `shared` is
//	                         none|crew|workspace and carries no names
//
// Everyone who is not a manager sees only that label and their own effective
// paths (GET /pages/{slug}/access/me). Not even a 409 on a move returns the
// ACL to a caller who may not read it (§3/8, §3/10).
//
// Revocation boundary (§3/12): permissions are evaluated at the start of every
// request and never cached. A write that began with a valid `w` and lost it
// mid-flight completes; the next request is refused. A move and an ACL change
// serialise through acl_version: the ACL writes bump it in their transaction,
// and a move's UPDATE is fenced on it in the same statement, so of two
// interleaved requests one gets a 409.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// entryPageFolderACLChanged records an ACL entry set or removed. Payload:
// folder, folder_id, op (set|removed), subject_type, subject_id, subject,
// can_write, acl_version, actor_user_id.
const entryPageFolderACLChanged journal.EntryType = "page.folder_acl_changed"

// The sharing labels a folder carries for callers who may not read its ACL
// (§3/10). "crew" means "shared with named crews or people" — the label says
// that somebody outside the owning crew reaches the folder, not who.
const (
	pageFolderSharedNone      = "none"
	pageFolderSharedCrew      = "crew"
	pageFolderSharedWorkspace = "workspace"
)

// ── Records and wire ───────────────────────────────────────────────────────

// pageFolderACLRecord is one page_folder_acl row. There is no CanRead: it is
// always true, in the schema (CHECK can_read = 1) and therefore here.
type pageFolderACLRecord struct {
	FolderID    string
	SubjectType string
	SubjectID   string
	CanWrite    bool
	SetBy       string // "" once the setter's account is gone
	SetAt       string
}

type pageFolderACLWire struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label"`
	CanRead     bool   `json:"can_read"`
	CanWrite    bool   `json:"can_write"`
	SetBy       string `json:"set_by"`
	SetByUserID string `json:"set_by_user_id"`
	SetAt       string `json:"set_at"`
}

// pageFolderACLDocument is the ACL endpoints' envelope: the whole ACL after
// the change and the fence a move will carry, so the client that just
// changed the sharing has the number it needs to move a page next.
type pageFolderACLDocument struct {
	Folder     string              `json:"folder"`
	ACL        []pageFolderACLWire `json:"acl"`
	ACLVersion int64               `json:"acl_version"`
	Entry      *pageFolderACLWire  `json:"entry,omitempty"`
}

// pageFolderACLWriteRequest is PUT's body. CanRead is a pointer so a body that
// says `"can_read": false` can be told apart from one that says nothing —
// the first is a 400 (§3/2), the second is the ordinary case.
type pageFolderACLWriteRequest struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	CanRead     *bool  `json:"can_read"`
	CanWrite    bool   `json:"can_write"`
}

// ── Loading ────────────────────────────────────────────────────────────────

const pageFolderACLSelect = `
	SELECT a.folder_id, a.subject_type, a.subject_id, a.can_write,
	       COALESCE(a.set_by_user_id, ''), a.set_at
	FROM page_folder_acl a`

func scanFolderACL(rows *sql.Rows) ([]pageFolderACLRecord, error) {
	defer rows.Close()
	var out []pageFolderACLRecord
	for rows.Next() {
		var e pageFolderACLRecord
		var canWrite int
		if err := rows.Scan(&e.FolderID, &e.SubjectType, &e.SubjectID, &canWrite, &e.SetBy, &e.SetAt); err != nil {
			return nil, err
		}
		e.CanWrite = canWrite == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// loadFolderACLIn is every ACL entry on every folder in the workspace, keyed
// by folder id, in ONE statement — the index calls it once and looks each
// page's folder up in memory, so the permission check costs nothing per row.
func (h *PageHandler) loadFolderACLIn(ctx context.Context, wsID string) (map[string][]pageFolderACLRecord, error) {
	rows, err := h.db.QueryContext(ctx, pageFolderACLSelect+`
		JOIN page_folders f ON f.id = a.folder_id
		WHERE f.workspace_id = ?
		ORDER BY a.folder_id, a.subject_type, a.subject_id`, wsID)
	if err != nil {
		return nil, err
	}
	entries, err := scanFolderACL(rows)
	if err != nil {
		return nil, err
	}
	out := map[string][]pageFolderACLRecord{}
	for _, e := range entries {
		out[e.FolderID] = append(out[e.FolderID], e)
	}
	return out, nil
}

// loadFolderACL is the one-folder form.
func (h *PageHandler) loadFolderACL(ctx context.Context, folderID string) ([]pageFolderACLRecord, error) {
	rows, err := h.db.QueryContext(ctx, pageFolderACLSelect+`
		WHERE a.folder_id = ?
		ORDER BY a.subject_type, a.subject_id`, folderID)
	if err != nil {
		return nil, err
	}
	return scanFolderACL(rows)
}

// ── Verdicts ───────────────────────────────────────────────────────────────

// folderACLVerdict is what one viewer holds on one folder through its ACL:
// nothing, `r`, or `r` and `w`. write implies read by construction (§3/2).
type folderACLVerdict struct {
	read  bool
	write bool
}

// folderACLReach is the single place that decides whether an entry names a
// viewer: their own user entry, a crew of theirs, or the workspace entry for
// any human member. An agent's viewer matches none of them — agents are not a
// subject kind here (the CHECK), and "everyone in the workspace" is people.
func folderACLReach(entries []pageFolderACLRecord, viewer *pageViewer) folderACLVerdict {
	var v folderACLVerdict
	if viewer == nil {
		return v
	}
	for _, e := range entries {
		var mine bool
		switch e.SubjectType {
		case pageSubjectUser:
			mine = e.SubjectID != "" && e.SubjectID == viewer.UserID
		case pageSubjectCrew:
			mine = viewer.Crews[e.SubjectID]
		case pageSubjectWorkspace:
			mine = viewer.isWorkspaceMember()
		}
		if !mine {
			continue
		}
		v.read = true
		if e.CanWrite {
			v.write = true
		}
	}
	return v
}

// folderACLVerdictFor is the single-page form: the verdict on the folder this
// page is in, or nothing when the page is unfiled. One statement, only when
// filed.
func (h *PageHandler) folderACLVerdictFor(ctx context.Context, rec *pageRecord, viewer *pageViewer) (folderACLVerdict, error) {
	if rec == nil || rec.FolderID == "" || viewer == nil {
		return folderACLVerdict{}, nil
	}
	entries, err := h.loadFolderACL(ctx, rec.FolderID)
	if err != nil {
		return folderACLVerdict{}, err
	}
	return folderACLReach(entries, viewer), nil
}

// folderReachSlug is the index's folder arm: the folder's slug when its ACL
// names the viewer, "" otherwise. Everything it reads was loaded in bulk.
func folderReachSlug(rec *pageRecord, folders map[string]*pageFolderRecord, acl map[string][]pageFolderACLRecord, viewer *pageViewer) string {
	if rec.FolderID == "" {
		return ""
	}
	f := folders[rec.FolderID]
	if f == nil || !folderACLReach(acl[rec.FolderID], viewer).read {
		return ""
	}
	return f.Slug
}

// folderSharedLabel is the no-names rendering of an ACL (§3/10).
func folderSharedLabel(entries []pageFolderACLRecord) string {
	label := pageFolderSharedNone
	for _, e := range entries {
		if e.SubjectType == pageSubjectWorkspace {
			return pageFolderSharedWorkspace
		}
		label = pageFolderSharedCrew
	}
	return label
}

// mayArrangeFolder answers rename/icon/colour, removal of a page, and the
// ACCEPTING half of a move: a manager, or a holder of `w` on the folder.
func mayArrangeFolder(viewer *pageViewer, f *pageFolderRecord, acl []pageFolderACLRecord) bool {
	return mayAdministerFolder(viewer, f) || folderACLReach(acl, viewer).write
}

// ── Subjects ───────────────────────────────────────────────────────────────

// resolveFolderACLSubject turns the request's subject into the stored pair.
// A user by id or email (a member of this workspace), a crew by id or slug,
// the workspace by its type alone. An agent is refused before any lookup
// (§3/1): the refusal is a sentence, not a CHECK violation.
func (h *PageHandler) resolveFolderACLSubject(w http.ResponseWriter, r *http.Request, wsID, subjectType, ref string) (id, label string, ok bool) {
	switch subjectType {
	case pageSubjectWorkspace:
		return "", pageSubjectWorkspace, true
	case pageSubjectUser, pageSubjectCrew:
		if ref == "" {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("subject_id is required for a %s: the %s the entry is for", subjectType, subjectType))
			return "", "", false
		}
		return h.resolveGrantSubject(w, r, wsID, subjectType, ref)
	case pageSubjectAgent:
		replyError(w, http.StatusBadRequest,
			"a folder's permissions name people, crews or everyone in the workspace, never an agent; "+
				"an agent reaches a page only by a grant on that page (crewship page grant --agent)")
		return "", "", false
	default:
		replyError(w, http.StatusBadRequest, `subject_type must be "user", "crew" or "workspace"`)
		return "", "", false
	}
}

// folderACLSubjectLabel renders a stored subject the way a person typed it.
func (h *PageHandler) folderACLSubjectLabel(ctx context.Context, wsID, subjectType, subjectID string) string {
	if subjectType == pageSubjectWorkspace {
		return pageSubjectWorkspace
	}
	return h.grantSubjectLabel(ctx, wsID, subjectType, subjectID)
}

func (h *PageHandler) folderACLDocument(ctx context.Context, wsID string, f *pageFolderRecord, entries []pageFolderACLRecord, entry *pageFolderACLRecord) *pageFolderACLDocument {
	out := &pageFolderACLDocument{Folder: f.Slug, ACL: make([]pageFolderACLWire, 0, len(entries)), ACLVersion: f.ACLVersion}
	setters := map[string]string{}
	render := func(e pageFolderACLRecord) pageFolderACLWire {
		setBy := ""
		if e.SetBy != "" {
			label, seen := setters[e.SetBy]
			if !seen {
				label = h.grantSubjectLabel(ctx, wsID, pageSubjectUser, e.SetBy)
				setters[e.SetBy] = label
			}
			setBy = label
		}
		return pageFolderACLWire{
			SubjectType: e.SubjectType, SubjectID: e.SubjectID,
			Label:   h.folderACLSubjectLabel(ctx, wsID, e.SubjectType, e.SubjectID),
			CanRead: true, CanWrite: e.CanWrite,
			SetBy: setBy, SetByUserID: e.SetBy, SetAt: e.SetAt,
		}
	}
	for _, e := range entries {
		out.ACL = append(out.ACL, render(e))
	}
	if entry != nil {
		wire := render(*entry)
		out.Entry = &wire
	}
	return out
}

// folderACLOrNotFound is the ACL endpoints' gate in one place: the folder
// exists and the caller can see it (404 otherwise, the same posture as every
// folder read), and the caller is a manager (403 otherwise, with the rule).
func (h *PageHandler) folderACLOrNotFound(w http.ResponseWriter, r *http.Request, wsID string, viewer *pageViewer, verb string) (*pageFolderRecord, []pageFolderACLRecord, bool) {
	f, acl, ok := h.folderOrNotFound(w, r, wsID, viewer)
	if !ok {
		return nil, nil, false
	}
	if !mayAdministerFolder(viewer, f) {
		replyError(w, http.StatusForbidden, fmt.Sprintf(
			"%s the permissions of folder %q needs a workspace admin, or a MANAGER (or higher) who belongs to crew/%s, its owning crew; "+
				"everyone else sees the folder's sharing label and their own access (GET /pages/{slug}/access/me)",
			verb, f.Slug, f.OwnerCrewSlug))
		return nil, nil, false
	}
	return f, acl, true
}

// ── 1. Read — GET /api/v1/page-folders/{slug}/acl ──────────────────────────

func (h *PageHandler) GetFolderACL(w http.ResponseWriter, r *http.Request) {
	_, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, acl, ok := h.folderACLOrNotFound(w, r, wsID, viewer, "reading")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.folderACLDocument(r.Context(), wsID, f, acl, nil))
}

// ── 2. Set — PUT /api/v1/page-folders/{slug}/acl ───────────────────────────

// PutFolderACL sets one entry: can view, or can view and edit. PUT because
// the row is identified by (folder, subject) and re-running the same command
// must be the same state, not a second row. It bumps acl_version in the same
// transaction, so a move confirmed against the previous ACL is refused.
func (h *PageHandler) PutFolderACL(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, _, ok := h.folderACLOrNotFound(w, r, wsID, viewer, "changing")
	if !ok {
		return
	}
	body, ok := readCapped(w, r, pages.MaxSpecBytes, "folder permission request")
	if !ok {
		return
	}
	var req pageFolderACLWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.SubjectType = strings.ToLower(strings.TrimSpace(req.SubjectType))
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	if req.SubjectType == "" {
		replyError(w, http.StatusBadRequest, `subject_type is required: "user", "crew" or "workspace"`)
		return
	}
	// A1: there is no "can edit without can view". The server stores read
	// on every row; a body that tries to withhold it is refused rather than
	// silently corrected, because the client believed it said something.
	if req.CanRead != nil && !*req.CanRead {
		replyError(w, http.StatusBadRequest,
			"can_read cannot be false: an entry either exists (can view) or exists with can_write (can view and edit); "+
				"to take the subject's access away, delete the entry")
		return
	}
	subjectID, subjectRef, ok := h.resolveFolderACLSubject(w, r, wsID, req.SubjectType, req.SubjectID)
	if !ok {
		return
	}

	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	canWrite := 0
	if req.CanWrite {
		canWrite = 1
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin folder permission change", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO page_folder_acl (folder_id, subject_type, subject_id, can_read, can_write, set_by_user_id, set_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT (folder_id, subject_type, subject_id) DO UPDATE SET
			can_write      = excluded.can_write,
			set_by_user_id = excluded.set_by_user_id,
			set_at         = excluded.set_at`,
		f.ID, req.SubjectType, subjectID, canWrite, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "set folder permission", err)
		return
	}
	if err := bumpFolderACLVersion(r.Context(), tx, f, now); err != nil {
		replyInternalError(w, h.logger, "bump folder acl_version", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit folder permission change", err)
		return
	}
	entry := pageFolderACLRecord{FolderID: f.ID, SubjectType: req.SubjectType, SubjectID: subjectID, CanWrite: req.CanWrite, SetBy: user.ID, SetAt: now}
	h.journalFolderACL(r.Context(), wsID, user, f, "set", entry, subjectRef)
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})

	acl, err := h.loadFolderACL(r.Context(), f.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load folder permissions", err)
		return
	}
	writeJSON(w, http.StatusOK, h.folderACLDocument(r.Context(), wsID, f, acl, &entry))
}

// ── 3. Remove — DELETE /api/v1/page-folders/{slug}/acl/{subject_type}/{subject_id}

// DeleteFolderACL removes one entry. The workspace subject has no id of its
// own; its path spells the type twice (`.../acl/workspace/workspace`) because
// a path segment cannot be empty. A user or crew is matched by the stored id
// or by the reference a person types, so an entry for somebody who has since
// left can still be removed.
func (h *PageHandler) DeleteFolderACL(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, acl, ok := h.folderACLOrNotFound(w, r, wsID, viewer, "changing")
	if !ok {
		return
	}
	subjectType := strings.ToLower(strings.TrimSpace(r.PathValue("subject_type")))
	ref := strings.TrimSpace(r.PathValue("subject_id"))
	switch subjectType {
	case pageSubjectUser, pageSubjectCrew, pageSubjectWorkspace:
	case pageSubjectAgent:
		replyError(w, http.StatusBadRequest, "a folder's permissions never name an agent, so there is nothing to remove")
		return
	default:
		replyError(w, http.StatusBadRequest, `subject_type must be "user", "crew" or "workspace"`)
		return
	}
	resolvedID := ""
	if subjectType != pageSubjectWorkspace {
		id, _, err := h.resolveGrantSubjectQuietly(r.Context(), wsID, subjectType, ref)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "resolve folder permission subject", err)
			return
		}
		resolvedID = id
	}
	var found *pageFolderACLRecord
	for i := range acl {
		e := acl[i]
		if e.SubjectType != subjectType {
			continue
		}
		if subjectType == pageSubjectWorkspace || e.SubjectID == resolvedID || strings.EqualFold(e.SubjectID, ref) {
			found = &e
			break
		}
	}
	if found == nil {
		replyError(w, http.StatusNotFound, fmt.Sprintf("folder %q has no permission entry for %s/%s", f.Slug, subjectType, ref))
		return
	}
	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin folder permission removal", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM page_folder_acl WHERE folder_id = ? AND subject_type = ? AND subject_id = ?`,
		f.ID, found.SubjectType, found.SubjectID); err != nil {
		replyInternalError(w, h.logger, "remove folder permission", err)
		return
	}
	if err := bumpFolderACLVersion(r.Context(), tx, f, now); err != nil {
		replyInternalError(w, h.logger, "bump folder acl_version", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit folder permission removal", err)
		return
	}
	h.journalFolderACL(r.Context(), wsID, user, f, "removed", *found, h.folderACLSubjectLabel(r.Context(), wsID, found.SubjectType, found.SubjectID))
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})
	w.WriteHeader(http.StatusNoContent)
}

// bumpFolderACLVersion is the CAS half of §3/12: every ACL write moves the
// fence a move is confirmed against, inside the write's own transaction.
func bumpFolderACLVersion(ctx context.Context, tx *sql.Tx, f *pageFolderRecord, now string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE page_folders SET acl_version = acl_version + 1, updated_at = ? WHERE id = ?`, now, f.ID); err != nil {
		return err
	}
	f.ACLVersion++
	f.UpdatedAt = now
	return nil
}

// ── 4. Batch move — POST /api/v1/page-folders/{slug}/pages:batch ───────────

type pageFolderBatchMoveRequest struct {
	Pages []struct {
		Page         string `json:"page"`
		PagesVersion *int64 `json:"pages_version"`
	} `json:"pages"`
	ACLVersion *int64 `json:"acl_version"`
}

// BatchMoveFolderPages files several pages in this folder in ONE transaction:
// every page is checked on its own (reach, ownership, fence) and a single
// refusal writes nothing and names the page (§3/9). The checks run before
// the transaction, against the same rules as the single move; the writes are
// fenced UPDATEs inside it, so a page that moved between the check and the
// write fails its own fence and rolls the whole batch back.
func (h *PageHandler) BatchMoveFolderPages(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, ok := h.folderOnPath(w, r, wsID)
	if !ok {
		return
	}
	body, ok := readCapped(w, r, pages.MaxSpecBytes, "folder batch move request")
	if !ok {
		return
	}
	var req pageFolderBatchMoveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Pages) == 0 {
		replyError(w, http.StatusBadRequest, "pages is required: the pages to file here, each with its pages_version")
		return
	}
	if req.ACLVersion == nil {
		replyError(w, http.StatusBadRequest, "acl_version is required: a move is confirmed against the folder's permissions as you last read them")
		return
	}
	acl, err := h.loadFolderACL(r.Context(), f.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load folder permissions", err)
		return
	}
	if !mayArrangeFolder(viewer, f, acl) {
		replyError(w, http.StatusForbidden, folderAcceptRefusal(f))
		return
	}
	if f.ACLVersion != *req.ACLVersion {
		h.writeFolderFenceConflict(w, r, wsID, viewer, nil, f, acl, "acl_version")
		return
	}
	type move struct {
		rec  *pageRecord
		from string
	}
	var moves []move
	seen := map[string]bool{}
	for _, item := range req.Pages {
		slug := strings.TrimSpace(item.Page)
		if slug == "" {
			replyError(w, http.StatusBadRequest, "every entry in pages needs a page: the slug of the page to file here")
			return
		}
		if seen[slug] {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("page %q is listed twice", slug))
			return
		}
		seen[slug] = true
		if item.PagesVersion == nil {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("page %q has no pages_version; each page is confirmed against the version you last read", slug))
			return
		}
		rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, slug)
		if !ok {
			return
		}
		if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(viewer.Role, "manage") {
			// The page is named as a field as well as in the sentence: a client
			// moving several pages has to point at the one that was refused.
			writeJSON(w, http.StatusForbidden, map[string]any{
				"error": fmt.Sprintf("moving page %q is initiated by its owner or a workspace admin; you are neither, so nothing was moved", rec.Slug),
				"page":  rec.Slug,
			})
			return
		}
		if rec.PagesVersion != *item.PagesVersion {
			h.writeFolderFenceConflict(w, r, wsID, viewer, rec, f, acl, "pages_version")
			return
		}
		from := ""
		if rec.FolderID != "" && rec.FolderID != f.ID {
			if prev, err := h.loadFolderByID(r.Context(), rec.FolderID); err == nil {
				from = prev.Slug
			}
		}
		moves = append(moves, move{rec: rec, from: from})
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin batch move", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range moves {
		if m.rec.FolderID == f.ID {
			continue // already here: nothing changes, nothing bumps
		}
		res, err := tx.ExecContext(r.Context(), folderMoveStatement, f.ID, m.rec.ID, m.rec.PagesVersion, f.ID, f.ACLVersion)
		if err != nil {
			replyInternalError(w, h.logger, "move page into folder", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Raced between the check and the write. Nothing is committed;
			// the 409 names the page whose fence moved, or the folder's.
			_ = tx.Rollback()
			h.writeFolderFenceConflict(w, r, wsID, viewer, m.rec, f, acl, "")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit batch move", err)
		return
	}
	out := make([]pageListWire, 0, len(moves))
	for _, m := range moves {
		if m.rec.FolderID != f.ID {
			m.rec.FolderID = f.ID
			m.rec.PagesVersion++
			h.journalFolderMembership(r.Context(), wsID, user, m.rec, "moved", m.from, f.Slug)
			broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": m.rec.ID, "slug": m.rec.Slug})
		}
		index, err := h.loadPageIndex(r.Context(), wsID, viewer, pageOnly(m.rec.ID))
		if err != nil {
			replyInternalError(w, h.logger, "render moved page", err)
			return
		}
		if len(index.rows) == 1 {
			out = append(out, h.pageListRow(&index.rows[0], index.folders, viewer))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": out, "acl_version": f.ACLVersion})
}

// folderMoveStatement is the fenced UPDATE both the single move and the batch
// run: the page's pages_version AND the folder's acl_version, in one
// statement, so a move and an ACL change that interleave cannot both succeed
// (§3/12). Arguments: folder id, page id, pages_version, folder id,
// acl_version.
const folderMoveStatement = `
	UPDATE pages SET folder_id = ?, pages_version = pages_version + 1
	WHERE id = ? AND pages_version = ?
	  AND EXISTS (SELECT 1 FROM page_folders WHERE id = ? AND acl_version = ?)`

// ── 5. My access — GET /api/v1/pages/{slug}/access/me ──────────────────────

// pageAccessMeWire is the caller's own paths to one page, in pageReach's
// vocabulary (`owner`, `role`, `crew:<slug>`, `panel_crew:<slug>`,
// `folder:<slug>`, `grant`) — the same words the index sends on every row,
// so a client renders both with one function. It describes the caller and
// nobody else; the ACL and the effective-access report have their own gates.
type pageAccessMeWire struct {
	Page        string   `json:"page"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label"`
	Paths       []string `json:"paths"`
	Folder      string   `json:"folder,omitempty"`
	Shared      string   `json:"shared,omitempty"`
}

// MyPageAccess answers anyone who can see the page (§3/10). A page the caller
// cannot reach is the same 404 as one that does not exist.
func (h *PageHandler) MyPageAccess(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	rec, ok := h.grantPage(w, r, wsID)
	if !ok {
		return
	}
	viewer, err := h.loadViewer(r.Context(), wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page viewer", err)
		return
	}
	index, err := h.loadPageIndex(r.Context(), wsID, viewer, pageOnly(rec.ID))
	if err != nil {
		replyInternalError(w, h.logger, "resolve page reach", err)
		return
	}
	if len(index.rows) == 0 {
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", rec.Slug))
		return
	}
	row := &index.rows[0]
	out := pageAccessMeWire{
		Page: rec.Slug, SubjectType: pageSubjectUser, SubjectID: user.ID,
		Label: h.grantSubjectLabel(r.Context(), wsID, pageSubjectUser, user.ID),
		Paths: row.reach,
	}
	if f := index.folders[rec.FolderID]; f != nil {
		out.Folder = f.Slug
		out.Shared = folderSharedLabel(index.acl[f.ID])
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Journal ────────────────────────────────────────────────────────────────

func (h *PageHandler) journalFolderACL(ctx context.Context, wsID string, actor *AuthUser, f *pageFolderRecord, op string, e pageFolderACLRecord, subjectRef string) {
	if h.journal == nil {
		return
	}
	right := "can view"
	if e.CanWrite {
		right = "can view and edit"
	}
	subject := e.SubjectType + "/" + subjectRef
	if e.SubjectType == pageSubjectWorkspace {
		subject = "everyone in the workspace"
	}
	summary := fmt.Sprintf("%s set %s on page folder %s: %s", actor.Email, subject, f.Slug, right)
	if op == "removed" {
		summary = fmt.Sprintf("%s removed %s from page folder %s", actor.Email, subject, f.Slug)
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryPageFolderACLChanged,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actor.ID,
		Summary:     summary,
		Payload: map[string]any{
			"folder": f.Slug, "folder_id": f.ID, "op": op,
			"subject_type": e.SubjectType, "subject_id": e.SubjectID, "subject": subjectRef,
			"can_write": e.CanWrite, "acl_version": f.ACLVersion, "actor_user_id": actor.ID,
		},
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: folder permission change was not journalled", "folder", f.Slug, "op", op, "error", err)
	}
}
