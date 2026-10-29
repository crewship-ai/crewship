package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Feedback is captured separately from the owner's current content base.
// The same tuple must survive every write of one admission transaction.
type artifactFeedbackIdentity struct {
	task, author string
	version      int64
}

func artifactProjectFeedbackIdentity(ctx context.Context, tx *sql.Tx, workspace string, intent artifacts.ProjectIntent) (artifactFeedbackIdentity, error) {
	var identity artifactFeedbackIdentity
	if intent.CommentID == "" {
		return identity, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT t.id,c.author,c.version FROM artifact_project_comments c JOIN users author ON author.id=c.author JOIN artifact_project_comment_tasks t ON t.id=c.task_id AND t.comment_id=c.id AND t.workspace_id=c.workspace_id AND t.page_id=c.page_id AND t.base_version=c.version WHERE c.id=? AND c.workspace_id=? AND c.page_id=?`, intent.CommentID, workspace, intent.PageID).Scan(&identity.task, &identity.author, &identity.version)
	if err != nil {
		return identity, errArtifactProjectAccess
	}
	return identity, nil
}

// The host calls this only after persisting the original human message and
// before dispatch. It is not exposed as a model-callable route.
func (h *PageHandler) RecordArtifactProjectIntent(ctx context.Context, receipt *chataudience.Receipt, message string, intent artifacts.ProjectIntent) error {
	if receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" {
		return errArtifactProjectAccess
	}
	ctx = context.WithValue(ctx, ctxWorkspaceID, receipt.WorkspaceID)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	var originalRun sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT run_id FROM artifact_project_message_intents WHERE message_id=?`, message).Scan(&originalRun); e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if err = h.artifactProjectMessageAuthority(ctx, tx, receipt, message, intent, originalRun.String); err != nil {
		return err
	}
	feedback, err := artifactProjectFeedbackIdentity(ctx, tx, receipt.WorkspaceID, intent)
	if err != nil {
		return err
	}
	var page, comment any
	if intent.PageID != "" {
		page = intent.PageID
	}
	if intent.CommentID != "" {
		comment = intent.CommentID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,comment_id,base_version,created_by_test,created_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, page, comment, intent.BaseVersion, intent.CreatedByTest, tsformat.Format(time.Now()))
	if err != nil {
		return err
	}
	if err = sealDocumentIterationChat(ctx, tx, receipt); err != nil {
		return err
	}
	if err = h.artifactProjectMessageAuthority(ctx, tx, receipt, message, intent, originalRun.String); err != nil {
		return err
	}
	finalFeedback, err := artifactProjectFeedbackIdentity(ctx, tx, receipt.WorkspaceID, intent)
	if err != nil || finalFeedback != feedback {
		return errArtifactProjectAccess
	}
	var same int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_project_message_intents WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND COALESCE(page_id,'')=? AND COALESCE(comment_id,'')=? AND base_version=? AND created_by_test=? AND revoked_at IS NULL`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, intent.PageID, intent.CommentID, intent.BaseVersion, intent.CreatedByTest).Scan(&same)
	if err != nil || same != 1 {
		return errArtifactProjectAccess
	}
	return tx.Commit()
}

func (h *PageHandler) artifactProjectMessageAuthority(ctx context.Context, tx *sql.Tx, receipt *chataudience.Receipt, message string, intent artifacts.ProjectIntent, run string) error {
	current, err := chataudience.RevalidateOriginalUserQuery(ctx, tx, receipt)
	if err != nil || current == nil || !receipt.OriginalCredential().Valid() {
		return errArtifactProjectAccess
	}
	if _, err = validateOriginalHumanCredential(ctx, tx, receipt.OriginalCredential(), "pages:write"); err != nil {
		return err
	}
	var crew string
	err = tx.QueryRowContext(ctx, `SELECT a.crew_id FROM conversation_messages m JOIN chats c ON c.id=m.session_id AND c.created_by=m.author_user_id AND c.visibility='private' JOIN agents a ON a.id=c.agent_id AND a.id=m.agent_id AND a.workspace_id=c.workspace_id WHERE m.id=? AND m.session_id=? AND m.author_user_id=? AND m.agent_id=? AND m.role='user' AND c.workspace_id=?`, message, receipt.ChatID, receipt.UserID, receipt.AgentID, receipt.WorkspaceID).Scan(&crew)
	enabled := h.artifactProjectEnabled(ctx)
	contentType := "artifact_project"
	if intent.Kind == "design" {
		enabled = h.designEnabled(ctx)
		contentType = "design"
	}
	if err != nil || !enabled || !h.artifactAgentPolicyAllows(ctx, receipt.WorkspaceID, crew) {
		return errArtifactProjectAccess
	}
	if intent.PageID == "" {
		if !h.artifactPrincipalMayCreate(ctx, receipt.WorkspaceID, receipt.UserID) {
			return errArtifactProjectAccess
		}
		return nil
	}
	var allowed int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages p JOIN artifact_version_heads v ON v.page_id=p.id AND v.workspace_id=p.workspace_id AND v.content_type=? WHERE p.id=? AND p.workspace_id=? AND p.owner_user_id=? AND p.owner_crew_id IS NULL AND v.latest_version=?`, contentType, intent.PageID, receipt.WorkspaceID, receipt.UserID, intent.BaseVersion).Scan(&allowed)
	if err != nil || allowed != 1 {
		return artifacts.ErrVersionConflict
	}
	if intent.CommentID != "" {
		// The owner's explicit current editing mandate is the latest version
		// above. Feedback keeps its original human author and immutable shared
		// task base; that author never lends a credential or owner-chat consent.
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_project_comments c JOIN users author ON author.id=c.author JOIN artifact_project_comment_tasks t ON t.id=c.task_id AND t.comment_id=c.id AND t.workspace_id=c.workspace_id AND t.page_id=c.page_id AND t.base_version=c.version WHERE c.id=? AND c.workspace_id=? AND c.page_id=? AND ((t.status='pending' AND t.active_run_id IS NULL) OR (t.status='working' AND t.active_run_id=? AND ?<>'') OR (t.status='working' AND EXISTS(SELECT 1 FROM journal_entries ended WHERE ended.workspace_id=t.workspace_id AND ended.trace_id=t.active_run_id AND ended.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop'))))`, intent.CommentID, receipt.WorkspaceID, intent.PageID, run, run).Scan(&allowed)
		if err != nil || allowed != 1 {
			return errArtifactProjectAccess
		}
	}
	return nil
}

// Run admission commits the journal start, immutable human principal and exact
// original consent together. No execution ACK precedes this transaction.
func (h *InternalHandler) admitArtifactProjectStart(r *http.Request, entry journal.Entry, receipt *chataudience.Receipt, message string, intent artifacts.ProjectIntent) (int, error) {
	if receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return 403, errArtifactProjectAccess
	}
	emitter, ok := h.journal.(journal.TransactionEmitter)
	if !ok {
		return 503, errors.New("transactional artifact admission unavailable")
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return 500, err
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	ctx = context.WithValue(ctx, ctxWorkspaceID, receipt.WorkspaceID)
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return 500, err
	}
	pages := NewPageHandler(h.db, nil, h.logger)
	if err = pages.artifactProjectMessageAuthority(ctx, tx, receipt, message, intent, entry.TraceID); err != nil {
		return 403, err
	}
	feedback, err := artifactProjectFeedbackIdentity(ctx, tx, receipt.WorkspaceID, intent)
	if err != nil {
		return 403, err
	}
	var bound sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM artifact_project_message_intents WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND COALESCE(page_id,'')=? AND COALESCE(comment_id,'')=? AND base_version=? AND created_by_test=? AND revoked_at IS NULL`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, intent.PageID, intent.CommentID, intent.BaseVersion, intent.CreatedByTest).Scan(&bound)
	if err != nil || bound.Valid && bound.String != entry.TraceID {
		return 403, errArtifactProjectAccess
	}
	matched, err := matchRunStartQuery(ctx, tx, entry)
	if err != nil {
		return 409, err
	}
	var notify func()
	if !matched {
		notify, err = emitter.AppendTx(ctx, tx, entry)
		if err != nil {
			return 500, err
		}
	}
	if err = captureOriginalUserArtifactPrincipalQuery(r, tx, entry.TraceID, entry.WorkspaceID, entry.AgentID, entry.CrewID, receipt.ChatID, message, receipt); err != nil {
		return 403, err
	}
	a := &artifactRunAuthority{RunID: entry.TraceID, WorkspaceID: entry.WorkspaceID, AgentID: entry.AgentID, CrewID: entry.CrewID, HumanUserID: receipt.UserID, ChatID: receipt.ChatID, MessageID: message}
	if err = pages.checkOriginalUserArtifactRunPrincipal(ctx, a); err != nil {
		return 403, err
	}
	var expiry string
	if err = tx.QueryRowContext(ctx, `SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, entry.TraceID).Scan(&expiry); err != nil {
		return 500, err
	}
	var page any
	if intent.PageID != "" {
		page = intent.PageID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,result_page_id,created_at,source_kind) VALUES(?,?,?,?,?,?,?,?,'chat') ON CONFLICT(run_id) DO NOTHING`, entry.TraceID, entry.WorkspaceID, receipt.UserID, receipt.ChatID, message, expiry, page, tsformat.Format(time.Now()))
	if err != nil {
		return 500, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE artifact_project_message_intents SET run_id=? WHERE message_id=? AND (run_id IS NULL OR run_id=?)`, entry.TraceID, message, entry.TraceID)
	if err != nil {
		return 500, err
	}
	if err = pages.checkArtifactProjectRunConsent(ctx, a, intent.PageID); err != nil {
		return 403, err
	}
	if intent.CommentID != "" {
		result, e := tx.ExecContext(ctx, `UPDATE artifact_project_comment_tasks SET status='working',active_run_id=? WHERE comment_id=? AND workspace_id=? AND page_id=? AND EXISTS(SELECT 1 FROM artifact_project_comments feedback JOIN users author ON author.id=feedback.author WHERE feedback.id=artifact_project_comment_tasks.comment_id AND feedback.task_id=artifact_project_comment_tasks.id AND feedback.workspace_id=artifact_project_comment_tasks.workspace_id AND feedback.page_id=artifact_project_comment_tasks.page_id AND feedback.version=artifact_project_comment_tasks.base_version) AND ((status='pending' AND active_run_id IS NULL) OR (status='working' AND active_run_id=?) OR (status='working' AND EXISTS(SELECT 1 FROM journal_entries ended WHERE ended.workspace_id=artifact_project_comment_tasks.workspace_id AND ended.trace_id=artifact_project_comment_tasks.active_run_id AND ended.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop'))))`, entry.TraceID, intent.CommentID, entry.WorkspaceID, intent.PageID, entry.TraceID)
		if e != nil {
			return 500, e
		}
		if n, e := result.RowsAffected(); e != nil || n != 1 {
			return 403, errArtifactProjectAccess
		}
	}
	// Task assignment is the final write and can change the source mandate.
	// Revalidate the complete original authority before any run becomes visible.
	if err = pages.artifactProjectMessageAuthority(ctx, tx, receipt, message, intent, entry.TraceID); err != nil {
		return 403, err
	}
	if err = pages.checkArtifactProjectRunConsent(ctx, a, intent.PageID); err != nil {
		return 403, err
	}
	finalFeedback, err := artifactProjectFeedbackIdentity(ctx, tx, receipt.WorkspaceID, intent)
	if err != nil || finalFeedback != feedback {
		return 403, errArtifactProjectAccess
	}
	if err = tx.Commit(); err != nil {
		return 500, err
	}
	if notify != nil {
		notify()
	}
	return 0, nil
}
