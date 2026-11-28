package conversation

import (
	"context"
	"errors"
)

const (
	RunAuditArchived   = "archived"
	RunAuditIncomplete = "incomplete"
	RunAuditOmitted    = "omitted"
)

// BeginRunAudit records incompleteness before execution. Publication after the
// capture is committed replaces this status in a single database update.
// The archive is owned by the chat, so backups and deletion share its lifecycle.
func (s *Store) BeginRunAudit(ctx context.Context, chatID, runID string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_run_audits (chat_id, run_id) VALUES (?, ?)`, chatID, runID)
	return err == nil, err
}

// FinishRunAudit receives only sanitized, bounded wire data from the output
// capture. NULL is used when privacy or a capture failure rules out content.
func (s *Store) FinishRunAudit(ctx context.Context, chatID, runID, status string, wire []byte) error {
	if s.db == nil {
		return errors.New("run archive store is unavailable")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE chat_run_audits SET status = ?, wire = ?
		WHERE chat_id = ? AND run_id = ? AND status = 'incomplete' AND wire IS NULL`, status, wire, chatID, runID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("run archive admission is unavailable")
	}
	return nil
}
