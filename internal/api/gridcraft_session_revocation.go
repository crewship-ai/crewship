package api

import (
	"context"
	"database/sql"
	"time"
)

// Close every editor session that loses its write authority in the same
// transaction as the grant change. Restoring a grant cannot revive an old
// editor-session ticket. The existing human authority rule remains canonical.
func (h *PageHandler) closeRevokedWorkbookSessions(ctx context.Context, tx *sql.Tx, workspace string, rec *pageRecord) ([]string, error) {
	if !rec.HasWorkbook {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT user_id FROM gridcraft_sessions WHERE page_id=? AND closed_at IS NULL AND expires_at>?`, rec.ID, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	var users []string
	for rows.Next() {
		var user string
		if err := rows.Scan(&user); err != nil {
			rows.Close()
			return nil, err
		}
		users = append(users, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	var closed []string
	for _, user := range users {
		if _, _, err := h.workbookHumanAccess(context.WithValue(ctx, ctxUser, &AuthUser{ID: user}), workspace, rec.ID, user, true); err == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gridcraft_sessions SET closed_at=coalesce(closed_at,?) WHERE page_id=? AND user_id=? AND closed_at IS NULL`, time.Now().UnixMilli(), rec.ID, user); err != nil {
			return nil, err
		}
		closed = append(closed, user)
	}
	return closed, nil
}

// Reuse the same authority rule for indirect changes such as ownership or
// workspace roles. The transaction fences session closure against re-admission.
func (h *PageHandler) recheckWorkbookSessions(ctx context.Context, page string) ([]string, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var workspace string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM gridcraft_heads WHERE page_id=?`, page).Scan(&workspace); err != nil {
		return nil, err
	}
	closed, err := h.closeRevokedWorkbookSessions(ctx, tx, workspace, &pageRecord{ID: page, HasWorkbook: true})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return closed, nil
}
