package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Original-author evidence only: no origin, source ordinal, inventory, bearer
// serialization or issuance. A later issuer must independently qualify the
// actual store/inventory writer and burned history. No route installs this.
// self rejects zero, JSON-created and copied values; no SQL row constructs one.
type collaborationOriginAuthor struct {
	self     *collaborationOriginAuthor
	registry *collaborationSubmissionRegistry
	binding  collaborationHumanStoreBinding
	cell     *collaborationSessionStore
	owner    mooring.OwnerLease
	write    bool
	human    *collaborationHumanAdmission
	agent    *collaborationAgentPageAccess
}

func (s *collaborationSubmissionRegistry) originAuthor(ctx context.Context, owner mooring.OwnerLease) (*collaborationOriginAuthor, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.humans == nil || s.humans.pages == nil || s.humans.pages.db == nil || s.humans.owners == nil || owner.Key != s.humans.scope || owner.Generation < 1 {
		return nil, errCollaborationSubmission
	}
	operation, err := s.active(ctx)
	if err != nil {
		return nil, err
	}
	s.humans.mu.Lock()
	generation, cell := s.humans.ownerGeneration, s.humans.sessionStore
	s.humans.mu.Unlock()
	if generation != owner.Generation {
		return nil, errCollaborationSubmission
	}
	a := &collaborationOriginAuthor{registry: s, binding: operation.binding, cell: cell, owner: owner}
	a.self = a
	return a, nil
}

// Called only inside the complete original human and submission operations.
// Derive scope from that existing admission, never a caller-requested upgrade.
// Its own writer must COMMIT before returning any reference to the caller.
func (s *collaborationSubmissionRegistry) captureOriginHuman(ctx context.Context, owner mooring.OwnerLease) (*collaborationOriginAuthor, error) {
	a, err := s.originAuthor(ctx, owner)
	if err != nil {
		return nil, err
	}
	binding, ok := ctx.Value(collaborationHumanBindingKey{}).(collaborationHumanBinding)
	if !ok || binding.registry != s.humans || binding.operation == nil || !binding.operation.active.Load() {
		return nil, errCollaborationSubmission
	}
	s.humans.mu.Lock()
	admission, ok := s.humans.admissions[binding.participant]
	s.humans.mu.Unlock()
	if !ok {
		return nil, errCollaborationSubmission
	}
	a.human, a.write = &admission, admission.write
	if err = s.withWriter(ctx, func(tx *sql.Tx) error { return a.check(ctx, tx, a.write) }); err != nil {
		return nil, err
	}
	return a, nil
}

// original is an actual Check result with private mandate/credential binding,
// not a participant string, current user lookup or persisted source record.
// The exact authenticated request is checked again without adopting its newer
// proof or extending the original horizon. Read proofs stay read-only.
func (s *collaborationSubmissionRegistry) captureOriginAgent(ctx context.Context, request *http.Request, owner mooring.OwnerLease, original collaborationAgentPageAccess) (*collaborationOriginAuthor, error) {
	a, err := s.originAuthor(ctx, owner)
	if err != nil {
		return nil, err
	}
	write, valid := collaborationAgentIntent(original.intent)
	if !valid || request == nil || original.store == nil || original.store.pages != s.humans.pages || original.store.pages.db != s.humans.pages.db || original.store.fence != s.humans.fence {
		return nil, errCollaborationSubmission
	}
	a.agent, a.write = &original, write
	err = s.withWriter(ctx, func(tx *sql.Tx) error {
		if err := a.check(ctx, tx, write); err != nil {
			return err
		}
		current, err := original.store.Check(request.WithContext(ctx), tx, original.mandateRef, owner.Key, original.intent)
		if err != nil {
			return err
		}
		if current.store != original.store || current.mandateRef != original.mandateRef || current.intent != original.intent || current.workID != original.workID || current.runID != original.runID || current.attempt != original.attempt || current.generation != original.generation || current.agentID != original.agentID || current.crewID != original.crewID || current.credentialBinding != original.credentialBinding || current.authorVector != original.authorVector || current.memberID != original.memberID || current.memberRevision != original.memberRevision {
			return errCollaborationSubmission
		}
		return a.check(ctx, tx, write)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// The enclosing trusted operation owns SAME-store SQL writer/COMMIT and the
// complete original runtime/ingress lease. This author-only check cannot prove
// a caller-owned transaction's DB provenance; future inventory integration must
// qualify that separately with WithSessionOrigins/Visit, never an ACL callback.
// No failure licenses durable permission_revoked, output or origin allocation.
func (a *collaborationOriginAuthor) check(ctx context.Context, tx *sql.Tx, write bool) error {
	if ctx == nil || ctx.Err() != nil || tx == nil || a == nil || a.self != a || a.registry == nil || a.registry.humans == nil || a.registry.humans.owners == nil || write && !a.write || (a.human == nil) == (a.agent == nil) {
		return errCollaborationSubmission
	}
	s := a.registry
	operation, err := s.active(ctx)
	if err != nil || operation.binding.runtime != a.binding.runtime || operation.binding.id != a.binding.id || a.owner.Key != s.humans.scope {
		return errCollaborationSubmission
	}
	s.humans.mu.Lock()
	generation, cell := s.humans.ownerGeneration, s.humans.sessionStore
	s.humans.mu.Unlock()
	if generation != a.owner.Generation || cell != a.cell {
		return errCollaborationSubmission
	}
	if err = s.humans.owners.Check(ctx, tx, a.owner); err != nil {
		return err
	}
	if cell != nil {
		if err = cell.check(ctx, tx); err != nil {
			return err
		}
	}
	if a.human != nil {
		binding, ok := ctx.Value(collaborationHumanBindingKey{}).(collaborationHumanBinding)
		if !ok || binding.registry != s.humans || binding.operation == nil || !binding.operation.active.Load() || binding.participant != a.human.participant {
			return errCollaborationSubmission
		}
		s.humans.mu.Lock()
		original, ok := s.humans.admissions[binding.participant]
		s.humans.mu.Unlock()
		if !ok || original != *a.human {
			return errCollaborationSubmission
		}
		if _, err = s.humans.checkCaptured(ctx, tx, *a.human, write, 0); err != nil {
			return err
		}
	} else {
		if a.agent.store == nil || a.agent.store.pages != s.humans.pages || a.agent.store.pages.db != s.humans.pages.db || a.agent.store.fence != s.humans.fence {
			return errCollaborationSubmission
		}
		intent := "read"
		if write {
			intent = "task"
		}
		decision, err := a.agent.store.DecideCaptured(ctx, tx, *a.agent, a.owner.Key, intent)
		if err != nil {
			return err
		}
		if decision.kind != collaborationAgentAllowed {
			return errCollaborationSubmission
		}
	}
	if _, err = s.active(ctx); err != nil {
		return err
	}
	if cell != nil {
		if err = cell.check(ctx, tx); err != nil {
			return err
		}
	}
	return s.humans.owners.Check(ctx, tx, a.owner)
}
