package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ArtifactAgentAccessModesFlag turns on agent access modes for one workspace
// (D1+D2, #5317): structured artifact references on chat messages and the
// run scope they admit. An undefined flag is off, and off changes nothing.
const ArtifactAgentAccessModesFlag = "artifact_agent_access_modes"

// MaxArtifactRefs bounds one message's references.
const MaxArtifactRefs = 10

// errArtifactRefUnavailable is the ONE refusal for a reference: a missing
// artifact, one the human cannot read as an ordinary member, one in another
// workspace, a malformed id and a disabled workspace all read the same, so a
// refusal never tells the sender that something exists.
var errArtifactRefUnavailable = errors.New("an attached artifact is not available")

var artifactRefID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CheckArtifactRefs is the read-only pre-check the chat bridge runs before it
// persists the human message: the same rules as RecordArtifactRefs, so a
// refused reference never leaves a stored message behind it.
func (h *PageHandler) CheckArtifactRefs(ctx context.Context, receipt *chataudience.Receipt, pageIDs []string) error {
	return h.admitArtifactRefs(ctx, receipt, "", pageIDs)
}

// RecordArtifactRefs is the trusted recorder. It runs in the human's own
// message admission, after the message is stored and before a model starts,
// and records each referenced artifact the human may read as an ordinary
// member. Role-only reach (a workspace ADMIN or OWNER reading what a member
// could not) is never recorded, so it never passes to an agent. An internal
// token or an agent cannot call it.
func (h *PageHandler) RecordArtifactRefs(ctx context.Context, receipt *chataudience.Receipt, message string, pageIDs []string) error {
	if message == "" {
		return errArtifactRefUnavailable
	}
	return h.admitArtifactRefs(ctx, receipt, message, pageIDs)
}

// admitArtifactRefs checks every reference against the sending human and,
// when message is set, records them in one transaction.
func (h *PageHandler) admitArtifactRefs(ctx context.Context, receipt *chataudience.Receipt, message string, pageIDs []string) error {
	if receipt == nil || len(pageIDs) == 0 || len(pageIDs) > MaxArtifactRefs ||
		InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" {
		return errArtifactRefUnavailable
	}
	seen := map[string]bool{}
	for _, id := range pageIDs {
		if !artifactRefID.MatchString(id) || seen[id] {
			return errArtifactRefUnavailable
		}
		seen[id] = true
	}
	ctx = context.WithValue(ctx, ctxWorkspaceID, receipt.WorkspaceID)
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if message != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
			return err
		}
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	enabled, err := featureflags.IsEnabledWith(ctx, tx, receipt.WorkspaceID, ArtifactAgentAccessModesFlag)
	if err != nil {
		return err
	}
	if !enabled {
		return errArtifactRefUnavailable
	}
	current, err := chataudience.RevalidateOriginalUserQuery(ctx, tx, receipt)
	if err != nil || current == nil {
		return errArtifactRefUnavailable
	}
	// Artifact runs are admitted only in the human's own private chat with
	// this agent, never in a shared or group chat.
	var own int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM chats c WHERE c.id=? AND c.workspace_id=? AND c.created_by=? AND c.agent_id=? AND c.visibility='private'`,
		receipt.ChatID, receipt.WorkspaceID, receipt.UserID, receipt.AgentID).Scan(&own); err != nil {
		return err
	}
	if own != 1 {
		return errArtifactRefUnavailable
	}
	if message != "" {
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages m WHERE m.id=? AND m.session_id=? AND m.author_user_id=? AND m.agent_id=? AND m.role='user'`,
			message, receipt.ChatID, receipt.UserID, receipt.AgentID).Scan(&own); err != nil {
			return err
		}
		if own != 1 {
			return errArtifactRefUnavailable
		}
	}
	viewer, err := h.delegableViewer(ctx, receipt.WorkspaceID, receipt.UserID)
	if err != nil {
		return err
	}
	now := tsformat.Format(time.Now().UTC())
	for _, id := range pageIDs {
		ok, err := h.delegableRead(ctx, receipt.WorkspaceID, id, viewer)
		if err != nil {
			return err
		}
		if !ok {
			return errArtifactRefUnavailable
		}
		if message == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_message_refs(message_id,workspace_id,human_user_id,chat_id,page_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(message_id,page_id) DO NOTHING`,
			message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID, id, now); err != nil {
			return err
		}
	}
	if message == "" {
		return nil
	}
	// An exact retry is the same set; a different set for one message is not.
	var recorded int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_message_refs WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=?`,
		message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID).Scan(&recorded); err != nil {
		return err
	}
	if recorded != len(pageIDs) {
		return errArtifactRefUnavailable
	}
	return tx.Commit()
}

