package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCollaborationAgentAccess = errors.New("collaboration agent access denied")

// This reads identity, not authority. The enclosing dormant mandate boundary
// must also check the original human, exact run credential, current Page/app
// policy and registry incarnation in this same already-acquired writer.
// Unlike the legacy run checker, a missing attempt can never fall back to the
// journal. Only a confirmed running runtime may obtain this first agent cap.
// A canonical cancel request withdraws document authority immediately, even
// while the worker is still running and waiting for its termination signal.
func loadCollaborationAgentAttempt(ctx context.Context, tx *sql.Tx, runID string) (collaborationAgentAttemptBinding, time.Time, error) {
	var a collaborationAgentAttemptBinding
	if tx == nil || !mooring.ValidWireIdentifier(runID) {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	var state, phase, class, lease, input string
	var generation, currentAttempt int64
	var deadline, ended, terminal sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT at.run_id,at.work_id,at.attempt,at.generation,at.started_at,at.lease_owner,at.runtime_locator,
 w.workspace_id,w.agent_id,w.crew_id,w.authorized_by_user_id,w.session_id,w.source,w.source_ref,w.domain_kind,w.domain_id,w.authorized_scope,w.input_sha256,w.target_revision,
 w.state,at.runtime_phase,w.class,at.lease_expires_at,w.generation,w.attempts,w.deadline_at,at.ended_at,w.terminal_at,w.input_json
 FROM work_attempts at JOIN work_items w ON w.id=at.work_id WHERE at.run_id=? AND at.cancel_requested_at IS NULL AND length(CAST(w.input_json AS BLOB))<=?`, runID, mooring.MaxRPCFrame).Scan(
		&a.runID, &a.workID, &a.attempt, &a.generation, &a.startedAt, &a.leaseOwner, &a.runtimeLocator,
		&a.workspaceID, &a.agentID, &a.crewID, &a.humanUserID, &a.chatID, &a.source, &a.sourceRef, &a.domainKind, &a.domainID, &a.authorizedScope, &a.inputSHA256, &a.targetRevision,
		&state, &phase, &class, &lease, &generation, &currentAttempt, &deadline, &ended, &terminal, &input)
	if err != nil {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	if state != "running" || phase != "confirmed" || class != "chat" || a.source != "chat" || ended.Valid || terminal.Valid || a.generation < 1 || a.generation != generation || a.attempt < 1 || a.attempt != currentAttempt || a.leaseOwner == "" || a.runtimeLocator == "" {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	digest := sha256.Sum256([]byte(input))
	if !utf8.ValidString(input) || !json.Valid([]byte(input)) || hex.EncodeToString(digest[:]) != a.inputSHA256 {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	for _, id := range []string{a.workID, a.workspaceID, a.agentID, a.crewID, a.humanUserID, a.chatID, a.sourceRef} {
		if !mooring.ValidWireIdentifier(id) {
			return a, time.Time{}, errCollaborationAgentAccess
		}
	}
	// Parsing all identity timestamps also rejects malformed restored values;
	// equality of the copied startedAt remains an immutable binding at use time.
	started, err := time.Parse(time.RFC3339Nano, a.startedAt)
	if err != nil || started.IsZero() {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	horizon, err := time.Parse(time.RFC3339Nano, lease)
	if err != nil || !time.Now().Before(horizon) {
		return a, time.Time{}, errCollaborationAgentAccess
	}
	if deadline.Valid {
		limit, e := time.Parse(time.RFC3339Nano, deadline.String)
		if e != nil || !time.Now().Before(limit) {
			return a, time.Time{}, errCollaborationAgentAccess
		}
		if limit.Before(horizon) {
			horizon = limit
		}
	}
	return a, horizon, nil
}

func collaborationAgentIntent(intent string) (write, valid bool) {
	switch intent {
	case "read", "read_checkpoint", "outcome":
		return false, true
	case "write", "task":
		return true, true
	default:
		return false, false
	}
}

func collaborationAgentGrantAllows(ctx context.Context, h *PageHandler, workspace string, rec *pageRecord, agent string, write bool) bool {
	if rec == nil || rec.PrivateAudience {
		return false
	}
	grants, e := h.agentGrants(ctx, workspace, rec, agent)
	if e != nil {
		return false
	}
	for _, g := range grants {
		if g.Level == pageGrantWrite || !write && g.Level == pageGrantRead {
			return true
		}
	}
	return false
}

// A subject reference alone cannot call this method: only an entry created by
// actual human StartTask and consumed by the trusted dispatcher reaches it.
func (s *collaborationAgentMandateStore) checkTaskAuthority(ctx context.Context, tx *sql.Tx, task collaborationAgentTaskBinding, a collaborationAgentAttemptBinding) (collaborationHumanPageAccess, time.Time, error) {
	var proof collaborationHumanPageAccess
	if !task.authorVector.matches(task.key, task.originalCredential.UserID, a.agentID, a.crewID) {
		return proof, time.Time{}, errCollaborationAuthorUnqualified
	}
	changed, err := collaborationAuthorVectorChanged(ctx, tx, task.authorVector)
	if err != nil || changed {
		return proof, time.Time{}, errCollaborationAuthorUnqualified
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	receipt := task.originalHuman
	source := task.originalCredential
	if receipt.OriginalCredential() != source || receipt.UserID != a.humanUserID || receipt.AgentID != a.agentID || receipt.WorkspaceID != a.workspaceID || receipt.ChatID != a.chatID || task.messageID != a.sourceRef {
		return proof, time.Time{}, errCollaborationAgentAccess
	}
	// Include the complete original chat/message/source tuple, not only a run ID
	// or today's human. Never refresh a changed binding into this old mandate.
	var expiry string
	e := tx.QueryRowContext(ctx, `SELECT p.expires_at FROM artifact_run_principals p
 JOIN conversation_messages m ON m.id=p.message_id AND m.session_id=p.chat_id AND m.agent_id=p.agent_id AND m.author_user_id=p.human_user_id AND m.role='user'
 WHERE p.run_id=? AND p.workspace_id=? AND p.agent_id=? AND p.crew_id=? AND p.human_user_id=? AND p.chat_id=? AND p.message_id=?
 AND p.member_id=? AND p.member_revision=? AND p.chat_generation=? AND p.chat_revision=? AND p.user_authority_revision=?
 AND p.source_credential_kind=? AND p.source_credential_id=? AND p.source_credential_expires_at=? AND p.source_credential_binding=? AND p.slides_request_origin IS NULL`, a.runID, a.workspaceID, a.agentID, a.crewID, a.humanUserID, a.chatID, task.messageID, receipt.MemberID, receipt.MemberRevision, receipt.ChatGeneration, receipt.ChatRevision, source.UserAuthorityRevision, source.Kind, source.ID, source.ExpiresAt, source.Binding).Scan(&expiry)
	principalExpiry, parseErr := time.Parse(time.RFC3339Nano, expiry)
	if e != nil || parseErr != nil || !time.Now().Before(principalExpiry) {
		return proof, time.Time{}, errCollaborationAgentAccess
	}
	run := &artifactRunAuthority{WorkspaceID: a.workspaceID, AgentID: a.agentID, CrewID: a.crewID, RunID: a.runID, HumanUserID: a.humanUserID, ChatID: a.chatID, MessageID: task.messageID}
	if s.pages.checkOriginalUserArtifactRunPrincipal(ctx, run) != nil || !s.pages.artifactAgentPolicyAllows(ctx, a.workspaceID, a.crewID) {
		return proof, time.Time{}, errCollaborationAgentAccess
	}
	proof, e = s.pages.authorizeCollaborationPageSubject(ctx, tx, task.key, task.intent, source)
	write, valid := collaborationAgentIntent(task.intent)
	if e != nil || !valid || proof.record == nil || proof.record.PrivateAudience || proof.memberID != task.memberID || proof.memberRevision != task.memberRevision || !collaborationAgentGrantAllows(ctx, s.pages, a.workspaceID, proof.record, a.agentID, write) {
		return collaborationHumanPageAccess{}, time.Time{}, errCollaborationAgentAccess
	}
	return proof, principalExpiry, nil
}
