package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Admission precedes reading a mutating payload. Receipt reconciliation stays
// read-only and available when storage is full. Cleanup removes only this
// request's transient reservation, never any confirmed artifact data.
func artifactStorageUpload(w http.ResponseWriter, r *http.Request, store *artifacts.VersionStore, key artifacts.VersionKey, maxBody int64, authorize artifacts.VersionAuthorize) (*http.Request, func(), error) {
	bytes := maxBody
	if r.ContentLength >= 0 && r.ContentLength < bytes {
		bytes = r.ContentLength
	}
	bytes += 256 << 10 // manifest/row overhead remains bounded independently of JSON content
	upload, err := store.ReserveUpload(r.Context(), key, bytes, authorize)
	if err != nil {
		return r, func() {}, err
	}
	ctx, cancel := context.WithDeadline(upload.Context(r.Context()), upload.ExpiresAt())
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(upload.ExpiresAt())
	body := r.Body
	stop := context.AfterFunc(ctx, func() {
		if body != nil {
			_ = body.Close()
		}
	})
	cleanup := func() { stop(); cancel(); _ = controller.SetReadDeadline(time.Time{}); _ = upload.Close() }
	return r.WithContext(ctx), cleanup, nil
}

// Creation has no immutable Page yet. This is only admission for a bounded
// body; the existing creation transaction still decides every content right,
// consent, format and Page limit from the decoded request.
func (h *PageHandler) artifactCreationUploadAuthority(r *http.Request, workspace string) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		user := UserFromContext(ctx)
		if user == nil || WorkspaceIDFromContext(ctx) != workspace {
			return "", errArtifactProjectAccess
		}
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		request := r.WithContext(ctx)
		actor := user.ID
		if run, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); run != nil {
			if run.WorkspaceID != workspace || run.HumanUserID != user.ID || h.checkOriginalUserArtifactRunPrincipal(ctx, run) != nil || !h.artifactAgentPolicyAllows(ctx, workspace, run.CrewID) || h.checkArtifactProjectRunConsent(ctx, run, "") != nil {
				return "", errArtifactProjectAccess
			}
			if _, err := h.validateArtifactPrincipalOriginalCredential(ctx, run, "pages:write"); err != nil {
				return "", errArtifactProjectAccess
			}
			actor = "agent:" + run.AgentID + ":" + run.RunID
		} else {
			agent, _ := pageGrantCallerIsAgent(request)
			if agent || projectAgentFrom(ctx) != nil || InternalTokenWorkspaceFromContext(ctx) != "" || validateContentHumanCredential(request, tx, "pages:write") != nil {
				return "", errArtifactProjectAccess
			}
		}
		role, err := h.liveContentHumanRole(ctx, workspace, user.ID)
		if err != nil {
			return "", errArtifactProjectAccess
		}
		var caps sql.NullString
		if tx.QueryRowContext(ctx, `SELECT capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, user.ID).Scan(&caps) != nil {
			return "", errArtifactProjectAccess
		}
		if !canRole(role, "create") && !HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate) {
			return "", errArtifactProjectAccess
		}
		return actor, nil
	}
}

// artifactStorageRefusal writes the shared aggregate-capacity responses for
// every artifact family. It returns false for any other error so the caller's
// own mapping applies. Nothing in these responses names another Page's usage.
func artifactStorageRefusal(w http.ResponseWriter, err error) bool {
	err = artifacts.StorageBudgetError(err)
	switch {
	case errors.Is(err, artifacts.ErrStorageQuota):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     "Workspace artifact storage is full. Nothing was saved and every existing version is retained; history is never deleted automatically. Keep your local draft (download or copy it) and ask a workspace owner or admin to free space or raise capacity, then save again.",
			"code":      "artifact_storage_quota",
			"retention": "keep-all",
		})
	case errors.Is(err, artifacts.ErrStorageBusy):
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "Too many artifact uploads are in progress. Nothing was saved; keep your draft and retry shortly.",
			"code":  "artifact_storage_busy",
		})
	default:
		return false
	}
	return true
}

// ArtifactStorageUsage reports the workspace's aggregate artifact storage to
// its live owners and admins. Members get 403: workspace-wide usage reveals
// activity on Pages they cannot read. Instance usage is a coarse level only.
func (h *PageHandler) ArtifactStorageUsage(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	ctx := r.Context()
	user := UserFromContext(ctx)
	workspace := WorkspaceIDFromContext(ctx)
	if user == nil || workspace == "" || validateContentHumanCredential(r, h.pageAuthReader(ctx), "pages:read") != nil {
		replyError(w, http.StatusForbidden, "workspace owner or admin required")
		return
	}
	role, err := h.liveContentHumanRole(ctx, workspace, user.ID)
	if err != nil || !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "workspace owner or admin required")
		return
	}
	usage, err := artifacts.NewVersionStore(h.db, artifacts.VersionOptions{Enabled: true}).WorkspaceStorage(ctx, workspace)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "artifact storage usage unavailable")
		return
	}
	writeJSON(w, http.StatusOK, usage)
}