// delegableViewer is what a human may hand to an agent: their reach as an
// ordinary member (memberViewer), never above their own role. A workspace
// VIEWER stays a VIEWER; an ADMIN or OWNER is read as a MEMBER.
func (h *PageHandler) delegableViewer(ctx context.Context, workspace, userID string) (*pageViewer, error) {
	ctx = withDelegationUser(ctx, userID)
	member, err := h.memberViewer(ctx, workspace, userID)
	if err != nil {
		return nil, err
	}
	var role string
	err = h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT m.role FROM workspace_members m JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted')`, workspace, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return &pageViewer{UserID: userID, Crews: map[string]bool{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if !canRole(role, "manage") && role != "MEMBER" && role != "MANAGER" {
		member.Role = role
	}
	return member, nil
}

// delegableRead answers whether viewer, an ordinary-member reading, may read
// the page with this id in this workspace. A page elsewhere is "no".
func (h *PageHandler) delegableRead(ctx context.Context, workspace, pageID string, viewer *pageViewer) (bool, error) {
	if viewer == nil || viewer.Role == "" || !artifactRefID.MatchString(pageID) {
		return false, nil
	}
	ctx = withDelegationUser(ctx, viewer.UserID)
	rec, err := h.loadPage(ctx, workspace, "@"+pageID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if rec.ArchivedAt != "" {
		return false, nil
	}
	panels, err := h.loadPanels(ctx, workspace, rec.ID)
	if err != nil {
		return false, err
	}
	return h.canSeePage(ctx, workspace, rec, panels, viewer)
}

// withDelegationUser names the delegating human for loaders that read the
// caller from context. It never replaces a different authenticated user.
func withDelegationUser(ctx context.Context, userID string) context.Context {
	if user := UserFromContext(ctx); user != nil {
		return ctx
	}
	return context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
}

// bindArtifactRunScopeTx copies the run's own principal message references
// into its scope, inside the admission transaction that captured the
// principal. The table trigger refuses any row that does not mirror them.
func bindArtifactRunScopeTx(ctx context.Context, tx *sql.Tx, run, workspace, message, human string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO artifact_run_scope(run_id,workspace_id,message_id,page_id)
SELECT ?,workspace_id,message_id,page_id FROM artifact_message_refs WHERE message_id=? AND workspace_id=? AND human_user_id=?
ON CONFLICT(run_id,page_id) DO NOTHING`, run, message, workspace, human)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// messageHasArtifactRefs reports whether the trusted recorder kept references
// for this message.
func messageHasArtifactRefs(ctx context.Context, q pageAuthQueryer, workspace, message string) (bool, error) {
	if message == "" {
		return false, nil
	}
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM artifact_message_refs WHERE message_id=? AND workspace_id=?`, message, workspace).Scan(&n)
	return n > 0, err
}

// admitArtifactRefsStart admits a plain chat run whose human message carries
// references: the journal start, the immutable human principal and the run
// scope commit together. Without a host human receipt nothing is bound.
func (h *InternalHandler) admitArtifactRefsStart(r *http.Request, entry journal.Entry, receipt *chataudience.Receipt, message string) (int, error) {
	if receipt == nil || message == "" || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		return http.StatusForbidden, errArtifactRefUnavailable
	}
	emitter, ok := h.journal.(journal.TransactionEmitter)
	if !ok {
		return http.StatusServiceUnavailable, errors.New("transactional run journal unavailable")
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, receipt.WorkspaceID, receipt.UserID); err != nil {
		return http.StatusInternalServerError, err
	}
	enabled, err := featureflags.IsEnabledWith(ctx, tx, receipt.WorkspaceID, ArtifactAgentAccessModesFlag)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !enabled {
		return http.StatusForbidden, errArtifactRefUnavailable
	}
	matched, err := matchRunStartQuery(ctx, tx, entry)
	if err != nil {
		if errors.Is(err, errRunStartIdentityConflict) || errors.Is(err, errRunAlreadyTerminal) {
			return http.StatusConflict, err
		}
		return http.StatusInternalServerError, err
	}
	var notify func()
	if !matched {
		if notify, err = emitter.AppendTx(ctx, tx, entry); err != nil {
			return http.StatusInternalServerError, err
		}
	}
	if err := captureArtifactChatPrincipalQuery(r, tx, entry.TraceID, entry.WorkspaceID, entry.AgentID, entry.CrewID, receipt.ChatID, message, receipt); err != nil {
		return http.StatusForbidden, err
	}
	n, err := bindArtifactRunScopeTx(ctx, tx, entry.TraceID, entry.WorkspaceID, message, receipt.UserID)
	if err != nil {
		return http.StatusForbidden, err
	}
	if n == 0 && !matched {
		return http.StatusForbidden, errArtifactRefUnavailable
	}
	if err := tx.Commit(); err != nil {
		return http.StatusInternalServerError, err
	}
	if notify != nil {
		notify()
	}
	return 0, nil
}
