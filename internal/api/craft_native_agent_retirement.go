package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type craftNativeAgentStoppedKey struct{}
type craftNativeAgentStoppedProof struct {
	manager     *craftNativeAgentManager
	runtime     *craftNativeAgentRuntime
	entry       *craftNativeAgentEntry
	launch      *webhookLaunch
	exec        string
	location    orchestrator.RunLocation
	tx          *sql.Tx
	lease       mooring.EditLease
	sawOriginal bool
}

// No HTTP body, run row, generic terminal event or PID absence constructs this
// proof. It names the original registered provider exec and requires the
// existing private-exec presence check to positively observe its termination.
func (m *craftNativeAgentManager) stoppedProof(ctx context.Context, e *craftNativeAgentEntry) (*craftNativeAgentStoppedProof, error) {
	m.mu.Lock()
	rt := m.runtime
	known := e != nil && e.seal == m.seal && m.runs[e.attempt.runID] == e
	m.mu.Unlock()
	if !known || rt == nil || rt.manager != m {
		return nil, errCraftNativeAgentDenied
	}
	launch, ok := rt.loadLaunch(e.attempt.runID)
	if !ok {
		return nil, errCraftNativeAgentDenied
	}
	launch.mu.Lock()
	exec := launch.nativeExecID
	confirmed := launch.confirmed && launch.requested
	var location orchestrator.RunLocation
	if launch.location != nil {
		location = *launch.location
	}
	launch.mu.Unlock()
	if !confirmed || !mooring.ValidWireIdentifier(exec) || location.RunID != e.attempt.runID || location.ContainerID == "" || location.AgentSlug == "" {
		return nil, errCraftNativeAgentDenied
	}
	alive, err := rt.Alive(ctx, "agent-run:"+e.attempt.runID)
	if err != nil || alive {
		return nil, errCraftNativeAgentDenied
	}
	return &craftNativeAgentStoppedProof{manager: m, runtime: rt, entry: e, launch: launch, exec: exec, location: location}, nil
}

func (m *craftNativeAgentManager) checkStoppedProof(ctx context.Context, tx *sql.Tx, p *craftNativeAgentStoppedProof) error {
	if p == nil || p.manager != m || p.runtime != m.runtime || p.entry == nil {
		return errCraftNativeAgentDenied
	}
	p.launch.mu.Lock()
	same := p.launch.nativeExecID == p.exec && p.launch.location != nil && *p.launch.location == p.location && p.launch.confirmed && p.launch.requested
	p.launch.mu.Unlock()
	if !same {
		return errCraftNativeAgentDenied
	}
	if err := m.checkGrant(ctx, tx, p.entry); err != nil {
		return err
	}
	current, _, err := loadCollaborationAgentAttempt(ctx, tx, p.entry.attempt.runID)
	if err != nil || current != p.entry.attempt {
		return errCraftNativeAgentDenied
	}
	// Deliberately no runauthority.Resolve here: a terminal original run loses
	// every tool/document capability, but may retire only its own stopped lease.
	return nil
}

func (m *craftNativeAgentManager) authorizeStoppedLease(ctx context.Context, tx *sql.Tx, a mooring.EditLeaseAccess) error {
	p, _ := ctx.Value(craftNativeAgentStoppedKey{}).(*craftNativeAgentStoppedProof)
	if p == nil || p.entry == nil || p.tx != tx || a.Intent != "revoke" || a.Scope != p.lease.Scope || a.Principal != craftNativeAgentPrincipal(p.entry) {
		return errCraftNativeAgentDenied
	}
	if err := m.checkStoppedProof(ctx, tx, p); err != nil {
		return err
	}
	var actor, session string
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE workspace_id=? AND page_id=?`, a.Scope.WorkspaceID, a.Scope.ArtifactID).Scan(&actor, &session, &generation); err != nil {
		return err
	}
	if generation != p.lease.Generation {
		return errCraftNativeAgentDenied
	}
	if actor == p.lease.Holder.ActorID && session == p.lease.Holder.SessionID {
		p.sawOriginal = true
		return nil
	}
	if p.sawOriginal && actor == "" && session == "" {
		return nil
	}
	return errCraftNativeAgentDenied
}

func (m *craftNativeAgentManager) retireStoppedLease(ctx context.Context, p *craftNativeAgentStoppedProof) error {
	tx, err := m.writer(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = m.checkStoppedProof(ctx, tx, p); err != nil {
		return err
	}
	e := p.entry
	l := mooring.EditLease{Scope: e.grant.Scope}
	err = tx.QueryRowContext(ctx, `SELECT actor_id,session_id,generation FROM mooring_edit_leases WHERE workspace_id=? AND page_id=?`, l.Scope.WorkspaceID, l.Scope.ArtifactID).Scan(&l.Holder.ActorID, &l.Holder.SessionID, &l.Generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && l.Holder == craftNativeAgentPrincipal(e) {
		p.tx, p.lease = tx, l
		bound := context.WithValue(ctx, craftNativeAgentStoppedKey{}, p)
		if err = m.human.leases.Revoke(bound, tx, l.Scope, l.Holder, l.Generation); err != nil {
			return err
		}
	}
	// No acquire window between lease retirement and registry withdrawal.
	// A failed commit is also fail-closed for this old original consent.
	m.mu.Lock()
	e.closed = true
	m.mu.Unlock()
	return tx.Commit()
}
