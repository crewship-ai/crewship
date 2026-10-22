package api

import (
	"context"
	"errors"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// workbookAccess is how a human reaches workbook content.
type workbookAccess int

const (
	workbookNoAccess workbookAccess = iota
	workbookOwner                   // the human owner of the Page
	workbookEditor                  // a member the owner explicitly named as editor
	workbookAuditor                 // workspace OWNER: audited read, never write
)

// errWorkbookProposalRequired refuses direct agent changes on a shared
// workbook. Shared content accepts agent changes only as proposals, which
// workbooks do not support yet, so there is no silent direct fallback.
var errWorkbookProposalRequired = errors.New("proposal_required")

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

// workbookPageOwnerOnly keeps the lifecycle and metadata history of a Page
// holding a workbook with its owner, before and after publication. Workbooks
// are excluded from backups and have no archive, so an ADMIN, the workspace
// OWNER or an editor deleting the Page would be an irreversible loss, and spec
// versions from the private period must not become readable to others.
func workbookPageOwnerOnly(rec *pageRecord, userID string) bool {
	return !rec.HasWorkbook || rec.OwnerUserID == userID
}
