package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/documents"
)

var errDocumentIterationProposalUnavailable = errors.New("shared Document changes require proposals; proposal delivery is unavailable")

// AuthorizeDocumentIteration seals the original human's private chat before
// returning any target context to the trusted preappend send boundary.
func (h *PageHandler) AuthorizeDocumentIteration(ctx context.Context, receipt *chataudience.Receipt, intent documents.IterationIntent) (chatbridge.PageChatContext, error) {
	var empty chatbridge.PageChatContext
	if receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" {
		return empty, errDocumentAccess
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return empty, err
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	check := func() (int64, error) {
		current, err := chataudience.CaptureTrustedQuery(ctx, tx, receipt.ChatID, receipt.UserID, receipt)
		if err != nil || current == nil || *current != *receipt {
			return 0, errDocumentAccess
		}
		var crew string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(a.crew_id,'') FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL AND a.expired_at IS NULL WHERE c.id=? AND c.workspace_id=? AND c.agent_id=? AND c.created_by=? AND c.visibility='private'`, receipt.ChatID, receipt.WorkspaceID, receipt.AgentID, receipt.UserID).Scan(&crew)
		if err != nil || !h.artifactAgentPolicyAllows(ctx, receipt.WorkspaceID, crew) {
			return 0, errDocumentAccess
		}
		_, version, err := h.documentIterationPage(ctx, receipt.WorkspaceID, receipt.UserID, intent)
		return version, err
	}
	version, err := check()
	if err != nil {
		return empty, err
	}
	if err = sealDocumentIterationChat(ctx, tx, receipt); err != nil {
		return empty, err
	}
	// Recheck after the seal too; no target metadata may escape a rejected tx.
	if final, err := check(); err != nil {
		return empty, err
	} else if final != version {
		return empty, errDocumentAccess
	}
	page := chatbridge.PageChatContext{WorkspaceID: receipt.WorkspaceID, PageID: intent.PageID}
	if err = tx.QueryRowContext(ctx, `SELECT slug,name FROM pages WHERE workspace_id=? AND id=?`, receipt.WorkspaceID, intent.PageID).Scan(&page.Slug, &page.Name); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	page.SnapshotAt = time.Now().UTC().Format(time.RFC3339)
	return page, nil
}

func decodeDocumentIterationIntent(raw []byte) (documents.IterationIntent, error) {
	return documents.DecodeIterationIntent(raw)
}

func sealDocumentIterationChat(ctx context.Context, tx *sql.Tx, receipt *chataudience.Receipt) error {
	sealed, err := tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=? AND workspace_id=? AND agent_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, receipt.UserID, receipt.ChatID, receipt.WorkspaceID, receipt.AgentID, receipt.UserID, receipt.UserID)
	if err != nil {
		return err
	}
	count, err := sealed.RowsAffected()
	if err != nil || count != 1 {
		return errDocumentAccess
	}
	return nil
}

// Only personal, human-owned private Documents have DIRECT in this delivery.
// Recheck the original immutable target, not a later slug-selected replacement.
func (h *PageHandler) documentIterationPage(ctx context.Context, workspace, human string, intent documents.IterationIntent) (documentDraftScope, int64, error) {
	var scope documentDraftScope
	var slug string
	var schema int
	if intent.Validate() != nil {
		return scope, 0, errDocumentAccess
	}
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT p.slug,d.schema_version,d.generation,d.head_rev FROM pages p JOIN documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=?`, intent.PageID, workspace).Scan(&slug, &schema, &scope.Generation, &scope.Rev)
	if err != nil {
		return scope, 0, errDocumentAccess
	}
	scope.PageID = intent.PageID
	role, err := h.liveContentHumanRole(ctx, workspace, human)
	if err != nil {
		return scope, 0, errDocumentAccess
	}
	// This human was captured by the host receipt or live run principal.
	// Do not inherit an agent's unrelated request user when reading private data.
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: human})
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, err := h.loadPage(ctx, workspace, slug)
	if err != nil || rec.ID != intent.PageID || rec.OwnerUserID != human || !h.mayEditSpec(ctx, workspace, human, role, rec) {
		return scope, 0, errDocumentAccess
	}
	if !rec.PrivateAudience || rec.FolderID != "" || rec.OwnerCrewID != "" {
		return scope, 0, errDocumentIterationProposalUnavailable
	}
	if rec.HasProject || rec.HasApplication {
		return scope, 0, errDocumentAccess
	}
	var panels int
	if h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT count(*) FROM page_panels WHERE page_id=?`, rec.ID).Scan(&panels) != nil || panels != 0 {
		return scope, 0, errDocumentAccess
	}
	if schema != intent.Schema || scope.Generation != intent.Generation || scope.Rev != intent.Rev {
		return scope, 0, documents.ErrConflict
	}
	return scope, rec.PagesVersion, nil
}

// Called by the trusted human send boundary after SyncSession and successful
// message mirroring, before dispatch. Ordinary message metadata calls nothing.
func (h *PageHandler) persistDocumentIterationIntent(r *http.Request, receipt *chataudience.Receipt, message string, intent documents.IterationIntent) error {
	if receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return errDocumentAccess
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	current, err := chataudience.CaptureTrustedQuery(ctx, tx, receipt.ChatID, receipt.UserID, receipt)
	if err != nil || current == nil || *current != *receipt {
		return errDocumentAccess
	}
	var instruction, crew string
	err = tx.QueryRowContext(ctx, `SELECT m.content,a.crew_id FROM conversation_messages m JOIN chats c ON c.id=m.session_id AND c.workspace_id=? AND c.created_by=m.author_user_id AND c.visibility='private' JOIN agents a ON a.id=m.agent_id AND a.workspace_id=c.workspace_id WHERE m.id=? AND m.session_id=? AND m.author_user_id=? AND m.agent_id=? AND m.role='user'`, receipt.WorkspaceID, message, receipt.ChatID, receipt.UserID, receipt.AgentID).Scan(&instruction, &crew)
	if err != nil || strings.TrimSpace(instruction) == "" || !h.artifactAgentPolicyAllows(ctx, receipt.WorkspaceID, crew) {
		return errDocumentAccess
	}
	currentIntent := intent
	var cursor int64
	existingErr := tx.QueryRowContext(ctx, `SELECT draft_rev FROM document_iteration_intents WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND page_id=? AND schema_version=? AND document_generation=? AND original_rev=? AND revoked_at IS NULL`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, intent.PageID, intent.Schema, intent.Generation, intent.Rev).Scan(&cursor)
	if existingErr == nil {
		currentIntent.Rev = cursor
	} else if existingErr != sql.ErrNoRows {
		return existingErr
	}
	_, version, err := h.documentIterationPage(ctx, receipt.WorkspaceID, receipt.UserID, currentIntent)
	if err != nil {
		return err
	}
	// Seal copied prompt/results before any run can read this private Page.
	// The existing retained marker also protects diagnostics after this grant ends.
	if err = sealDocumentIterationChat(ctx, tx, receipt); err != nil {
		return err
	}
	var policy int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1`, receipt.WorkspaceID).Scan(&policy); err != nil {
		return errDocumentAccess
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_iteration_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,draft_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(message_id) DO NOTHING`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, intent.PageID, intent.Schema, intent.Generation, intent.Rev, intent.Rev, version, policy, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision)
	if err != nil {
		return err
	}
	var same int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM document_iteration_intents WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND page_id=? AND schema_version=? AND document_generation=? AND original_rev=? AND pages_version=? AND policy_version=? AND member_id=? AND member_revision=? AND chat_generation=? AND chat_revision=? AND revoked_at IS NULL`, message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, intent.SubmissionID, intent.PageID, intent.Schema, intent.Generation, intent.Rev, version, policy, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision).Scan(&same)
	if err != nil || same != 1 {
		return errDocumentAccess
	}
	return tx.Commit()
}

