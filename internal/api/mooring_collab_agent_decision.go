package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Unqualified is deliberately the zero value. It never licenses a durable
// denied outcome: absent admission, storage failure and opaque policy-helper
// errors do not establish a canonical revocation. These are host-only types.
type collaborationAgentDecisionKind uint8

const (
	collaborationAgentUnqualified collaborationAgentDecisionKind = iota
	collaborationAgentAllowed
	collaborationAgentCanonicalPermissionChanged
)

type collaborationAgentDecision struct {
	kind   collaborationAgentDecisionKind
	access collaborationAgentPageAccess
}

// DecideCaptured is a dormant trusted participant callback, never an agent
// bearer/handle endpoint. Only an original Check result can supply its private
// registry and credential binding. It never adopts a fresh scope, attempt,
// permission epoch, credential or ingress stamp for that original proof.
//
// The caller already holds the store's SQL writer and a full-operation original
// ingress lease through commit/response. Its short internal lease only checks
// the callback. Even CanonicalPermissionChanged is not a delivery revocation
// barrier or a durable outcome by itself; the enclosing store owns those.
func (s *collaborationAgentMandateStore) DecideCaptured(ctx context.Context, tx *sql.Tx, captured collaborationAgentPageAccess, key mooring.SessionKey, intent string) (collaborationAgentDecision, error) {
	var result collaborationAgentDecision
	if s == nil || tx == nil || captured.store != s {
		return result, errCollaborationAgentAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	boot, tick, err := s.now()
	if err != nil {
		return result, err
	}
	s.pruneLocked(boot, tick)
	entry, ok := s.attached[captured.mandateRef]
	if !ok || entry.binding.task.key != key {
		return result, errCollaborationAgentAccess
	}
	a := entry.binding.attempt
	task := entry.binding.task
	write, valid := collaborationAgentIntent(intent)
	capturedWrite, capturedValid := collaborationAgentIntent(captured.intent)
	allowedWrite, _ := collaborationAgentIntent(task.intent)
	if !valid || !capturedValid || capturedWrite && !allowedWrite || write && !capturedWrite || captured.agentID != a.agentID || captured.crewID != a.crewID || captured.runID != a.runID || captured.workID != a.workID || captured.attempt != a.attempt || captured.generation != a.generation || captured.credentialBinding != entry.binding.credentialSHA256 || captured.permissionVersion != task.permissionVersion || captured.memberID != task.memberID || captured.memberRevision != task.memberRevision || captured.credentialExpiry.IsZero() || !time.Now().Before(captured.credentialExpiry) {
		return result, errCollaborationAgentAccess
	}
	release, err := s.fence.acquireOriginal(task.ingress)
	if err != nil {
		return result, err
	}
	defer release()
	// Recheck the credential against the current host key without retaining a
	// raw bearer or accepting a freshly supplied replacement after rotation.
	expected := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(s.pages.artifactMasterToken, a.workspaceID, a.crewID), a.workspaceID, a.agentID, a.runID)
	if expected == "" || sha256.Sum256([]byte(expected)) != captured.credentialBinding {
		return result, errCollaborationAgentAccess
	}
	current, leaseExpiry, err := loadCollaborationAgentAttempt(ctx, tx, a.runID)
	if err != nil || current != a {
		// The older loader intentionally hides storage errors. No error from it
		// is evidence for a terminal canonical permission decision.
		return result, errCollaborationAgentAccess
	}
	principalExpiry, err := collaborationAgentCapturedRunHorizon(ctx, tx, task, a)
	if err != nil {
		return result, err
	}
	changed, err := collaborationAgentCapturedFenceChanged(ctx, tx, task)
	if err != nil {
		return result, err
	}
	bootChecked, tickChecked, err := s.now()
	horizon := collaborationAgentEarlier(captured.credentialExpiry, leaseExpiry)
	horizon = collaborationAgentEarlier(horizon, entry.binding.expiresAt)
	horizon = collaborationAgentEarlier(horizon, principalExpiry)
	if err != nil || bootChecked != boot || tickChecked >= entry.expiresTick || !time.Now().Before(horizon) {
		return result, errCollaborationAgentAccess
	}
	if changed {
		return collaborationAgentDecision{kind: collaborationAgentCanonicalPermissionChanged}, nil
	}
	page, currentPrincipalExpiry, err := s.checkTaskAuthority(ctx, tx, task, a)
	if err != nil {
		// In particular, bool Page/app-policy helpers may have hidden a failed
		// SQL read. Never translate their opaque denial into permission_revoked.
		return result, err
	}
	bootNow, tickNow, err := s.now()
	if err != nil || bootNow != boot || tickNow >= entry.expiresTick {
		return result, errCollaborationAgentAccess
	}
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, captured.credentialExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, entry.binding.expiresAt)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, principalExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, currentPrincipalExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, leaseExpiry)
	page.credentialExpiry = collaborationAgentEarlier(page.credentialExpiry, time.Now().Add(time.Duration(entry.expiresTick-tickNow)))
	if !time.Now().Before(page.credentialExpiry) {
		return result, errCollaborationAgentAccess
	}
	verified := captured
	verified.collaborationHumanPageAccess = page
	return collaborationAgentDecision{kind: collaborationAgentAllowed, access: verified}, nil
}

