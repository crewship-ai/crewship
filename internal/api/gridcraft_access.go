package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// workbookAccess is how a human reaches workbook content.
type workbookAccess int

const (
	workbookNoAccess workbookAccess = iota
	workbookOwner                   // the human owner of the Page
	workbookEditor                  // a member the owner explicitly named as editor
	workbookAuditor                 // workspace OWNER: audited read, never write
)

// errWorkbookProposalRequired refuses direct agent access to shared workbooks.
// Shared changes require proposals; unavailable proposal delivery never grants DIRECT.
var errWorkbookProposalRequired = errors.New("proposal_required")

// agentSharedWorkbook reports whether the run's human owns the published
// workbook k, so a change only a proposal could make is refused as such.
func (h *gridcraftHTTP) agentSharedWorkbook(ctx context.Context, a *artifactRunAuthority, k appengine.Key) bool {
	var shared bool
	err := h.pages.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages p JOIN gridcraft_heads g ON g.page_id=p.id AND g.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=? AND p.owner_user_id=? AND p.private_audience=0)`, k.PageID, k.WorkspaceID, a.HumanUserID).Scan(&shared)
	return err == nil && shared
}

// workbookHumanAccess is the single human authorization rule for workbook
// content. Import, read, status and any later human edit path call it inside
// their own transaction (pageAuthReaderKey), so a revoked grant stops the
// next operation, including a second check after an engine candidate.
//
// A private workbook keeps the private-output audience: the owner reads and
// writes, a workspace OWNER reads with an audit. After the owner explicitly
// publishes it (gridcraftHTTP.share), the owner and every live, trusted member
// holding a user `write` grant issued by the current owner may read and write;
// a workspace OWNER still only reads, audited. Workspace roles, folder ACLs,
// crew/agent/workspace grants, `read` grants and grants issued by anybody but
// the owner do not reach workbook content.
func (h *PageHandler) workbookHumanAccess(ctx context.Context, workspace, pageID, userID string, write bool) (*pageRecord, workbookAccess, error) {
	viewer, err := h.livePrivatePageReader(ctx, workspace, userID)
	if err != nil || viewer == nil {
		return nil, workbookNoAccess, appengine.ErrForbidden
	}
	ctx = context.WithValue(ctx, ctxRole, viewer.Role)
	rec, err := h.loadPageForRead(ctx, workspace, "@"+pageID)
	if err != nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.FolderID != "" || rec.HasDocument || rec.HasProject || rec.HasApplication {
		return nil, workbookNoAccess, appengine.ErrForbidden
	}
	if rec.PrivateAudience {
		if write {
			if rec.OwnerUserID != userID || !h.mayEditSpec(ctx, workspace, userID, viewer.Role, rec) {
				return nil, workbookNoAccess, appengine.ErrForbidden
			}
			return rec, workbookOwner, nil
		}
		if !privatePageViewer(rec, viewer) {
			return nil, workbookNoAccess, appengine.ErrForbidden
		}
		if rec.OwnerUserID == userID {
			return rec, workbookOwner, nil
		}
		return rec, workbookAuditor, nil
	}
	// Published: a restricted (Guest) membership carries an empty role here.
	if viewer.Role == "" {
		return nil, workbookNoAccess, appengine.ErrForbidden
	}
	if rec.OwnerUserID == userID {
		return rec, workbookOwner, nil
	}
	if _, err := h.liveContentHumanRole(ctx, workspace, rec.OwnerUserID); err == nil {
		var editor bool
		if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_grants WHERE page_id=? AND subject_type='user' AND subject_id=? AND level='write' AND granted_by_user_id=?)`, rec.ID, userID, rec.OwnerUserID).Scan(&editor) == nil && editor {
			return rec, workbookEditor, nil
		}
	}
	if !write && viewer.Role == "OWNER" {
		return rec, workbookAuditor, nil
	}
	return nil, workbookNoAccess, appengine.ErrForbidden
}

// workbookPageRefusal is shown to anyone but the owner who tries to delete,
// rename, roll back or read the spec history of a Page holding a workbook.
const workbookPageRefusal = "only the workbook owner may delete, rename, roll back or read the history of this page; workbooks have no backup or archive"

// workbookFolderRefusal is shown to anyone but the owner who tries to move a
// Page holding a workbook into or out of a folder. A workbook in a folder is
// unreachable for everyone, so filing it is a lifecycle decision.
const workbookFolderRefusal = "only the workbook owner may move this page into or out of a folder"

