package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// WithJournal retains the router's configured transactional audit writer.
func (h *AdminUsersHandler) WithJournal(j journal.Emitter) *AdminUsersHandler {
	h.journal = j
	return h
}

// The read-page target check is only a preflight. Security mutations must
// resolve live actor and target authority again under the writer lock.
func (h *AdminUsersHandler) securityAuthority(r *http.Request, tx *sql.Tx, target string) (bool, int, error) {
	actor := UserFromContext(r.Context())
	var email string
	var role, suspended sql.NullString
	var restricted bool
	err := tx.QueryRowContext(r.Context(), `SELECT email,instance_role,suspended_at,EXISTS(SELECT 1 FROM workspace_members WHERE user_id=users.id AND access_mode='restricted') FROM users WHERE id=?`, actor.ID).Scan(&email, &role, &suspended, &restricted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 403, nil
	}
	if err != nil {
		return false, 500, err
	}
	if suspended.Valid || restricted {
		return false, 403, nil
	}
	instance := instanceAdminSourceFor(email, role.String, false) != ""
	if !instance {
		actorRole, err := invitationIssuerRole(r.Context(), tx, actor.ID, WorkspaceIDFromContext(r.Context()))
		if errors.Is(err, sql.ErrNoRows) {
			return false, 403, nil
		}
		if err != nil {
			return false, 500, err
		}
		var targetRole string
		err = tx.QueryRowContext(r.Context(), `SELECT role FROM workspace_members WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(r.Context()), target).Scan(&targetRole)
		if errors.Is(err, sql.ErrNoRows) {
			return false, 404, nil
		}
		if err != nil {
			return false, 500, err
		}
		if targetRole == "OWNER" && actorRole != "OWNER" {
			return false, 403, nil
		}
	}
	var exists bool
	err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=?)`, target).Scan(&exists)
	if err != nil {
		return false, 500, err
	}
	if !exists {
		return false, 404, nil
	}
	return instance, 0, nil
}

func (h *AdminUsersHandler) mutateSecurity(w http.ResponseWriter, r *http.Request, operation string) {
	_, _, target, ok := h.target(w, r)
	if !ok {
		return
	}
	session := strings.TrimSpace(r.PathValue("sessionId"))
	if operation == "one" && session == "" {
		replyError(w, 400, "sessionId path parameter required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin admin security mutation", err)
		return
	}
	defer tx.Rollback()
	instance, status, err := h.securityAuthority(r, tx, target)
	if err != nil {
		replyInternalError(w, h.logger, "read admin security authority", err)
		return
	}
	if status != 0 {
		replyError(w, status, http.StatusText(status))
		return
	}
	action := map[string]string{"one": "user.session_revoked", "all": "user.sessions_revoked", "unlock": "user.unlocked"}[operation]
	metadata := map[string]any{}
	var notifySessions, notifyAudit func()
	var ids []string
	var revoked int64
	if operation == "one" {
		store, ok := h.sessions.(sessions.SingleTransactionRevoker)
		if !ok {
			replyInternalError(w, h.logger, "admin session store", errors.New("atomic single-session revocation unavailable"))
			return
		}
		var changed bool
		changed, notifySessions, err = store.RevokeSessionTx(r.Context(), tx, target, session, reasonAdminRevoke)
		if changed {
			ids = []string{session}
			revoked = 1
		}
		metadata["session_id"], metadata["revoked"] = session, revoked
	} else if operation == "all" {
		store, ok := h.sessions.(sessions.TransactionRevoker)
		if !ok {
			replyInternalError(w, h.logger, "admin session store", errors.New("atomic session revocation unavailable"))
			return
		}
		ids, notifySessions, err = store.RevokeOthersTx(r.Context(), tx, target, "", reasonAdminInvalidate)
		revoked = int64(len(ids))
		metadata["revoked"] = revoked
	} else {
		var failed int
		var locked sql.NullString
		err = tx.QueryRowContext(r.Context(), `SELECT failed_login_count,locked_until FROM users WHERE id=?`, target).Scan(&failed, &locked)
		if err == nil {
			metadata["old_failed_login_count"], metadata["old_locked"] = failed, locked.Valid
			metadata["new_failed_login_count"], metadata["new_locked"] = 0, false
			var res sql.Result
			res, err = tx.ExecContext(r.Context(), `UPDATE users SET locked_until=NULL,failed_login_count=0 WHERE id=?`, target)
			if err == nil {
				var n int64
				n, err = res.RowsAffected()
				if err == nil && n != 1 {
					err = errors.New("unlock not acknowledged")
				}
			}
		}
	}
	if err == nil && operation != "unlock" {
		// Durable, session-independent marker in the same transaction: a
		// Guest-published run accepted earlier stops at its next step even
		// when the user had already logged out and no session row changed.
		err = MarkAccessRevokedTx(r.Context(), tx, target)
	}
	if errors.Is(err, sessions.ErrNotFound) {
		replyError(w, 404, "session not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "admin security mutation", err)
		return
	}
	var expected []adminSessionRevocation
	if operation != "unlock" {
		selected := ids
		if operation == "one" {
			selected = []string{session}
		}
		expected, err = readAdminSessionRevocations(r, tx, target, selected)
		if err != nil {
			replyInternalError(w, h.logger, "snapshot session revocation", err)
			return
		}
	}
	if instance {
		// Revocations proceed with a broken chain tail; unlock restores access
		// and stays chained-only.
		auditCtx := r.Context()
		if operation != "unlock" {
			auditCtx = withSecurityAuditFallback(auditCtx)
		}
		err = auditInstance(auditCtx, r, tx, action, "USER", target, WorkspaceIDFromContext(r.Context()), metadata)
	} else {
		notifyAudit, err = appendSettingsAudit(r, tx, h.journal, action, "USER", target, metadata)
	}
	if err == nil {
		err = acknowledgeAdminSecurity(r, tx, target, operation, expected)
	}
	if err != nil {
		replyInternalError(w, h.logger, "admin security audit", err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit admin security mutation", err)
		return
	}
	if notifyAudit != nil {
		notifyAudit()
	}
	if notifySessions != nil {
		notifySessions()
	}
	if operation == "all" {
		writeJSON(w, 200, adminRevokeAllResponse{Revoked: revoked})
		return
	}
	w.WriteHeader(204)
}

func acknowledgeAdminSecurity(r *http.Request, tx *sql.Tx, target, operation string, expected []adminSessionRevocation) error {
	if operation == "unlock" {
		var failed int
		var locked sql.NullString
		err := tx.QueryRowContext(r.Context(), `SELECT failed_login_count,locked_until FROM users WHERE id=?`, target).Scan(&failed, &locked)
		if err != nil || failed != 0 || locked.Valid {
			return errors.Join(err, errors.New("unlock changed during audit"))
		}
		return nil
	}
	for _, want := range expected {
		rows, err := readAdminSessionRevocations(r, tx, target, []string{want.ID})
		if err != nil || len(rows) != 1 || rows[0] != want || !want.At.Valid {
			return errors.Join(err, errors.New("session revocation changed during audit"))
		}
	}
	if operation == "all" {
		var active bool
		err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE user_id=? AND revoked_at IS NULL)`, target).Scan(&active)
		if err != nil || active {
			return errors.Join(err, errors.New("unrevoked session remains"))
		}
	}
	return nil
}

// Preserve the exact revocation facts across the configured audit callback,
// including a repeated request that must not rewrite an older revocation.
type adminSessionRevocation struct {
	ID         string
	At, Reason sql.NullString
}

func readAdminSessionRevocations(r *http.Request, tx *sql.Tx, target string, ids []string) ([]adminSessionRevocation, error) {
	values := make([]adminSessionRevocation, 0, len(ids))
	for _, id := range ids {
		row := adminSessionRevocation{ID: id}
		if err := tx.QueryRowContext(r.Context(), `SELECT revoked_at,revoked_reason FROM user_sessions WHERE id=? AND user_id=?`, id, target).Scan(&row.At, &row.Reason); err != nil {
			return nil, err
		}
		values = append(values, row)
	}
	return values, nil
}

// MarkAccessRevokedTx records an administrator revocation or account recovery
// of a user's access. Every such path, including host-side CLI recovery,
// writes it in its own transaction; it stops accepted Guest-published runs.
func MarkAccessRevokedTx(ctx context.Context, tx *sql.Tx, user string) error {
	result, err := tx.ExecContext(ctx, `UPDATE users SET access_revoked_at=? WHERE id=?`, tsformat.Format(time.Now()), user)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.Join(err, errors.New("access revocation marker not written"))
	}
	return nil
}
