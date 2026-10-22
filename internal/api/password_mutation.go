package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/journal"
)

func passwordSessionsTx(ctx context.Context, tx *sql.Tx, store sessions.Store, user, keep string) ([]string, func(), error) {
	if store == nil {
		return nil, nil, errors.New("session store required for password mutation")
	}
	writer, ok := store.(sessions.TransactionRevoker)
	if !ok {
		return nil, nil, errors.New("session store does not support atomic revocation")
	}
	return writer.RevokeOthersTx(ctx, tx, user, keep, sessions.ReasonPasswordChange)
}

func auditPasswordTx(ctx context.Context, tx *sql.Tx, user, action, now string, sessionsRevoked, tokensRevoked int) error {
	// Counts only: never password hashes, reset proofs, addresses or credentials.
	metadata, err := json.Marshal(map[string]int{"sessions_revoked": sessionsRevoked, "cli_tokens_revoked": tokensRevoked})
	if err != nil {
		return err
	}
	// No unchained fallback: whoever sets the password gains access with it,
	// so a broken chain tail refuses (replyPasswordAuditError says why).
	return appendInstanceAuditTx(ctx, tx, journal.InstanceAuditFields{
		ID: generateCUID(), UserID: sql.NullString{String: user, Valid: true}, Action: action,
		EntityType: "user", EntityID: sql.NullString{String: user, Valid: true},
		Metadata: sql.NullString{String: string(metadata), Valid: true}, CreatedAt: now,
	})
}

// replyPasswordAuditError fails a web password change/reset closed. A broken
// audit chain is reported as such (503), not as an anonymous server error.
func replyPasswordAuditError(w http.ResponseWriter, logger *slog.Logger, op string, err error) {
	if errors.Is(err, journal.ErrChainIntegrity) || errors.Is(err, journal.ErrSigningKeyUnavailable) {
		logger.Error(op+": instance audit chain refused the record", "err", err)
		replyError(w, http.StatusServiceUnavailable, "Passwords cannot be changed until an instance administrator repairs the audit trail.")
		return
	}
	replyInternalError(w, logger, op, err)
}

func acknowledgePasswordTx(ctx context.Context, tx *sql.Tx, user, expected string) error {
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT hashed_password FROM users WHERE id=?`, user).Scan(&stored); err != nil {
		return err
	}
	if stored != expected {
		return errors.New("password write not acknowledged")
	}
	return nil
}

func acknowledgePasswordSessions(ctx context.Context, tx *sql.Tx, user, expected, keep string, ids []string) error {
	if err := acknowledgePasswordTx(ctx, tx, user, expected); err != nil {
		return err
	}
	for _, id := range ids {
		var at, reason sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT revoked_at,revoked_reason FROM user_sessions WHERE id=? AND user_id=?`, id, user).Scan(&at, &reason); err != nil {
			return err
		}
		if !at.Valid || reason.String != sessions.ReasonPasswordChange {
			return errors.New("password session revocation not acknowledged")
		}
	}
	// A later audit append must not leave a new active sibling outside the
	// selected IDs. Reset revokes every session; change preserves only the
	// caller's session and already expired siblings.
	var remaining bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions
		WHERE user_id=? AND revoked_at IS NULL AND id!=?
		AND (?='' OR julianday(expires_at) IS NULL OR julianday(expires_at)>julianday('now')))`, user, keep, keep).Scan(&remaining); err != nil {
		return err
	}
	if remaining {
		return errors.New("active sessions remain after password mutation")
	}
	return nil
}

// AuditLocalPasswordResetTx records host-side recovery without inventing an
// authenticated human actor. The caller owns mutations and the final commit.
func AuditLocalPasswordResetTx(ctx context.Context, tx *sql.Tx, userID, expectedHash, now string, sessionsRevoked int64) error {
	metadata, err := json.Marshal(map[string]any{"source": "local_recovery", "sessions_revoked": sessionsRevoked, "cli_tokens_revoked": 0})
	if err != nil {
		return err
	}
	// Host-side recovery is the operator's own incident response (shell
	// access to the server already implies it), so it may fall back.
	if err := appendInstanceAuditTx(withSecurityAuditFallback(ctx), tx, journal.InstanceAuditFields{ID: generateCUID(), Action: "auth.password_reset", EntityType: "user", EntityID: sql.NullString{String: userID, Valid: true}, Metadata: sql.NullString{String: string(metadata), Valid: true}, CreatedAt: now}); err != nil {
		return err
	}
	if err := acknowledgePasswordTx(ctx, tx, userID, expectedHash); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_sessions WHERE user_id=? AND revoked_at IS NULL`, userID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("local reset sessions not revoked")
	}
	if err := acknowledgePasswordRecoveryTx(ctx, tx, userID); err != nil {
		return err
	}

	return nil
}

func acknowledgePasswordRecoveryTx(ctx context.Context, tx *sql.Tx, user string) error {
	var failed int
	var locked, last sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT failed_login_count,locked_until,last_failed_login_at FROM users WHERE id=?`, user).Scan(&failed, &locked, &last); err != nil {
		return err
	}
	if failed != 0 || locked.Valid || last.Valid {
		return errors.New("password recovery lockout reset not acknowledged")
	}
	return nil
}
