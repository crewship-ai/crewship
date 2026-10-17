package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

// bindDocumentCreationRun admits only a consent previously persisted by the
// explicit trusted-human conversation entrypoint, never generic run metadata.
func bindDocumentCreationRun(r *http.Request, db *sql.DB, run, message string, receipt *chataudience.Receipt, intent documents.CreationIntent) error {
	if receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return errors.New("host human document creation admission required")
	}
	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return err
	}
	if err := bindDocumentCreationRunTx(r, db, tx, run, message, receipt, intent); err != nil {
		return err
	}
	return tx.Commit()
}

func bindDocumentCreationRunTx(r *http.Request, db *sql.DB, tx *sql.Tx, run, message string, receipt *chataudience.Receipt, intent documents.CreationIntent) error {
	if tx == nil || receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return errors.New("host human document creation transaction required")
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	current, err := chataudience.CaptureTrustedQuery(ctx, tx, receipt.ChatID, receipt.UserID, receipt)
	if err != nil || current == nil || *current != *receipt {
		return errors.New("document creation human admission changed")
	}
	var a artifactRunAuthority
	var expires string
	if err := tx.QueryRowContext(ctx, `SELECT run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND agent_id=? AND human_user_id=? AND chat_id=? AND message_id=? AND slides_request_origin IS NULL`, run, receipt.WorkspaceID, receipt.AgentID, receipt.UserID, receipt.ChatID, message).Scan(&a.RunID, &a.WorkspaceID, &a.AgentID, &a.CrewID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expires); err != nil {
		return errors.New("document creation run principal changed")
	}
	h := &PageHandler{db: db}
	if err := h.checkDocumentCreationRunScope(ctx, &a); err != nil {
		return err
	}
	if h.checkArtifactRunPrincipal(ctx, &a) != nil || !h.artifactPrincipalMayCreate(ctx, a.WorkspaceID, a.HumanUserID) || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return errors.New("document creation authority denied")
	}
	// Both the original SQL mirror and its consent must agree. Rebinding an
	// already used submission to a different run/message is never an automatic retry.
	var storedRun sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT c.run_id FROM document_creation_consents c JOIN conversation_messages m ON m.id=c.message_id AND m.session_id=c.chat_id AND m.author_user_id=c.human_user_id AND m.agent_id=? AND m.role='user' WHERE c.message_id=? AND c.workspace_id=? AND c.human_user_id=? AND c.chat_id=? AND c.submission_id=? AND c.revoked_at IS NULL`, receipt.AgentID, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID).Scan(&storedRun); err != nil || (storedRun.Valid && storedRun.String != run) {
		return errors.New("original document creation consent missing or changed")
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_creation_consents SET run_id=?,expires_at=? WHERE message_id=? AND (run_id IS NULL OR run_id=?)`, run, expires, message, run)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("document creation binding conflict")
	}
	return nil
}
