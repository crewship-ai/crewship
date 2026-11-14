package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/documents"
)

// Run-scope reach for a Sheet (GridCraft), a Mooring document and a Word
// document (Docs) (#5317 step 5.3, owner decision).
//
// With the flag on, a chat run reaches these kinds through its scope like any
// other artifact (artifactRunReach): attached to the message that started the
// run, created in this chat, or its exact-run consent; a Document also through
// the agent's standing grant or its crew's folder. A workbook and a Mooring
// document keep their own rule that agent grants, crew grants and folders
// never reach their content, so standing and folder reach do not apply to
// them. The kind's mandate or consent path is unchanged and stays the only way
// to change content.
//
// The scope gives a read principal only, derived from the run and checked on
// every use inside the storage transaction:
//
//   - the human must hold the kind's own ordinary read: a workbook's owner or
//     owner-named editor, a Mooring document's owner, a Document's reader as
//     an ordinary member. Access held only through the ADMIN or OWNER role
//     (the audited administrator read) never passes to the agent;
//   - authorizeScopedArtifactRun then checks the reach, intersects it with the
//     human as an ordinary member, counts standing and folder reads against
//     run_read_limit and records the read, failing closed;
//   - anything else is refused like a missing artifact.
//
// An agent change without a mandate or consent is refused: a Sheet or Mooring
// change answers proposal_required (no proposal delivery exists for them), a
// Document has no scope write at all (its proposal path needs an iteration
// propose mandate). Nothing reaches a lease or a head.

// workbookRunScopeMandate names the read principal of a run that reaches a
// workbook through its scope. It is not a stored mandate: the agent handler
// returns it only for a read, and its one target matches no cell.
const workbookRunScopeMandate = "run-scope-read"

// errMooringProposalRequired refuses an agent change to a Mooring document
// the run reaches only through its scope: Mooring has no proposal delivery.
var errMooringProposalRequired = errors.New("mooring proposal_required")

// kindScopedRunRead admits a flag-on run's read of rec through its scope, in
// tx. It returns errArtifactRefNotFound for anything the run does not reach.
// Without standing, only attached, created or consented reach counts: a
// workbook's or a Mooring document's own rule is that agent grants, crew
// grants and folders never reach its content.
func (h *PageHandler) kindScopedRunRead(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, rec *pageRecord, standing bool) error {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if a == nil || rec == nil || !h.artifactRunModesOn(ctx, a.WorkspaceID) {
		return errArtifactRefNotFound
	}
	if !standing {
		reach, _, err := h.artifactRunReach(ctx, a, rec)
		if err != nil {
			return err
		}
		if reach == runReachNone || readOnlyReach(reach) {
			return errArtifactRefNotFound
		}
	}
	_, err := h.authorizeScopedArtifactRun(withScopedRunKind(ctx, scopedRunKind{FamilyChecked: true}), tx, a, a.WorkspaceID, rec, false, false)
	return err
}

// workbookScopedRunPrincipal is the read principal of a flag-on run that
// reaches workbook k through its scope. The human must read the workbook as
// its owner or an owner-named editor; both read the head (a workbook has no
// shared version), an editor from the publication floor on.
func (h *PageHandler) workbookScopedRunPrincipal(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, k appengine.Key) (appengine.Principal, error) {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if a == nil || k.WorkspaceID != a.WorkspaceID {
		return appengine.Principal{}, appengine.ErrForbidden
	}
	rec, access, err := h.workbookHumanAccess(ctx, a.WorkspaceID, k.PageID, a.HumanUserID, false)
	if err == nil && access == workbookAuditor {
		// The audited administrator read is role-only reach; an ADMIN who is
		// also an owner-named editor still reads as that editor.
		rec, access, err = h.workbookHumanAccess(ctx, a.WorkspaceID, k.PageID, a.HumanUserID, true)
	}
	if err != nil || rec == nil || !rec.HasWorkbook || access != workbookOwner && access != workbookEditor {
		return appengine.Principal{}, appengine.ErrForbidden
	}
	if err = h.kindScopedRunRead(ctx, tx, a, rec, false); err != nil {
		if errors.Is(err, errArtifactRunCap) {
			return appengine.Principal{}, err
		}
		return appengine.Principal{}, appengine.ErrForbidden
	}
	p := appengine.Principal{AuthorityID: "agent:" + a.AgentID + ":run:" + a.RunID + ":scope", MandateID: workbookRunScopeMandate, Targets: []string{workbookRunScopeMandate}}
	if access == workbookEditor {
		if p.HistoryFloor, err = workbookPublicationFloor(ctx, tx, k); err != nil {
			return appengine.Principal{}, err
		}
	}
	return p, nil
}

// scopedRunChange answers an agent change without a mandate: like a missing
// artifact when the run does not reach it, and reached otherwise. It only
// checks access, records nothing and never commits.
func (h *PageHandler) scopedRunChange(ctx context.Context, reach func(context.Context, *sql.Tx) error, reached error) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if reach(artifactAuditProbeContext(ctx), tx) != nil {
		return appengine.ErrForbidden
	}
	return reached
}

