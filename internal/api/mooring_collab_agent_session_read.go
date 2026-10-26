package api

import (
	"context"
	"database/sql"
	"net/http"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type collaborationAgentSessionReadKey struct{}

// Private operation lifetime, not a serialized agent/source capability. No raw
// bearer is retained after Check; the captured original proof cannot refresh.
type collaborationAgentSessionRead struct {
	cell     *collaborationSessionStore
	mandates *collaborationAgentMandateStore
	owner    mooring.OwnerLease
	captured collaborationAgentPageAccess
	active   atomic.Bool
}

// The actual HTTP ingress and live process remain held through the complete
// bounded read. This grants host checkpoint/inventory processing only. External
// delivery still needs its current recipient gate and any private-owner audit.
func (c *collaborationSessionStore) runAgentRead(r *http.Request, mandates *collaborationAgentMandateStore, handle string, owner mooring.OwnerLease, call func(context.Context) error) error {
	if c == nil || r == nil || mandates == nil || mandates.pages != c.pages || mandates.fence != c.fence || call == nil || owner.Key != c.scope || !c.operations.TryRLock() {
		return errCollaborationAgentAccess
	}
	defer c.operations.RUnlock()
	runtime, err := c.live()
	if err != nil || owner.OwnerID != runtime.RuntimeRef() {
		return errCollaborationAgentAccess
	}
	stamp, err := c.fence.capture(r.Context())
	if err != nil || stamp != c.lifecycle {
		return errCollaborationAgentAccess
	}
	release, err := c.fence.acquire(r.Context(), stamp)
	if err != nil {
		return err
	}
	defer release()
	// Capture through the existing canonical mandate boundary before invoking
	// any read work. Publication follows COMMIT; no provisional proof escapes.
	tx, err := c.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	var synchronous int
	if err = tx.QueryRowContext(r.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous < 2 {
		return errCollaborationAgentAccess
	}
	if err = c.check(r.Context(), tx); err != nil {
		return err
	}
	if err = c.owners.Check(r.Context(), tx, owner); err != nil {
		return err
	}
	captured, err := mandates.Check(r, tx, handle, c.scope, "read")
	if err != nil {
		return err
	}
	decision, err := mandates.DecideCaptured(r.Context(), tx, captured, c.scope, "read")
	if err != nil {
		return err
	}
	if decision.kind != collaborationAgentAllowed {
		return errCollaborationAgentAccess
	}
	if err = c.check(r.Context(), tx); err != nil {
		return err
	}
	if err = c.owners.Check(r.Context(), tx, owner); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = r.Context().Err(); err != nil {
		return err
	}
	operation := &collaborationAgentSessionRead{cell: c, mandates: mandates, owner: owner, captured: captured}
	operation.active.Store(true)
	defer operation.active.Store(false)
	return call(context.WithValue(r.Context(), collaborationAgentSessionReadKey{}, operation))
}

// Called only by the ORIGINAL immutable store authority inside its acquired
// FULL writer. A read operation can never borrow a human or write/outcome scope.
func (c *collaborationSessionStore) authorizeAgentRead(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
	operation, ok := ctx.Value(collaborationAgentSessionReadKey{}).(*collaborationAgentSessionRead)
	if !ok || operation == nil || operation.cell != c || !operation.active.Load() || operation.mandates == nil || operation.mandates.pages != c.pages || operation.mandates.fence != c.fence ||
		!access.FromStore(c.store) || access.Scope != c.scope || access.Intent != "read_checkpoint" || access.Operation != nil || access.TaskID != "" || access.Participant != "" ||
		UserFromContext(ctx) != nil || AuthKindFromContext(ctx) != "" || auth.OriginalCredentialFromContext(ctx) != nil || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil ||
		InternalTokenWorkspaceFromContext(ctx) != operation.captured.authorVector.workspace || InternalTokenCrewFromContext(ctx) != operation.captured.crewID {
		return errCollaborationAgentAccess
	}
	if err := c.owners.Check(ctx, tx, operation.owner); err != nil {
		return err
	}
	decision, err := operation.mandates.DecideCaptured(ctx, tx, operation.captured, c.scope, "read")
	if err != nil {
		return err
	}
	if decision.kind != collaborationAgentAllowed {
		return errCollaborationAgentAccess
	}
	return nil
}

func (c *collaborationSessionStore) agentCheckpoint(r *http.Request, mandates *collaborationAgentMandateStore, handle string, owner mooring.OwnerLease) (mooring.FullCheckpointSnapshot, error) {
	var snapshot mooring.FullCheckpointSnapshot
	err := c.runAgentRead(r, mandates, handle, owner, func(ctx context.Context) error {
		var err error
		snapshot, err = c.store.ReadFullCheckpoint(ctx, owner)
		return err
	})
	if err != nil {
		return mooring.FullCheckpointSnapshot{}, err
	}
	return snapshot, nil
}
