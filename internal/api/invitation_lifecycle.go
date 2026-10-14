package api

import (
	"context"
	"database/sql"
	"github.com/crewship-ai/crewship/internal/journal"
	"net/http"
	"time"
)

// ReissueInvitation rotates a pending invitation's proof. It never adds a
// membership or returns a previously issued proof.
func (h *WorkspaceHandler) ReissueInvitation(w http.ResponseWriter, r *http.Request) {
	h.mutateInvitation(w, r, true)
}
func (h *WorkspaceHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	h.mutateInvitation(w, r, false)
}

func invitationIssuerRole(ctx context.Context, tx *sql.Tx, user, workspace string) (string, error) {
	var role string
	err := tx.QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id JOIN workspaces w ON w.id=m.workspace_id WHERE m.user_id=? AND m.workspace_id=? AND u.suspended_at IS NULL AND w.deleted_at IS NULL AND m.role IN ('OWNER','ADMIN') AND NOT EXISTS(SELECT 1 FROM workspace_members restricted_member WHERE restricted_member.user_id=u.id AND restricted_member.access_mode='restricted')`, user, workspace).Scan(&role)
	return role, err
}

func (h *WorkspaceHandler) mutateInvitation(w http.ResponseWriter, r *http.Request, reissue bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Authentication required")
		return
	}
	workspace := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("invitationId")
	ctx, publishAudit := withInvitationAudit(r.Context(), h.invitationJournal)
	r = r.WithContext(ctx)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin invitation mutation", err)
		return
	}
	defer tx.Rollback()
	issuerRole, err := invitationIssuerRole(r.Context(), tx, user.ID, workspace)
	if err == sql.ErrNoRows {
		replyError(w, 403, "Forbidden")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "check invitation issuer", err)
		return
	}
	var inv invitationResponse
	var oldProof string
	err = tx.QueryRowContext(r.Context(), `SELECT id,workspace_id,email,role,token,created_at FROM workspace_invitations WHERE id=? AND workspace_id=? AND accepted_at IS NULL`, id, workspace).Scan(&inv.ID, &inv.WorkspaceID, &inv.Email, &inv.Role, &oldProof, &inv.CreatedAt)
	if err == sql.ErrNoRows {
		replyError(w, 404, "Invitation unavailable")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read invitation mutation", err)
		return
	}
	if inv.Role == "ADMIN" && issuerRole != "OWNER" {
		replyError(w, 403, "Only workspace owner can manage an ADMIN invitation")
		return
	}
	if inv.Role != "ADMIN" && inv.Role != "MANAGER" && inv.Role != "MEMBER" && inv.Role != "VIEWER" {
		replyError(w, 409, "Invitation role unavailable")
		return
	}
	now := time.Now().UTC()
	stored := "revoked:" + generateCUID()
	inv.ExpiresAt = "1970-01-01T00:00:00Z"
	if reissue {
		var existing bool
		if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=? AND LOWER(u.email)=LOWER(?))`, workspace, inv.Email).Scan(&existing); err != nil {
			replyInternalError(w, h.logger, "check invitation membership", err)
			return
		}
		if existing {
			replyError(w, 409, "User is already a member of this workspace")
			return
		}
		raw, e := generateToken()
		if e != nil {
			replyInternalError(w, h.logger, "generate invitation proof", e)
			return
		}
		inv.Token = "wi1_" + raw
		inv.Status = "pending"
		stored = invitationTokenHash(inv.Token)
		inv.ExpiresAt = now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE workspace_invitations SET token=?,expires_at=?,invited_by=? WHERE id=? AND workspace_id=? AND token=? AND accepted_at IS NULL`, stored, inv.ExpiresAt, user.ID, id, workspace, oldProof)
	if err != nil {
		replyInternalError(w, h.logger, "mutate invitation", err)
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "claim invitation mutation", err)
		return
	}
	if n != 1 {
		replyError(w, 409, "Invitation changed")
		return
	}
	kind := journal.EntryInvitationRevoked
	if reissue {
		kind = journal.EntryInvitationReissued
	}
	if err := appendInvitationAudit(r.Context(), tx, kind, workspace, user.ID, id, inv.Role, ""); err != nil {
		replyInternalError(w, h.logger, "audit invitation mutation", err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit invitation mutation", err)
		return
	}
	publishAudit()
	if reissue {
		inv.InvitedBy = user.ID
		writeJSON(w, http.StatusOK, inv)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
