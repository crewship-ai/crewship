package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// AcceptInvitation is an explicit authenticated handoff. Narrow automation
// credentials cannot add membership authority to their owner.
func (h *AuthHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Authentication required")
		return
	}
	if !canScope(r.Context(), "*") {
		replyError(w, 403, "An unrestricted session is required")
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !validInvitationProof(request.Token) {
		replyError(w, 400, "Invitation token required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "accept invitation", err)
		return
	}
	defer tx.Rollback()
	// Use the live account identity, rather than the JWT's possibly stale email.
	var email string
	if tx.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id=? AND suspended_at IS NULL`, user.ID).Scan(&email) != nil {
		replyError(w, 404, "Invitation unavailable")
		return
	}
	joined, err := redeemPendingInvitations(r.Context(), tx, user.ID, email, request.Token, time.Now().UTC().Format(time.RFC3339), h.checkInvitationAdmission)
	if errors.Is(err, errInvitationConflict) {
		replyError(w, 409, "Invitation conflicts with current membership or was changed")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "accept invitation", err)
		return
	}
	if joined != 1 {
		// Signup may already have redeemed this token before the sign-in
		// handoff. Same-account replay is a no-op, never a new grant.
		var alreadyMember bool
		err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_invitations i JOIN workspaces w ON w.id=i.workspace_id AND w.deleted_at IS NULL JOIN workspace_members m ON m.workspace_id=i.workspace_id AND m.user_id=? WHERE (i.token=? OR (i.token NOT LIKE 'sha256:%' AND i.token=?)) AND LOWER(i.email)=LOWER(?) AND i.accepted_at IS NOT NULL)`, user.ID, invitationTokenHash(request.Token), request.Token, email).Scan(&alreadyMember)
		if err != nil {
			replyInternalError(w, h.logger, "accept invitation", err)
			return
		}
		if !alreadyMember {
			replyError(w, 404, "Invitation unavailable")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "accept invitation", err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
