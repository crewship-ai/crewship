package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func (m *craftNativeAgentManager) writer(ctx context.Context) (*sql.Tx, error) {
	tx, err := m.human.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		tx.Rollback()
		return nil, err
	}
	var synchronous int
	if err = tx.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&synchronous); err != nil || synchronous < 2 {
		tx.Rollback()
		return nil, errCraftNativeAgentDenied
	}
	return tx, nil
}

func (m *craftNativeAgentManager) assignmentEntry(a dispatch.Assignment) *craftNativeAgentEntry {
	if a.Item == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[a.Item.ID]
}

func (m *craftNativeAgentManager) checkAssignment(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry, a dispatch.Assignment) error {
	if a.Item == nil || !mooring.ValidWireIdentifier(a.RunID) || a.Generation < 1 || a.Attempt < 1 {
		return errCraftNativeAgentDenied
	}
	if err := m.checkGrant(ctx, tx, e); err != nil {
		return err
	}
	m.mu.Lock()
	oldRun, oldAttempt, oldGen := e.claimedRun, e.claimedAttempt, e.claimedGeneration
	m.mu.Unlock()
	if oldRun != "" && (oldRun != a.RunID || oldAttempt != a.Attempt || oldGen != a.Generation) {
		return errCraftNativeAgentDenied
	}
	g := e.grant
	i := a.Item
	if i.ID != e.workID || i.WorkspaceID != g.Scope.WorkspaceID || i.DomainKind != craftNativeAgentDomain || i.DomainID != g.Scope.ArtifactID || i.AgentID != g.AgentID || i.CrewID != g.CrewID || i.SessionID != g.Original.ChatID || i.AuthorizedByUserID != g.Source.UserID || i.AuthorizedScope != "pages:write" || i.SourceRef != g.MessageID || string(i.Source) != "chat" || i.InputSHA256 != e.inputSHA256 || i.TargetRevision != "whole-native" {
		return errCraftNativeAgentDenied
	}
	var workspace, agent, crew, human, chat, source, message, domain, page, scope, input, digest, target, state, class, lease string
	var generation, attempt int64
	var terminal, ended, deadline, cancel sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT w.workspace_id,w.agent_id,w.crew_id,w.authorized_by_user_id,w.session_id,w.source,w.source_ref,w.domain_kind,w.domain_id,w.authorized_scope,w.input_json,w.input_sha256,w.target_revision,w.state,w.class,w.generation,w.attempts,w.terminal_at,w.deadline_at,at.lease_expires_at,at.ended_at,at.cancel_requested_at
 FROM work_items w JOIN work_attempts at ON at.work_id=w.id AND at.generation=w.generation AND at.attempt=w.attempts WHERE w.id=? AND at.run_id=? AND length(CAST(w.input_json AS BLOB))<=?`, e.workID, a.RunID, mooring.MaxRPCFrame).Scan(&workspace, &agent, &crew, &human, &chat, &source, &message, &domain, &page, &scope, &input, &digest, &target, &state, &class, &generation, &attempt, &terminal, &deadline, &lease, &ended, &cancel)
	hash := sha256.Sum256([]byte(input))
	if err != nil || workspace != i.WorkspaceID || agent != i.AgentID || crew != i.CrewID || human != i.AuthorizedByUserID || chat != i.SessionID || source != string(i.Source) || message != i.SourceRef || domain != i.DomainKind || page != i.DomainID || scope != i.AuthorizedScope || target != i.TargetRevision || digest != e.inputSHA256 || input != i.InputJSON || hex.EncodeToString(hash[:]) != digest || state != "starting" || class != "chat" || generation != a.Generation || attempt != int64(a.Attempt) || terminal.Valid || ended.Valid || cancel.Valid {
		return errCraftNativeAgentDenied
	}
	for _, s := range []string{lease, deadline.String} {
		expiry, err := time.Parse(time.RFC3339Nano, s)
		if err != nil || !time.Now().Before(expiry) {
			return errCraftNativeAgentDenied
		}
	}
	return nil
}

func (m *craftNativeAgentManager) Authorize(ctx context.Context, a dispatch.Assignment) (dispatch.Decision, error) {
	e := m.assignmentEntry(a)
	if e == nil {
		return dispatch.Decision{}, errCraftNativeAgentDenied
	}
	tx, err := m.writer(ctx)
	if err != nil {
		return dispatch.Decision{}, err
	}
	defer tx.Rollback()
	if err = m.checkAssignment(ctx, tx, e, a); err != nil {
		return dispatch.Decision{}, err
	}
	if err = tx.Commit(); err != nil {
		return dispatch.Decision{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e.closed || m.entries[e.workID] != e || e.claimedRun != "" && (e.claimedRun != a.RunID || e.claimedAttempt != a.Attempt || e.claimedGeneration != a.Generation) {
		return dispatch.Decision{}, errCraftNativeAgentDenied
	}
	e.claimedRun, e.claimedAttempt, e.claimedGeneration = a.RunID, a.Attempt, a.Generation
	return dispatch.Allow(), nil
}
