package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

// memberAccessTarget uses the authenticated workspace and membership row ID;
// neither the actor nor the target user can be supplied in the policy body.
func (h *WorkspaceHandler) memberAccessTarget(w http.ResponseWriter, r *http.Request) (actor, user, workspace string, ok bool) {
	w.Header().Set("Cache-Control", "no-store")
	caller := UserFromContext(r.Context())
	workspace = WorkspaceIDFromContext(r.Context())
	if caller == nil || !canScope(r.Context(), "workspace:admin") {
		replyError(w, http.StatusForbidden, "Workspace administration required")
		return
	}
	if workspace == "" || workspace != r.PathValue("workspaceId") {
		replyError(w, http.StatusNotFound, "Member not found")
		return
	}
	err := h.db.QueryRowContext(r.Context(), `SELECT user_id FROM workspace_members WHERE id=? AND workspace_id=?`, r.PathValue("memberId"), workspace).Scan(&user)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	return caller.ID, user, workspace, true
}

func (h *WorkspaceHandler) memberAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrDenied), errors.Is(err, sql.ErrNoRows):
		replyError(w, http.StatusNotFound, "Member policy unavailable")
	case errors.Is(err, access.ErrConflict):
		replyError(w, http.StatusConflict, "Member policy changed; read the current policy before replacing it")
	default:
		replyInternalError(w, h.logger, "member access policy", err)
	}
}

// GetMemberAccess returns the current complete policy to a trusted admin.
func (h *WorkspaceHandler) GetMemberAccess(w http.ResponseWriter, r *http.Request) {
	actor, user, workspace, ok := h.memberAccessTarget(w, r)
	if !ok {
		return
	}
	p, err := (access.Store{DB: h.db}).Policy(r.Context(), actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PutMemberAccess replaces grants atomically against the submitted membership
// revision. It never retries conflicts or upgrades existing runtime attempts.
func (h *WorkspaceHandler) PutMemberAccess(w http.ResponseWriter, r *http.Request) {
	actor, user, workspace, ok := h.memberAccessTarget(w, r)
	if !ok {
		return
	}
	var p access.Policy
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.ID != r.PathValue("memberId") || p.Revision < 1 || p.Rights == nil || (p.Mode != "trusted" && p.Mode != "restricted") {
		replyError(w, http.StatusBadRequest, "Provide membership_id, revision, mode and an explicit rights array")
		return
	}
	m, err := (access.Store{DB: h.db}).Replace(r.Context(), actor, user, workspace, p.Mode, p.Membership, p.Rights)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	p.Membership = m
	writeJSON(w, http.StatusOK, p)
}