// Join this to the existing transactional run start, after captureArtifactChatPrincipalQuery.
func bindDocumentIterationRunTx(r *http.Request, db *sql.DB, tx *sql.Tx, run, message string, receipt *chataudience.Receipt, intent documents.IterationIntent) error {
	if tx == nil || receipt == nil || intent.Validate() != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return errDocumentAccess
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	h := &PageHandler{db: db}
	var a artifactRunAuthority
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND agent_id=? AND human_user_id=? AND chat_id=? AND message_id=? AND member_id=? AND member_revision=? AND chat_generation=? AND chat_revision=? AND slides_request_origin IS NULL`, run, receipt.WorkspaceID, receipt.AgentID, receipt.UserID, receipt.ChatID, message, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision).Scan(&a.RunID, &a.WorkspaceID, &a.AgentID, &a.CrewID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expiry)
	if err != nil || h.checkArtifactRunPrincipal(ctx, &a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return errDocumentAccess
	}
	var creation int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM document_creation_consents WHERE run_id=?`, run).Scan(&creation) != nil || creation != 0 {
		return errDocumentAccess
	}
	var bound sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT run_id FROM document_iteration_intents WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND page_id=? AND schema_version=? AND document_generation=? AND original_rev=? AND member_id=? AND member_revision=? AND chat_generation=? AND chat_revision=? AND revoked_at IS NULL`, message, a.WorkspaceID, a.HumanUserID, a.ChatID, intent.SubmissionID, intent.PageID, intent.Schema, intent.Generation, intent.Rev, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision).Scan(&bound)
	if err != nil || (bound.Valid && bound.String != run) {
		return errDocumentAccess
	}
	if bound.Valid {
		// Lost admission ACK does not reset the cursor or renew its expiry.
		_, err = h.documentIterationScope(ctx, &a, intent.PageID)
		return err
	}
	_, version, err := h.documentIterationPage(ctx, a.WorkspaceID, a.HumanUserID, intent)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_iteration_intents SET run_id=?,expires_at=? WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND submission_id=? AND page_id=? AND schema_version=? AND document_generation=? AND original_rev=? AND draft_rev=original_rev AND pages_version=? AND member_id=? AND member_revision=? AND chat_generation=? AND chat_revision=? AND revoked_at IS NULL AND (run_id IS NULL OR run_id=?) AND policy_version=(SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1)`, run, expiry, message, a.WorkspaceID, a.HumanUserID, a.ChatID, intent.SubmissionID, intent.PageID, intent.Schema, intent.Generation, intent.Rev, version, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision, run, a.WorkspaceID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errDocumentAccess
	}
	return nil
}

