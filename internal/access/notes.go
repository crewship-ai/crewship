package access

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SaveNote records an explicit human memory with its own classified source.
// No model invocation or credential authority is admitted.
func (s Store) SaveNote(ctx context.Context, user, workspace, agent, chat, text string) (ContextEntry, error) {
	if len(text) == 0 || len(text) > 8192 {
		return ContextEntry{}, ErrDenied
	}
	handle, _, err := s.AdmitChat(ctx, user, workspace, agent, chat, "", nil)
	if err != nil {
		return ContextEntry{}, err
	}
	ok := false
	defer func() {
		if !ok {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.RevokeAttempt(cleanup, handle)
		}
	}()
	source, err := s.AppendContext(ctx, handle, ContextUser, text)
	if err != nil {
		return ContextEntry{}, err
	}
	note, err := s.DeriveContext(ctx, handle, ContextMemory, []string{source.ID}, text)
	if err != nil {
		return ContextEntry{}, err
	}
	if err = s.CompleteAttempt(ctx, handle); err != nil {
		return ContextEntry{}, err
	}
	ok = true
	return note, nil
}

// DeleteNote withdraws the origin, retaining immutable provenance so queued
// consumers fail closed instead of silently losing a dependency.
func (s Store) DeleteNote(ctx context.Context, user, workspace, agent, chat, id string) error {
	if s.DB == nil {
		return ErrDenied
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := currentContextAudience(ctx, tx, user, workspace, agent, chat)
	if err != nil {
		return err
	}
	e, err := readContext(ctx, tx, a, id, map[string]bool{})
	if err != nil || e.Kind != ContextMemory {
		return ErrDenied
	}
	var origin string
	if err = tx.QueryRowContext(ctx, `SELECT c.attempt_id FROM access_context c JOIN access_attempts a ON a.id=c.attempt_id WHERE c.id=? AND a.principal_id=?`, id, user).Scan(&origin); err != nil {
		return ErrDenied
	}
	if _, err = tx.ExecContext(ctx, `UPDATE access_attempts SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), origin); err != nil {
		return err
	}
	return tx.Commit()
}
