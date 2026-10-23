package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCollaborationHumanAccess = errors.New("collaboration human access denied")

// Current canonical Page policy, not a ticket, participant grant or Saved
// receipt. The enclosing admission must bind its issued scope/member/epoch and
// original source; delivery still requires a send-time gate and private-owner
// reads require their existing audit boundary before releasing content.
type collaborationHumanPageAccess struct {
	permissionVersion string
	memberID          string
	memberRevision    int64
	credentialExpiry  time.Time
	record            *pageRecord
}

// This dormant primitive is only for a host-authenticated human. Call it in the
// same writer transaction used by the protected store, never as a cached HTTP
// preflight. It does not authorize agents, issue tickets or permit old admissions
// to replace their captured permission version with today's value.
func (h *PageHandler) authorizeCollaborationHumanPage(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, intent string) (collaborationHumanPageAccess, error) {
	var result collaborationHumanPageAccess
	if tx == nil || key.WorkspaceID == "" || key.ArtifactID == "" || key.ApplicationID != "wordcraft" {
		return result, errCollaborationHumanAccess
	}
	if InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
		return result, errCollaborationHumanAccess
	}
	source := auth.OriginalCredentialFromContext(ctx)
	user := UserFromContext(ctx)
	if source == nil || user == nil || user.ID != source.UserID || user.OriginalCredential == nil || *user.OriginalCredential != *source {
		return result, errCollaborationHumanAccess
	}
	kind := AuthKindFromContext(ctx)
	if kind != source.Kind {
		return result, errCollaborationHumanAccess
	}
	switch kind {
	case AuthKindSession:
		if user.SessionID != source.ID || user.TokenID != "" {
			return result, errCollaborationHumanAccess
		}
	case AuthKindCLIToken:
		if user.TokenID != source.ID || user.SessionID != "" {
			return result, errCollaborationHumanAccess
		}
	default:
		return result, errCollaborationHumanAccess
	}
	write, initialize := false, false
	switch intent {
	case "read", "read_checkpoint", "outcome":
	case "write", "task":
		write = true
	case "initialize":
		write = true
		initialize = true
	default:
		return result, errCollaborationHumanAccess
	}
	requiredScope := "pages:read"
	if write {
		requiredScope = "pages:write"
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	// Caller-supplied/middleware role and scope snapshots never replace current
	// credential scope, membership and canonical Page checks in this writer.
	ctx = context.WithValue(ctx, ctxRole, "")
	expiry, err := validateOriginalHumanCredential(ctx, tx, *source, requiredScope)
	if err != nil {
		return result, err
	}
	rec, err := h.loadPageForRead(ctx, key.WorkspaceID, "@"+key.ArtifactID)
	if err != nil {
		return result, err
	}
	// A folder ID is not a composite workspace foreign key. Never borrow
	// another workspace's ACL from a corrupt or restored Page pointer.
	if rec.FolderID != "" {
		var local bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_folders WHERE id=? AND workspace_id=?)`, rec.FolderID, key.WorkspaceID).Scan(&local); err != nil {
			return result, err
		}
		if !local {
			return result, errCollaborationHumanAccess
		}
	}
	role, err := h.liveContentHumanRole(ctx, key.WorkspaceID, user.ID)
	if errors.Is(err, sql.ErrNoRows) && rec.PrivateAudience && !write {
		reader, readErr := h.livePrivatePageReader(ctx, key.WorkspaceID, user.ID)
		if readErr != nil {
			return result, readErr
		}
		if reader == nil || !privatePageViewer(rec, reader) {
			return result, errCollaborationHumanAccess
		}
		role = reader.Role
	} else if err != nil {
		return result, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	viewer, err := h.loadViewer(ctx, key.WorkspaceID, user.ID)
	if err != nil {
		return result, err
	}
	visible, err := h.canSeePage(ctx, key.WorkspaceID, rec, nil, viewer)
	if err != nil {
		return result, err
	}
	if !visible || (write && !h.mayEditSpec(ctx, key.WorkspaceID, user.ID, role, rec)) {
		return result, errCollaborationHumanAccess
	}
	if initialize {
		// Initial custody is explicit. Ordinary workspace management cannot attach
		// a new document to another human's Page or an existing content engine.
		if rec.OwnerUserID != user.ID || rec.OwnerCrewID != "" || rec.HasProject || rec.HasApplication {
			return result, errCollaborationHumanAccess
		}
		privateSQL, _, err := privatePageOwnershipSQL(ctx, tx, "p")
		if err != nil {
			return result, err
		}
		var attached bool
		if err = tx.QueryRowContext(ctx, `SELECT `+privateSQL+` OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=p.id) FROM pages p WHERE p.id=? AND p.workspace_id=?`, rec.ID, key.WorkspaceID).Scan(&attached); err != nil {
			return result, err
		}
		if attached {
			return result, errCollaborationHumanAccess
		}
	} else {
		var application string
		if err = tx.QueryRowContext(ctx, `SELECT application_id FROM mooring_collab_documents WHERE page_id=? AND workspace_id=?`, rec.ID, key.WorkspaceID).Scan(&application); err != nil {
			return result, err
		}
		if application != key.ApplicationID {
			return result, errCollaborationHumanAccess
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(w.authority_revision AS TEXT),m.id,m.access_revision FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id JOIN users u ON u.id=m.user_id WHERE w.id=? AND m.user_id=? AND w.deleted_at IS NULL AND u.suspended_at IS NULL AND CAST(u.authority_revision AS TEXT)=?`, key.WorkspaceID, user.ID, source.UserAuthorityRevision).Scan(&result.permissionVersion, &result.memberID, &result.memberRevision); err != nil {
		return result, err
	}
	if !chataudience.ValidUserAuthorityRevision(result.permissionVersion) || result.memberID == "" || result.memberRevision < 1 || (!expiry.IsZero() && !time.Now().Before(expiry)) {
		return collaborationHumanPageAccess{}, errCollaborationHumanAccess
	}
	result.credentialExpiry = expiry
	result.record = rec
	return result, nil
}
