package conversation

import (
	"context"
	"errors"
)

const (
	RunAuditArchived   = "archived"
	RunAuditStaged     = "staged"
	RunAuditIncomplete = "incomplete"
	RunAuditOmitted    = "omitted"
)

// BeginRunAudit records incompleteness before execution. The capture stays
// incomplete to readers until the conversation and agent log are durable.
// The archive is owned by the chat, so backups and deletion share its lifecycle.
func (s *Store) BeginRunAudit(ctx context.Context, chatID, runID string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_run_audits (chat_id, run_id) VALUES (?, ?)`, chatID, runID)
	return err == nil, err
}

// FinishRunAudit records sanitized, bounded wire data. A complete capture
// is staged until the other durable records are written. A partial capture
// can retain sanitized diagnostic events while its status stays incomplete.
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

// PublishRunAudit exposes a complete capture after the chat and agent records
// have been synced. Strict privacy runs already have a terminal omitted status.
func (s *Store) PublishRunAudit(ctx context.Context, chatID, runID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE chat_run_audits
		SET status = CASE WHEN status = 'staged' THEN 'archived' ELSE status END
		WHERE chat_id = ? AND run_id = ? AND status IN ('staged', 'omitted')`, chatID, runID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("run archive is not ready for publication")
	}
	return nil
}