// A previous proof cannot turn a retired principal/chat/run into a current
// participant, even if an unrelated canonical fence also changed. Do not join
// today's human/member revision here: that is the separate known fence check.
func collaborationAgentCapturedRunHorizon(ctx context.Context, tx *sql.Tx, task collaborationAgentTaskBinding, a collaborationAgentAttemptBinding) (time.Time, error) {
	r := task.originalHuman
	source := task.originalCredential
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT p.expires_at FROM artifact_run_principals p
 JOIN chats c ON c.id=p.chat_id AND c.workspace_id=p.workspace_id AND c.agent_id=p.agent_id AND c.authority_generation=p.chat_generation AND c.authority_revision=p.chat_revision
 JOIN conversation_messages m ON m.id=p.message_id AND m.session_id=p.chat_id AND m.agent_id=p.agent_id AND m.author_user_id=p.human_user_id AND m.role='user'
 WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=? AND p.human_user_id=? AND p.chat_id=? AND p.message_id=?
 AND p.member_id=? AND p.member_revision=? AND p.chat_generation=? AND p.chat_revision=? AND p.user_authority_revision=?
 AND p.source_credential_kind=? AND p.source_credential_id=? AND p.source_credential_expires_at=? AND p.source_credential_binding=? AND p.slides_request_origin IS NULL`, a.runID, a.workspaceID, a.agentID, a.crewID, a.humanUserID, a.chatID, task.messageID, r.MemberID, r.MemberRevision, r.ChatGeneration, r.ChatRevision, source.UserAuthorityRevision, source.Kind, source.ID, source.ExpiresAt, source.Binding).Scan(&expiry)
	if err != nil {
		return time.Time{}, err
	}
	horizon, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || !time.Now().Before(horizon) {
		return time.Time{}, errCollaborationAgentAccess
	}
	var started, ended int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN entry_type='run.started' THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') THEN 1 ELSE 0 END),0) FROM journal_entries WHERE workspace_id=? AND agent_id=? AND trace_id=?`, a.workspaceID, a.agentID, a.runID).Scan(&started, &ended)
	if err != nil {
		return time.Time{}, err
	}
	if started == 0 || ended != 0 {
		return time.Time{}, errCollaborationAgentAccess
	}
	return horizon, nil
}

// Only successful scalar reads (or the known absence of the original canonical
// row) establish a changed fence. This is not a second ACL: all current Page,
// source, grant and crew policy still runs above before an allowed decision.
func collaborationAgentCapturedFenceChanged(ctx context.Context, tx *sql.Tx, task collaborationAgentTaskBinding) (bool, error) {
	var workspaceRevision string
	var userRevision, memberID, workspaceDeleted, userSuspended sql.NullString
	var memberRevision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT CAST(w.authority_revision AS TEXT),CAST(u.authority_revision AS TEXT),m.id,m.access_revision,w.deleted_at,u.suspended_at
 FROM workspaces w LEFT JOIN users u ON u.id=? LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=? WHERE w.id=?`, task.originalCredential.UserID, task.originalCredential.UserID, task.key.WorkspaceID).Scan(&workspaceRevision, &userRevision, &memberID, &memberRevision, &workspaceDeleted, &userSuspended)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !chataudience.ValidUserAuthorityRevision(workspaceRevision) || (userRevision.Valid && !chataudience.ValidUserAuthorityRevision(userRevision.String)) || (memberID.Valid && memberID.String == "") || (memberRevision.Valid && memberRevision.Int64 < 1) {
		return false, errCollaborationAgentAccess
	}
	return workspaceDeleted.Valid || userSuspended.Valid || !userRevision.Valid || !memberID.Valid || !memberRevision.Valid || workspaceRevision != task.permissionVersion || userRevision.String != task.originalCredential.UserAuthorityRevision || memberID.String != task.memberID || memberRevision.Int64 != task.memberRevision, nil
}