func (h *PageHandler) documentIterationScope(ctx context.Context, a *artifactRunAuthority, page string) (documentDraftScope, error) {
	var scope documentDraftScope
	if a == nil || h.checkArtifactRunPrincipal(ctx, a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return scope, errDocumentAccess
	}
	var intent documents.IterationIntent
	var version, policy, currentPolicy int64
	var expiry string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT i.submission_id,i.page_id,i.schema_version,i.document_generation,i.draft_rev,i.pages_version,i.policy_version,p.version,i.expires_at FROM document_iteration_intents i JOIN workspace_artifact_creation_policy p ON p.workspace_id=i.workspace_id AND p.enabled=1 JOIN artifact_run_principals a ON a.run_id=i.run_id AND a.member_id=i.member_id AND a.member_revision=i.member_revision AND a.chat_generation=i.chat_generation AND a.chat_revision=i.chat_revision JOIN conversation_messages m ON m.id=i.message_id AND m.session_id=i.chat_id AND m.author_user_id=i.human_user_id AND m.agent_id=a.agent_id AND m.role='user' JOIN chats c ON c.id=i.chat_id AND c.created_by=i.human_user_id AND c.visibility='private' AND c.private_artifact_author_user_id=i.human_user_id WHERE i.run_id=? AND i.message_id=? AND i.workspace_id=? AND i.human_user_id=? AND i.chat_id=? AND i.revoked_at IS NULL`, a.RunID, a.MessageID, a.WorkspaceID, a.HumanUserID, a.ChatID).Scan(&intent.SubmissionID, &intent.PageID, &intent.Schema, &intent.Generation, &intent.Rev, &version, &policy, &currentPolicy, &expiry)
	deadline, parseErr := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || parseErr != nil || !time.Now().Before(deadline) || policy != currentPolicy || (page != "" && page != intent.PageID) {
		return scope, errDocumentAccess
	}
	scope, currentVersion, err := h.documentIterationPage(ctx, a.WorkspaceID, a.HumanUserID, intent)
	if err != nil {
		return scope, err
	}
	if currentVersion != version {
		return scope, documents.ErrConflict
	}
	return scope, nil
}

func (h *PageHandler) documentIterationGuard(a *artifactRunAuthority, k documents.Key, operation string, ifRev int64) documents.MutationGuard {
	return func(ctx context.Context, tx *sql.Tx, actor string, state documents.State) error {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		scope, err := h.documentIterationScope(ctx, a, k.PageID)
		if err != nil {
			return err
		}
		if k.WorkspaceID != a.WorkspaceID || actor != "agent:"+a.AgentID+":"+a.RunID || ifRev != scope.Rev || state.HeadRev != scope.Rev || state.Generation != scope.Generation {
			return documents.ErrConflict
		}
		if operation == "checkpoint" {
			return nil
		}
		if operation != "commit" {
			return errDocumentAccess
		}
		result, err := tx.ExecContext(ctx, `UPDATE document_iteration_intents SET draft_rev=draft_rev+1 WHERE run_id=? AND page_id=? AND document_generation=? AND draft_rev=? AND revoked_at IS NULL`, a.RunID, k.PageID, scope.Generation, scope.Rev)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return documents.ErrConflict
		}
		return nil
	}
}

// Dispatch only a retained, bound iteration intent. Absence preserves the
// creation-only path; unsupported installed schemas fail closed.
func (h *PageHandler) documentIterationRun(ctx context.Context, a *artifactRunAuthority) (bool, error) {
	if a == nil {
		return false, errDocumentAccess
	}
	columns, present, err := documentCreationScopeColumns(ctx, h.pageAuthReader(ctx), "document_iteration_intents")
	if err != nil || !present {
		return false, err
	}
	for _, name := range []string{"message_id", "run_id", "page_id", "document_generation", "draft_rev"} {
		if !columns[name] {
			return false, errDocumentAccess
		}
	}
	var selected bool
	err = h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM document_iteration_intents WHERE run_id=?)`, a.RunID).Scan(&selected)
	return selected, err
}
