package api

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type collaborationAgentTaskRegistrationKey struct{}

// This proof exists only around one original store call. It is never returned
// to a caller, accepted from JSON, or reconstructed from a SQL work row.
type collaborationAgentTaskRegistration struct {
	registry    *collaborationSubmissionRegistry
	participant *collaborationAgentSubmissionParticipant
	owner       mooring.OwnerLease
	active      atomic.Bool
}

// Registration belongs to admission of an already confirmed original task,
// before opening its native participant. It cannot be inferred from a submit
// payload. The retained participant survives an uncertain registration result;
// a retry therefore reuses the same immutable work/participant/policy binding.
func (s *collaborationSubmissionRegistry) registerAgentTask(ctx context.Context, participant *collaborationAgentSubmissionParticipant, owner mooring.OwnerLease) error {
	if ctx == nil || s == nil || s.humans == nil || participant == nil || participant.registry != s || participant.captured.intent != "task" || s.humans.sessionStore == nil || owner.Key != s.humans.scope {
		return errCollaborationAgentAccess
	}
	if _, err := s.active(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	known := s.agents[participant.id] == participant
	s.mu.Unlock()
	if !known {
		return errCollaborationAgentAccess
	}
	operation := &collaborationAgentTaskRegistration{registry: s, participant: participant, owner: owner}
	operation.active.Store(true)
	defer operation.active.Store(false)
	bound := context.WithValue(ctx, collaborationAgentTaskRegistrationKey{}, operation)
	return s.humans.sessionStore.store.RegisterTask(bound, owner, participant.captured.workID, participant.id, participant.captured.allowPartial)
}

// Only the original immutable store calls this inside its FULL writer. Current
// rights and original work/attempt/human consent are checked on every call;
// neither an ordinary agent read nor a human operation can borrow this intent.
func (c *collaborationSessionStore) authorizeAgentTaskRegistration(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
	if c == nil || ctx == nil || tx == nil {
		return errCollaborationAgentAccess
	}
	operation, ok := ctx.Value(collaborationAgentTaskRegistrationKey{}).(*collaborationAgentTaskRegistration)
	if !ok || operation == nil || !operation.active.Load() || operation.registry == nil || operation.registry.humans == nil || operation.participant == nil ||
		operation.registry.humans.sessionStore != c || operation.participant.registry != operation.registry || operation.owner.Key != c.scope ||
		!access.FromStore(c.store) || access.Scope != c.scope || access.Intent != "task" || access.Operation != nil || access.IsCommitFence() ||
		access.TaskID != operation.participant.captured.workID || access.Participant != operation.participant.id ||
		UserFromContext(ctx) != nil || AuthKindFromContext(ctx) != "" || auth.OriginalCredentialFromContext(ctx) != nil || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil {
		return errCollaborationAgentAccess
	}
	s, p := operation.registry, operation.participant
	if _, err := s.active(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	known := s.agents[p.id] == p
	s.mu.Unlock()
	captured := p.captured
	if !known || captured.intent != "task" || captured.store == nil || captured.store.pages != c.pages || captured.store.fence != c.fence ||
		InternalTokenWorkspaceFromContext(ctx) != captured.authorVector.workspace || InternalTokenCrewFromContext(ctx) != captured.crewID {
		return errCollaborationAgentAccess
	}
	if err := c.owners.Check(ctx, tx, operation.owner); err != nil {
		return err
	}
	decision, err := captured.store.DecideCaptured(ctx, tx, captured, c.scope, "task")
	if err != nil {
		return err
	}
	if decision.kind != collaborationAgentAllowed {
		return errCollaborationAgentAccess
	}
	return nil
}
