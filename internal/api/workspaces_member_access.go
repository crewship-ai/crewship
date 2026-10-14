package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Paths may reject a mismatch; only the validated context scopes queries.
func memberAccessWorkspace(r *http.Request) (string, bool) {
	workspace := WorkspaceIDFromContext(r.Context())
	return workspace, workspace != "" && workspace == r.PathValue("workspaceId")
}

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
	h.writeMemberAccessPolicy(w, r, p)
}

// Fence the final document to the requested membership. Removal and rejoin
// between target lookup and snapshot must not return a replacement membership.
func (h *WorkspaceHandler) writeMemberAccessPolicy(w http.ResponseWriter, r *http.Request, p access.Policy) {
	if p.ID != r.PathValue("memberId") {
		h.memberAccessError(w, access.ErrDenied)
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
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || p.ID != r.PathValue("memberId") || p.Revision < 1 || p.Rights == nil || (p.Mode != "trusted" && p.Mode != "restricted") {
		replyError(w, http.StatusBadRequest, "Provide membership_id, revision, mode and an explicit rights array")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	defer tx.Rollback()
	store := access.Store{DB: h.db}
	before, err := store.PolicyIn(r.Context(), tx, actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if _, err = store.ReplaceIn(r.Context(), tx, actor, user, workspace, p.Mode, p.Membership, p.Rights, p.Reissue...); err != nil {
		h.memberAccessError(w, err)
		return
	}
	after, err := store.PolicyIn(r.Context(), tx, actor, user, workspace)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	entry := journal.Entry{WorkspaceID: workspace, Type: journal.EntryMemberAccessChanged, ActorType: journal.ActorUser, ActorID: actor, Summary: "Member resource access changed", Payload: map[string]any{
		"member_id": after.ID, "subject_user_id": user, "previous_mode": before.Mode, "mode": after.Mode,
		"previous_revision": before.Revision, "revision": after.Revision,
		"previous_grants": auditGrantDetails(before.GrantDetails), "grants": auditGrantDetails(after.GrantDetails), "reissued_rights": p.Reissue,
	}}
	appendTx := journal.AppendTx
	if h.policyJournal != nil {
		emitter, ok := h.policyJournal.(journal.TransactionEmitter)
		if !ok {
			h.memberAccessError(w, errors.New("member access journal does not support atomic writes"))
			return
		}
		appendTx = emitter.AppendTx
	}
	notify, err := appendTx(r.Context(), tx, entry)
	if err != nil {
		h.memberAccessError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.memberAccessError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	h.writeMemberAccessPolicy(w, r, after)
}

// Identity names belong in the admin presentation, not the immutable audit.
func auditGrantDetails(details []access.GrantDetail) []access.GrantDetail {
	result := append([]access.GrantDetail{}, details...)
	for i := range result {
		result[i].IssuerName = ""
	}
	return result
}
