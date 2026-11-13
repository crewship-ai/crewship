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
// persists the human message: the same rules as RecordArtifactRefs. Access
// lost between this check and the record still refuses the record; the
// stored message then has no references and starts no run.
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
WHERE m.workspace_id=? AND m.user_id=? AND m.access_mode IS NOT 'restricted'`, workspace, userID).Scan(&role)
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

// ArtifactRefsEnabled reports whether the workspace has agent access modes
// on. With it off the chat bridge ignores `artifact_refs` metadata entirely,
// exactly as before the flag existed.
func (h *PageHandler) ArtifactRefsEnabled(ctx context.Context, workspace string) (bool, error) {
	return featureflags.IsEnabledWith(ctx, h.db, workspace, ArtifactAgentAccessModesFlag)
}

// errArtifactRefsDisabled means the workspace flag is off: the run is started
// the ordinary way and nothing is bound.
var errArtifactRefsDisabled = errors.New("artifact agent access modes are off")

// messageHasArtifactRefs reports whether the trusted recorder kept references
// for this message and the workspace still has the flag on. A kill switch
// returns runs to the ordinary path instead of failing them.
func messageHasArtifactRefs(ctx context.Context, q pageAuthQueryer, workspace, message string) (bool, error) {
	if message == "" {
		return false, nil
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM artifact_message_refs WHERE message_id=? AND workspace_id=?`, message, workspace).Scan(&n); err != nil || n == 0 {
		return false, err
	}
	return featureflags.IsEnabledWith(ctx, q, workspace, ArtifactAgentAccessModesFlag)
}

// admitArtifactRefsRun is CreateRun's branch for a referenced human message.
// It reports admitted=false, with no error, when the message has no
// references or the flag is off, and the caller starts the run normally.
func (h *InternalHandler) admitArtifactRefsRun(r *http.Request, entry journal.Entry, triggerType, originID string, receipt *chataudience.Receipt, workspace, message string) (bool, int, error) {
	hasRefs, err := messageHasArtifactRefs(r.Context(), h.db, workspace, message)
	if err != nil {
		return false, http.StatusInternalServerError, err
	}
	if !hasRefs {
		return false, 0, nil
	}
	if triggerType != "USER" || originID != "" || receipt == nil {
		return false, http.StatusForbidden, errArtifactRefUnavailable
	}
	status, err := h.admitArtifactRefsStart(r, entry, receipt, message)
	if errors.Is(err, errArtifactRefsDisabled) {
		return false, 0, nil
	}
	if err != nil {
		return false, status, err
	}
	return true, 0, nil
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
		return 0, errArtifactRefsDisabled
	}
	// An exact retry of an admitted start is idempotent, as on the plain path.
	// Otherwise the references are checked again: a human who lost read
	// access since sending does not hand it to the run.
	matched, err := matchRunStartQuery(ctx, tx, entry)
	if err != nil {
		if errors.Is(err, errRunStartIdentityConflict) || errors.Is(err, errRunAlreadyTerminal) {
			return http.StatusConflict, err
		}
		return http.StatusInternalServerError, err
	}

	ctx = context.WithValue(ctx, ctxWorkspaceID, receipt.WorkspaceID)
	rows, err := tx.QueryContext(ctx, `SELECT page_id FROM artifact_message_refs WHERE message_id=? AND workspace_id=? AND human_user_id=? AND chat_id=?`,
		message, receipt.WorkspaceID, receipt.UserID, receipt.ChatID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	var refs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return http.StatusInternalServerError, err
		}
		refs = append(refs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return http.StatusInternalServerError, err
	}
	if len(refs) == 0 {
		return http.StatusForbidden, errArtifactRefUnavailable
	}
	pages := NewPageHandler(h.db, nil, h.logger)
	viewer, err := pages.delegableViewer(ctx, receipt.WorkspaceID, receipt.UserID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	for _, id := range refs {
		if matched {
			break
		}
		ok, err := pages.delegableRead(ctx, receipt.WorkspaceID, id, viewer)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		if !ok {
			return http.StatusForbidden, errArtifactRefUnavailable
		}
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
	if int(n) != len(refs) && !matched {
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
