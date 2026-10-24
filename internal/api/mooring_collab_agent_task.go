package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

// StartTask is a dormant actual-human task producer. It always inserts a NEW
// host-generated work ID through the existing ledger; callers cannot nominate
// an old queued work row, run ID, agent identity or role in a body.
//
// Caller must acquire the shared ingress lease BEFORE this writer transaction,
// roll back on every error, retain the lease through commit/response, and keep
// the returned pending handle only in trusted host memory. No queued/accepted
// response precedes SQL commit. No route or dispatcher uses this method yet.
func (s *collaborationAgentMandateStore) StartTask(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, intent, message string, receipt *chataudience.Receipt) (*collaborationAgentPending, work.Receipt, error) {
	var accepted work.Receipt
	if s == nil || tx == nil || receipt == nil {
		return nil, accepted, errCollaborationAgentAccess
	}
	// Disabled stores reject before touching SQL or a missing dependency.
	s.mu.Lock()
	boot, tick, e := s.now()
	if e == nil {
		s.pruneLocked(boot, tick)
		if len(s.pending)+len(s.attached) >= s.capacity {
			e = errCollaborationAgentAccess
		}
	}
	s.mu.Unlock()
	if e != nil {
		return nil, accepted, e
	}
	stamp, e := s.fence.capture(ctx)
	if e != nil {
		return nil, accepted, e
	}
	release, e := s.fence.acquire(ctx, stamp)
	if e != nil {
		return nil, accepted, e
	}
	defer release()
	source := auth.OriginalCredentialFromContext(ctx)
	if source == nil || receipt.OriginalCredential() != *source || receipt.WorkspaceID != key.WorkspaceID || !mooring.ValidWireIdentifier(message) {
		return nil, accepted, errCollaborationAgentAccess
	}
	current, e := chataudience.RevalidateOriginalUserQuery(ctx, tx, receipt)
	if e != nil || current == nil || *current != *receipt {
		return nil, accepted, errCollaborationAgentAccess
	}
	proof, e := s.pages.authorizeCollaborationHumanPage(ctx, tx, key, intent)
	write, valid := collaborationAgentIntent(intent)
	if e != nil || !valid || proof.record == nil || proof.record.PrivateAudience {
		return nil, accepted, errCollaborationAgentAccess
	}
	var crew, instruction, revision string
	e = tx.QueryRowContext(ctx, `SELECT a.crew_id,m.content,d.revision FROM agents a
 JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
 JOIN conversation_messages m ON m.agent_id=a.id AND m.id=? AND m.session_id=? AND m.author_user_id=? AND m.role='user'
 JOIN mooring_collab_documents d ON d.workspace_id=a.workspace_id AND d.page_id=? AND d.application_id='wordcraft'
 WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL
 AND length(CAST(m.content AS BLOB))<=?`, message, receipt.ChatID, source.UserID, key.ArtifactID, receipt.AgentID, key.WorkspaceID, mooring.MaxRPCFrame).Scan(&crew, &instruction, &revision)
	if e != nil || !mooring.ValidWireIdentifier(crew) || !utf8.ValidString(instruction) || strings.TrimSpace(instruction) == "" || len(instruction) > mooring.MaxRPCFrame {
		return nil, accepted, errCollaborationAgentAccess
	}
	number, e := strconv.ParseUint(revision, 10, 64)
	if e != nil || strconv.FormatUint(number, 10) != revision {
		return nil, accepted, errCollaborationAgentAccess
	}
	pageCtx := context.WithValue(ctx, pageAuthReaderKey{}, tx)
	if !s.pages.artifactAgentPolicyAllows(pageCtx, key.WorkspaceID, crew) || !collaborationAgentGrantAllows(pageCtx, s.pages, key.WorkspaceID, proof.record, receipt.AgentID, write) {
		return nil, accepted, errCollaborationAgentAccess
	}
	// Routing fields are explicit host values. User text stays opaque instruction
	// data; actor/run/credential metadata inside that text has no authority.
	input, e := json.Marshal(struct {
		Application string `json:"application_id"`
		Page        string `json:"page_id"`
		Chat        string `json:"chat_id"`
		Message     string `json:"message_id"`
		Instruction string `json:"instruction"`
	}{key.ApplicationID, key.ArtifactID, receipt.ChatID, message, instruction})
	if e != nil || len(input) > mooring.MaxRPCFrame {
		return nil, accepted, errCollaborationAgentAccess
	}
	digest := sha256.Sum256(input)
	scope := "pages:read"
	if write {
		scope = "pages:write"
	}
	deadline := collaborationAgentEarlier(proof.credentialExpiry, time.Now().Add(s.ttl))
	accepted, e = work.NewStore(s.pages.db).AcceptTx(ctx, tx, work.AcceptRequest{
		WorkspaceID: key.WorkspaceID, Source: work.SourceChat, SourceRef: message, DomainKind: "mooring_wordcraft_task", DomainID: key.ArtifactID,
		AgentID: receipt.AgentID, CrewID: crew, SessionID: receipt.ChatID, Class: work.ClassChat, AuthorizedByUserID: source.UserID, AuthorizedScope: scope,
		InputJSON: string(input), InputSHA256: hex.EncodeToString(digest[:]), TargetRevision: revision, DeadlineAt: deadline,
	})
	if e != nil {
		return nil, work.Receipt{}, e
	}
	pending, e := s.prepareAcceptedTask(ctx, tx, key, intent, message, receipt, collaborationAgentFreshWork{seal: s.workSeal, workID: accepted.WorkID, boot: boot, expiresTick: tick + int64(s.ttl)})
	if e != nil {
		return nil, work.Receipt{}, e
	}
	return pending, accepted, nil
}