// mooringScopedRunRead admits a flag-on run's read of a generic Mooring
// document through its scope. Mooring documents are private output: only
// their owner reads them as an ordinary member.
func (h *PageHandler) mooringScopedRunRead(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority, page string) error {
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if a == nil {
		return appengine.ErrForbidden
	}
	rec, access, err := h.workbookHumanAccess(ctx, a.WorkspaceID, page, a.HumanUserID, false)
	if err != nil || rec == nil || !rec.PrivateAudience || rec.HasWorkbook || access != workbookOwner {
		return appengine.ErrForbidden
	}
	var docs int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM mooring_documents WHERE workspace_id=? AND page_id=?`, a.WorkspaceID, page).Scan(&docs); err != nil || docs != 1 {
		return appengine.ErrForbidden
	}
	if err = h.kindScopedRunRead(ctx, tx, a, rec, false); err != nil {
		if errors.Is(err, errArtifactRunCap) {
			return err
		}
		return appengine.ErrForbidden
	}
	return nil
}

// scopedAgent serves a flag-on run's Mooring call without a mandate: a read
// through the run's scope, or the refusal of a change.
func (m *mooringHTTP) scopedAgent(w http.ResponseWriter, r *http.Request, a *artifactRunAuthority, packet mooringAgentPacket) {
	k := appengine.Key{WorkspaceID: a.WorkspaceID, PageID: packet.ArtifactID}
	pages := m.workbook.pages
	if packet.Action != "read" {
		mooringError(w, pages.scopedRunChange(r.Context(), func(ctx context.Context, tx *sql.Tx) error {
			return pages.mooringScopedRunRead(ctx, tx, a, k.PageID)
		}, errMooringProposalRequired))
		return
	}
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	if e = pages.mooringScopedRunRead(r.Context(), tx, a, k.PageID); e != nil {
		mooringError(w, e)
		return
	}
	manifest, state, e := m.document(r.Context(), tx, k)
	if e != nil {
		mooringError(w, e)
		return
	}
	// The read's audit row commits with the read, or the read fails.
	if e = tx.Commit(); e != nil {
		mooringError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"app_id": manifest.ID, "manifest": manifest, "state": state})
}

// documentScopedRunRead admits a flag-on run's read of a Document through its
// scope, in the transaction carried by ctx (pageAuthReaderKey). It returns the
// head the run reads: Docs have no shared version, and every reader the
// human's ordinary-member read admits reads the head.
func (h *PageHandler) documentScopedRunRead(ctx context.Context, a *artifactRunAuthority, page string) (documentDraftScope, error) {
	var scope documentDraftScope
	tx, _ := ctx.Value(pageAuthReaderKey{}).(*sql.Tx)
	if tx == nil || a == nil || !artifactRefID.MatchString(page) || !h.artifactRunModesOn(ctx, a.WorkspaceID) {
		return scope, errDocumentAccess
	}
	role, err := h.liveContentHumanRole(ctx, a.WorkspaceID, a.HumanUserID)
	if err != nil {
		return scope, errDocumentAccess
	}
	rec, err := h.loadPage(context.WithValue(ctx, ctxRole, role), a.WorkspaceID, "@"+page)
	if err != nil || rec.HasProject || rec.HasApplication || rec.HasWorkbook {
		return scope, errDocumentAccess
	}
	var panels int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM page_panels WHERE page_id=?`, rec.ID).Scan(&panels) != nil || panels != 0 {
		return scope, errDocumentAccess
	}
	if tx.QueryRowContext(ctx, `SELECT generation,head_rev,schema_version FROM documents WHERE workspace_id=? AND page_id=?`, a.WorkspaceID, rec.ID).Scan(&scope.Generation, &scope.Rev, &scope.Schema) != nil ||
		!documents.SchemaAllows(scope.Schema, documents.CapRead) {
		return scope, errDocumentAccess
	}
	if err = h.kindScopedRunRead(ctx, tx, a, rec, true); err != nil {
		if errors.Is(err, errArtifactRunCap) {
			return scope, err
		}
		return scope, errDocumentAccess
	}
	scope.PageID, scope.Read = rec.ID, true
	return scope, nil
}

// readDocumentRunScope is readDocumentDraftScope for a Document the run
// reaches through its scope: an access check under the writer fence that
// records nothing (the Node read's own state callback records the read).
func (h *PageHandler) readDocumentRunScope(r *http.Request, a *artifactRunAuthority, page string) (documentDraftScope, error) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return documentDraftScope{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return documentDraftScope{}, err
	}
	ctx := context.WithValue(artifactAuditProbeContext(r.Context()), pageAuthReaderKey{}, tx)
	return h.documentScopedRunRead(ctx, a, page)
}

// artifactRunCapRefusal answers a run past its per-run read limit.
func artifactRunCapRefusal(w http.ResponseWriter) {
	writeJSON(w, 429, map[string]any{"error": "This run reached its artifact limit. Ask the human to send a new message, or ask an admin to raise the limit.", "code": "artifact_run_limit"})
}
