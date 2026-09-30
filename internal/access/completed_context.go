package access

import (
	"context"
	"database/sql"
)

// CompletedContext is a host-only exact output projection. The opaque origin
// handle grants no execution lease and public entry IDs alone are insufficient.
func (s Store) CompletedContext(ctx context.Context, handle, user, workspace, agent, chat string, ids []string) ([]ContextEntry, error) {
	if s.DB == nil || handle == "" || len(ids) == 0 || len(ids) > 64 {
		return nil, ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a, err := resolveState(ctx, tx, digest(handle), true, map[string]bool{}, true)
	if err != nil || a.Principal != user || a.Workspace != workspace || a.Agent != agent || a.Chat != chat {
		return nil, ErrDenied
	}
	var completed int
	if tx.QueryRowContext(ctx, `SELECT 1 FROM access_attempts WHERE id=? AND completed_at IS NOT NULL`, a.ID).Scan(&completed) != nil {
		return nil, ErrDenied
	}
	entries := make([]ContextEntry, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, ErrDenied
		}
		seen[id] = true
		var owned int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM access_context WHERE id=? AND attempt_id=? AND kind='history' AND role='assistant'`, id, a.ID).Scan(&owned) != nil {
			return nil, ErrDenied
		}
		e, err := readContext(ctx, tx, a, id, map[string]bool{})
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = s.CheckContextAttempt(ctx, handle); err != nil {
		return nil, err
	}
	return entries, nil
}
