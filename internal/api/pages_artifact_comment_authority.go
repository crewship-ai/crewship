package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// mayCommentPage is human feedback authority, never content/producer authority.
// The normal live-grant reader retains issuer and membership revocation checks.
func (h *PageHandler) mayCommentPage(ctx context.Context, wsID, userID, role string, rec *pageRecord) bool {
	if rec == nil || userID == "" {
		return false
	}
	if h.mayEditSpec(ctx, wsID, userID, role, rec) {
		return true
	}
	if rec.PrivateAudience {
		return false
	}
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return false
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if grant.Level == pageGrantComment || grant.Level == pageGrantWrite {
			return true
		}
	}
	return false
}

// Native observer and existing collaborative checkpoint reads currently serve
// latest rather than a shared snapshot.
// Preserve all pre-existing read paths, but do not turn the new comment level
// into permission to obtain native private bytes before shared reads exist.
func (h *PageHandler) mayReadNativeWithoutComment(ctx context.Context, wsID string, rec *pageRecord, viewer *pageViewer) bool {
	if rec == nil || viewer == nil {
		return false
	}
	if rec.PrivateAudience {
		return privatePageViewer(rec, viewer)
	}
	if h.pageReachedWithoutGrant(rec, nil, viewer) {
		return true
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false
	}
	for _, grant := range grants {
		if grant.Level == pageGrantRead || grant.Level == pageGrantProduce || grant.Level == pageGrantWrite {
			return true
		}
	}
	folder, err := h.folderACLVerdictFor(ctx, rec, viewer)
	return err == nil && folder.read
}

func artifactCommentVersion(ctx context.Context, tx *sql.Tx, k artifacts.VersionKey, human string) (int64, error) {
	var owner string
	var latest, shared int64
	err := tx.QueryRowContext(ctx, `SELECT p.owner_user_id,v.latest_version,v.shared_version FROM pages p JOIN artifact_version_heads v ON v.page_id=p.id AND v.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&owner, &latest, &shared)
	if err != nil {
		return 0, err
	}
	if owner == human {
		return latest, nil
	}
	if shared < 1 {
		return 0, artifacts.ErrVersionNotFound
	}
	return shared, nil
}

// A feedback transaction captures one Page/pin/authority epoch. A trigger which
// revokes/regrants rights or moves the shared pin before commit cannot refresh
// that admission. Agent publication and owner-chat execution use other gates.
func (h *PageHandler) artifactProjectCommentAuthority(r *http.Request, k artifacts.VersionKey) artifacts.VersionAuthorize {
	var originalActor, originalEpoch string
	var originalVersion int64
	checked := false
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		request := r.WithContext(ctx)
		if _, err := h.artifactProjectAuthority(request, k, false, false)(ctx, tx); err != nil {
			return "", err
		}
		if agent, _ := pageGrantCallerIsAgent(request); agent || projectAgentFrom(ctx) != nil || InternalTokenWorkspaceFromContext(ctx) != "" || ctx.Value(artifactRunKey{}) != nil {
			return "", errArtifactProjectAccess
		}
		user := UserFromContext(ctx)
		if user == nil || validateContentHumanCredential(request, tx, "pages:write") != nil {
			return "", errArtifactProjectAccess
		}
		role, err := h.liveContentHumanRole(ctx, k.WorkspaceID, user.ID)
		if err != nil {
			return "", errArtifactProjectAccess
		}
		rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
		if err != nil || !h.mayCommentPage(ctx, k.WorkspaceID, user.ID, role, rec) {
			return "", errArtifactProjectAccess
		}
		version, err := artifactCommentVersion(ctx, tx, k, user.ID)
		if err != nil {
			return "", err
		}
		var epoch string
		if err = tx.QueryRowContext(ctx, `SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=? AND deleted_at IS NULL`, k.WorkspaceID).Scan(&epoch); err != nil {
			return "", err
		}
		if checked && (user.ID != originalActor || version != originalVersion || epoch != originalEpoch) {
			return "", errArtifactProjectAccess
		}
		originalActor, originalEpoch, originalVersion, checked = user.ID, epoch, version, true
		return user.ID, nil
	}
}

func (h *PageHandler) artifactProjectCanComment(r *http.Request, k artifacts.VersionKey, version int64) bool {
	authorize := h.artifactProjectCommentAuthority(r, k)
	_, err := h.artifactProjectStore(r.Context()).Quota(r.Context(), k, func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil {
			return "", err
		}
		selected, err := artifactCommentVersion(ctx, tx, k, actor)
		if err != nil || selected != version {
			return "", errArtifactProjectAccess
		}
		return actor, nil
	})
	return err == nil
}
