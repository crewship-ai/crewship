package conversation

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

var ErrDocumentCreationConsentUnconfirmed = errors.New("Your message was saved, but document creation permission could not be confirmed. The agent run was not started")

// ErrDocumentCreationSubmissionConflict refuses reuse of an accepted key with a
// different request. Neither refusal starts a run or renews creation authority.
var ErrDocumentCreationSubmissionConflict = &documentCreationSubmissionConflict{}

type documentCreationSubmissionConflict struct{}

func (*documentCreationSubmissionConflict) Error() string {
	return "This document submission key was already used for a different request. No new run was started"
}
func (e *documentCreationSubmissionConflict) DocumentCreationSubmissionRefused() string {
	return e.Error()
}

type DocumentCreationAlreadyAccepted struct{ MessageID string }

func (e *DocumentCreationAlreadyAccepted) DocumentCreationSubmissionRefused() string {
	return e.Error()
}

func (e *DocumentCreationAlreadyAccepted) Error() string {
	return "This document request was already accepted. Check the saved conversation; no new run was started. Original message: " + e.MessageID
}

// CheckDocumentCreationSubmission checks current authority before runtime
// provisioning. Append repeats the same check under its own writer fence.
func (s *Store) CheckDocumentCreationSubmission(ctx context.Context, session string, msg Message, intent documents.CreationIntent, receipt *chataudience.Receipt) error {
	tx, err := s.documentCreationTransaction(ctx, session, msg, intent, receipt)
	if tx != nil {
		_ = tx.Rollback()
	}
	return err
}

// AppendDocumentCreation is the trusted human-chat entrypoint. Ordinary Append
// deliberately cannot grant authority from Message.Metadata. The transcript is
// synced after the writer fence and replay check; a later SQL failure preserves
// that message but admits no run.
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
	tx, err := s.documentCreationTransaction(ctx, session, msg, intent, receipt)
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
	if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,tool_summary,ts,author_user_id,run_id) VALUES(?,?,?,?,?,?,?,?,?)`, msg.ID, session, msg.AgentID, string(msg.Role), msg.Content, msg.ToolSummary, msg.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), msg.AuthorUserID, msg.RunID); err != nil {
		return fmt.Errorf("insert document creation message: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at,drafting_allowed) VALUES(?,?,?,?,?,?,?)`, msg.ID, receipt.WorkspaceID, receipt.UserID, session, intent.SubmissionID, msg.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), intent.Drafting); err != nil {
		return fmt.Errorf("insert document creation consent: %w", err)
	}
	return tx.Commit()
}

// A SQL writer lock serializes independent Store instances before any JSONL
// write. Keep DB -> file lock ordering, as used by transcript restoration.
func (s *Store) documentCreationTransaction(ctx context.Context, session string, msg Message, intent documents.CreationIntent, receipt *chataudience.Receipt) (tx *sql.Tx, err error) {
	if s.db == nil || receipt == nil || intent.Validate() != nil || msg.Role != RoleUser || msg.AuthorUserID != receipt.UserID || session != receipt.ChatID || msg.AgentID != receipt.AgentID || msg.ID == "" {
		return nil, errors.New("trusted human document creation consent required")
	}
	raw, err := json.Marshal(msg.Metadata)
	if err != nil {
		return nil, err
	}
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return nil, errors.New("document creation message metadata required")
	}
	stored, err := documents.CreationIntentFromMetadata(metadata)
	if err != nil || stored == nil || *stored != intent {
		return nil, errors.New("document creation message consent mismatch")
	}
	if err := validateID(session); err != nil {
		return nil, err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func(open *sql.Tx) {
		if err != nil {
			_ = open.Rollback()
		}
	}(tx)
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return nil, err
	}
	current, err := chataudience.CaptureTrustedQuery(ctx, tx, session, receipt.UserID, receipt)
	if err != nil || current == nil || *current != *receipt {
		return nil, errors.New("document creation human authority changed")
	}
	// Ordinary operator visibility is not consent to create private output
	// inside another human's conversation. Fence the original author in the
	// same writer transaction that persists the before-run audience seal.
	var author string
	if err = tx.QueryRowContext(ctx, `SELECT created_by FROM chats WHERE id=? AND workspace_id=? AND agent_id=? AND visibility='private'`, session, receipt.WorkspaceID, receipt.AgentID).Scan(&author); err != nil || author != receipt.UserID {
		return nil, errors.New("document creation requires the private conversation author")
	}

	var messageID, content string
	var drafting bool
	var revoked sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT c.message_id,m.content,c.drafting_allowed,c.revoked_at FROM document_creation_consents c JOIN conversation_messages m ON m.id=c.message_id WHERE c.workspace_id=? AND c.human_user_id=? AND c.chat_id=? AND c.submission_id=?`, receipt.WorkspaceID, receipt.UserID, session, intent.SubmissionID).Scan(&messageID, &content, &drafting, &revoked)
	if err == nil {
		if revoked.Valid {
			return nil, errors.New("document creation permission was revoked")
		}
		if content != msg.Content || drafting != intent.Drafting {
			return nil, ErrDocumentCreationSubmissionConflict
		}
		return nil, &DocumentCreationAlreadyAccepted{MessageID: messageID}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// An interrupted SQL mirror may leave a durable human turn without consent.
	// Streaming the authoritative file avoids loading an unbounded conversation.
	path := filepath.Join(s.basePath, "conversations", session+".jsonl")
	f, openErr := os.Open(path)
	if openErr != nil && !os.IsNotExist(openErr) {
		return nil, openErr
	}
	if f != nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			var prior Message
			if json.Unmarshal(scanner.Bytes(), &prior) != nil {
				continue
			}
			metadata, ok := prior.Metadata.(map[string]any)
			if !ok || prior.Role != RoleUser || prior.AuthorUserID != receipt.UserID {
				continue
			}
			previous, parseErr := documents.CreationIntentFromMetadata(metadata)
			if parseErr == nil && previous != nil && previous.SubmissionID == intent.SubmissionID {
				// Ordinary Append may retain untrusted creation metadata. It
				// already has a mirror row and cannot reserve a submission.
				var mirrored int
				if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE id=? AND session_id=?`, prior.ID, session).Scan(&mirrored); err != nil {
					return nil, err
				}
				if mirrored == 0 {
					return nil, ErrDocumentCreationConsentUnconfirmed
				}
			}
		}
		if err = scanner.Err(); err != nil {
			return nil, err
		}
	}
	return tx, nil
}
