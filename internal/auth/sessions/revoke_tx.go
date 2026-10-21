package sessions

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TransactionRevoker stages revocations in the caller's transaction. The
// notification callback must run only after a successful business commit.
type TransactionRevoker interface {
	RevokeOthersTx(context.Context, *sql.Tx, string, string, string) ([]string, func(), error)
}

func (s *DBStore) RevokeOthersTx(ctx context.Context, tx *sql.Tx, userID, keepID, reason string) ([]string, func(), error) {
	if userID == "" {
		return nil, nil, errors.New("user id required")
	}
	now := s.clock().UTC().Format(time.RFC3339)
	const predicate = `user_id=? AND revoked_at IS NULL AND id!=? AND (?='' OR expires_at>?)`
	args := []any{userID, keepID, keepID, now}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM user_sessions WHERE `+predicate+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE user_sessions SET revoked_at=?,revoked_reason=? WHERE `+predicate, append([]any{now, reason}, args...)...)
	if err != nil {
		return nil, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if n != int64(len(ids)) {
		return nil, nil, errors.New("session revoke count changed")
	}
	for _, id := range ids {
		var at, why string
		if err := tx.QueryRowContext(ctx, `SELECT revoked_at,revoked_reason FROM user_sessions WHERE id=? AND user_id=?`, id, userID).Scan(&at, &why); err != nil {
			return nil, nil, err
		}
		if at != now || why != reason {
			return nil, nil, errors.New("session revoke not acknowledged")
		}
	}
	return ids, nil, nil
}

// SingleTransactionRevoker changes one person's session on the caller's writer
// transaction. A repeated revoke preserves the original timestamp and reason.
type SingleTransactionRevoker interface {
	RevokeSessionTx(context.Context, *sql.Tx, string, string, string) (bool, func(), error)
}

func (s *DBStore) RevokeSessionTx(ctx context.Context, tx *sql.Tx, user, id, reason string) (bool, func(), error) {
	var at sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM user_sessions WHERE id=? AND user_id=?`, id, user).Scan(&at); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, ErrNotFound
		}
		return false, nil, err
	}
	if at.Valid {
		return false, nil, nil
	}
	stamp := s.clock().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(ctx, `UPDATE user_sessions SET revoked_at=?,revoked_reason=? WHERE id=? AND user_id=? AND revoked_at IS NULL`, stamp, reason, id, user)
	if err != nil {
		return false, nil, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, nil, errors.Join(err, errors.New("session revoke not retained"))
	}
	var stored, why string
	err = tx.QueryRowContext(ctx, `SELECT revoked_at,revoked_reason FROM user_sessions WHERE id=? AND user_id=?`, id, user).Scan(&stored, &why)
	if err != nil || stored != stamp || why != reason {
		return false, nil, errors.Join(err, errors.New("session revoke not acknowledged"))
	}
	return true, nil, nil
}