// workbookPageOwnerOnly keeps the lifecycle and metadata history of a Page
// holding a workbook with its owner, before and after publication. Workbooks
// are excluded from backups and have no archive, so an ADMIN, the workspace
// OWNER or an editor deleting the Page would be an irreversible loss, and spec
// versions from the private period must not become readable to others.
func workbookPageOwnerOnly(rec *pageRecord, userID string) bool {
	return !rec.HasWorkbook || rec.OwnerUserID == userID
}

// workbookHumanDeleteRefusal is shown when an agent, or a request carrying an
// agent's identity, tries to delete a Page holding a workbook.
const workbookHumanDeleteRefusal = "deleting a workbook page requires a current human credential; agents cannot delete workbooks"

// workbookOrphanSQL is true for a Page whose owner is no longer a member of its
// workspace, including a Page an older departure handed to a crew. Nobody can
// read or change such a workbook, so only its deletion remains possible.
const workbookOrphanSQL = `(pages.owner_user_id IS NULL OR NOT EXISTS(SELECT 1 FROM workspace_members o WHERE o.workspace_id = pages.workspace_id AND o.user_id = pages.owner_user_id))`

// deleteWorkbookPage deletes a Page holding a workbook, or returns why not.
// Workbooks have no backup, so it needs a current human credential, like any
// workbook write. The owner may delete it. A workspace OWNER may delete it
// only when it is orphaned (workbookOrphanSQL), and that deletion is audited
// in the same transaction. Both rules are repeated in the DELETE itself.
func (h *PageHandler) deleteWorkbookPage(r *http.Request, workspace string, rec *pageRecord, user *AuthUser) (string, error) {
	ctx := r.Context()
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
		return workbookHumanDeleteRefusal, nil
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write") != nil {
		return workbookHumanDeleteRefusal, nil
	}
	// The loaded Page may have changed content type before this transaction.
	// Never erase collaboration history via the legacy owner/orphan path, and
	// do not append a deletion audit for a type we cannot retire yet.
	var collaboration bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=? AND workspace_id=?)`, rec.ID, workspace).Scan(&collaboration); err != nil {
		return "", err
	}
	if collaboration {
		return "collaboration documents cannot be deleted yet", nil
	}
	var notify func()
	var res sql.Result
	if rec.OwnerUserID != "" && rec.OwnerUserID == user.ID {
		res, err = tx.ExecContext(ctx, `DELETE FROM pages WHERE id = ? AND workspace_id = ? AND owner_user_id = ? AND NOT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=pages.id)`, rec.ID, workspace, user.ID)
	} else {
		role, roleErr := h.liveContentHumanRole(ctx, workspace, user.ID)
		if roleErr != nil && !errors.Is(roleErr, sql.ErrNoRows) {
			return "", roleErr
		}
		var orphaned bool
		if err := tx.QueryRowContext(ctx, `SELECT `+workbookOrphanSQL+` FROM pages WHERE id = ? AND workspace_id = ?`, rec.ID, workspace).Scan(&orphaned); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if role != "OWNER" || !orphaned {
			return workbookPageRefusal, nil
		}
		metadata, _ := json.Marshal(map[string]string{"owner_user_id": rec.OwnerUserID, "owner_crew_id": rec.OwnerCrewID})
		if err := execOneRow(ctx, tx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), workspace, user.ID, "artifact.orphaned_workbook_deleted", "page", rec.ID, string(metadata), tsformat.Format(time.Now())); err != nil {
			return "", err
		}
		notify, err = appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			WorkspaceID: workspace, Type: "artifact.orphaned_workbook_deleted", ActorType: journal.ActorUser, ActorID: user.ID,
			Summary: "Orphaned workbook deleted", Payload: map[string]any{"page_id": rec.ID, "owner_user_id": rec.OwnerUserID, "owner_crew_id": rec.OwnerCrewID},
		})
		if err != nil {
			return "", err
		}
		res, err = tx.ExecContext(ctx, `DELETE FROM pages WHERE id = ? AND workspace_id = ? AND NOT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=pages.id) AND `+workbookOrphanSQL+` AND EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id = m.user_id AND u.suspended_at IS NULL WHERE m.workspace_id = pages.workspace_id AND m.user_id = ? AND m.role = 'OWNER')`, rec.ID, workspace, user.ID)
	}
	if err != nil {
		return "", err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", err
	} else if n != 1 {
		return workbookPageRefusal, nil
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if notify != nil {
		notify()
	}
	return "", nil
}
