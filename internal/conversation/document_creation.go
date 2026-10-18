package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

var ErrDocumentCreationConsentUnconfirmed = errors.New("Your message was saved, but document creation permission could not be confirmed. The agent run was not started")

// AppendDocumentCreation is the trusted human-chat entrypoint. Ordinary Append
// deliberately cannot grant authority from Message.Metadata. The transcript is
// synced first; a later SQL failure preserves that message but admits no run.
func (s *Store) AppendDocumentCreation(ctx context.Context, session string, msg Message, intent documents.CreationIntent, receipt *chataudience.Receipt) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	transcriptSaved := false
	defer func() {
		if err != nil && transcriptSaved {
			err = fmt.Errorf("%w: %v", ErrDocumentCreationConsentUnconfirmed, err)
		}
	}()
	if s.db == nil || receipt == nil || intent.Validate() != nil || msg.Role != RoleUser || msg.AuthorUserID != receipt.UserID || session != receipt.ChatID || msg.AgentID != receipt.AgentID || msg.ID == "" {
		return errors.New("trusted human document creation consent required")
	}
	raw, err := json.Marshal(msg.Metadata)
	if err != nil {
		return err
	}
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return errors.New("document creation message metadata required")
	}
	stored, err := documents.CreationIntentFromMetadata(metadata)
	if err != nil || stored == nil || *stored != intent {
		return errors.New("document creation message consent mismatch")
	}
	if err := validateID(session); err != nil {
		return err
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}
	msg.ChatID = session
	encoded, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err = s.appendJSONL(session, append(encoded, '\n')); err != nil {
		return err
	}
	if err = s.SyncSession(ctx, session); err != nil {
		return err
	}
	transcriptSaved = true
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return err
	}
	current, err := chataudience.CaptureTrustedQuery(ctx, tx, session, receipt.UserID, receipt)
	if err != nil || current == nil || *current != *receipt {
		return errors.New("document creation human authority changed")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,tool_summary,ts,author_user_id,run_id) VALUES(?,?,?,?,?,?,?,?,?)`, msg.ID, session, msg.AgentID, string(msg.Role), msg.Content, msg.ToolSummary, msg.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), msg.AuthorUserID, msg.RunID); err != nil {
		return fmt.Errorf("insert document creation message: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at,drafting_allowed) VALUES(?,?,?,?,?,?,?)`, msg.ID, receipt.WorkspaceID, receipt.UserID, session, intent.SubmissionID, msg.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), intent.Drafting); err != nil {
		return fmt.Errorf("insert document creation consent: %w", err)
	}
	return tx.Commit()
}
