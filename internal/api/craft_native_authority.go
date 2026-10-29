package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCraftNativeAccess = errors.New("native Craft access denied")

type craftNativeProof struct {
	workspaceEpoch, memberID string
	memberRevision           int64
	expiry                   time.Time
	record                   *pageRecord
	application              string
}

// Canonical Page policy, evaluated in the caller's protected SQL transaction.
// This has no collaboration document, Yrs session, or agent admission dependency.
func (m *craftNativeHTTP) pageAuthority(ctx context.Context, tx *sql.Tx, source auth.OriginalCredential, scope mooring.EditLeaseScope, write bool) (craftNativeProof, error) {
	return m.pageAuthorityForView(ctx, tx, source, scope, write, false)
}

// sharedPreview permits canonical Comment visibility only for the separate
// shared snapshot selector. It cannot open a native editor admission.
func (m *craftNativeHTTP) pageAuthorityForView(ctx context.Context, tx *sql.Tx, source auth.OriginalCredential, scope mooring.EditLeaseScope, write, sharedPreview bool) (craftNativeProof, error) {
	var result craftNativeProof
	required := "pages:read"
	if write {
		required = "pages:write"
	}
	expiry, err := validateOriginalHumanCredential(ctx, tx, source, required)
	if err != nil {
		return result, err
	}
	ctx = context.WithValue(context.WithValue(ctx, pageAuthReaderKey{}, tx), ctxRole, "")
	rec, err := m.pages.loadPageForRead(ctx, scope.WorkspaceID, "@"+scope.ArtifactID)
	if err != nil {
		return result, err
	}
	if rec.FolderID != "" {
		var local bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM page_folders WHERE id=? AND workspace_id=?)`, rec.FolderID, scope.WorkspaceID).Scan(&local); err != nil {
			return result, err
		}
		if !local {
			return result, errCraftNativeAccess
		}
	}
	role, err := m.pages.liveContentHumanRole(ctx, scope.WorkspaceID, source.UserID)
	if err != nil {
		return result, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	viewer, err := m.pages.loadViewer(ctx, scope.WorkspaceID, source.UserID)
	if err != nil {
		return result, err
	}
	visible, err := m.pages.canSeePage(ctx, scope.WorkspaceID, rec, nil, viewer)
	if err != nil {
		return result, err
	}
	if !write && !sharedPreview && !m.pages.mayReadNativeWithoutComment(ctx, scope.WorkspaceID, rec, viewer) {
		return result, errCraftNativeAccess
	}
	if !visible || write && !m.pages.mayEditSpec(ctx, scope.WorkspaceID, source.UserID, role, rec) {
		return result, errCraftNativeAccess
	}
	if err = tx.QueryRowContext(ctx, `SELECT d.application_id,CAST(w.authority_revision AS TEXT),wm.id,wm.access_revision FROM craft_native_documents d JOIN workspaces w ON w.id=d.workspace_id JOIN workspace_members wm ON wm.workspace_id=w.id WHERE d.page_id=? AND d.workspace_id=? AND wm.user_id=? AND w.deleted_at IS NULL`, scope.ArtifactID, scope.WorkspaceID, source.UserID).Scan(&result.application, &result.workspaceEpoch, &result.memberID, &result.memberRevision); err != nil {
		return result, err
	}
	if result.application != "wordcraft" && result.application != "gridcraft" {
		return result, errCraftNativeAccess
	}
	if result.memberID == "" || result.memberRevision < 1 || result.workspaceEpoch == "" || !expiry.IsZero() && !time.Now().Before(expiry) {
		return result, errCraftNativeAccess
	}
	result.record = rec
	result.expiry = expiry
	return result, nil
}

// Authentication precedes admission. A source reference alone is never a bearer.
func craftNativeHuman(ctx context.Context) (auth.OriginalCredential, error) {
	var zero auth.OriginalCredential
	user, source := UserFromContext(ctx), auth.OriginalCredentialFromContext(ctx)
	if user == nil || source == nil || !source.Valid() || user.OriginalCredential == nil || *user.OriginalCredential != *source || source.UserID != user.ID || AuthKindFromContext(ctx) != source.Kind || InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
		return zero, errCraftNativeAccess
	}
	switch source.Kind {
	case AuthKindSession:
		if user.SessionID != source.ID || user.TokenID != "" {
			return zero, errCraftNativeAccess
		}
	case AuthKindCLIToken:
		if user.TokenID != source.ID || user.SessionID != "" {
			return zero, errCraftNativeAccess
		}
	default:
		return zero, errCraftNativeAccess
	}
	return *source, nil
}
