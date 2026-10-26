package api

import (
	"context"
	"database/sql"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Optional host transport fences. SQL validation runs without connection locks;
// publication holds only short local locks and performs no SQL or network I/O.
type collaborationHumanContinuityTransportKey struct{}
type collaborationHumanContinuityTransport struct {
	checkSQL func(context.Context, *sql.Tx) error
	publish  func(func() error) error
}

// Shared only by copies of one original host-issued admission. This is a
// freshness horizon, never an authorization result. Every send and commit still
// checks current SQL rights and the immutable original authority epoch.
type collaborationHumanFreshness struct {
	authorization atomic.Pointer[collaborationHumanAuthorization]
	pins          atomic.Int32
}

// Published atomically so queued original operations observe a coherent expiry
// and horizon. Immutable attribution and revocation epochs stay on admission.
type collaborationHumanAuthorization struct {
	source                  auth.OriginalCredential
	deadline, credentialCap time.Duration
}

func (a collaborationHumanAdmission) currentCredential() auth.OriginalCredential {
	if a.freshness != nil {
		return a.freshness.authorization.Load().source
	}
	return a.source
}

func (a collaborationHumanAdmission) currentDeadline() time.Duration {
	if a.freshness == nil {
		return a.deadline
	}
	return a.freshness.authorization.Load().deadline
}

// Prepare and publication stay inside the SAME authenticated ingress/runtime
// operation. The private closure is one-shot, cannot revive expired admissions,
// and rechecks canonical SQL after preparation to detect revoke/restore ABA.
func (s *collaborationHumanRegistry) prepareHumanContinuity(ctx context.Context, owner mooring.OwnerLease) (func() error, error) {
	binding, ok := ctx.Value(collaborationHumanBindingKey{}).(collaborationHumanBinding)
	if !ok || binding.registry != s || binding.operation == nil || !binding.operation.active.Load() || ctx.Err() != nil || owner.Key != s.scope {
		return nil, errCollaborationAdmission
	}
	s.mu.Lock()
	admission, ok := s.admissions[binding.participant]
	s.mu.Unlock()
	source := auth.OriginalCredentialFromContext(ctx)
	if !ok || admission.freshness == nil || source == nil {
		return nil, errCollaborationAdmission
	}
	// Only an authenticated refresh of the SAME credential and revocation epoch
	// may advance expiry. CLI credentials and original agent mandates stay fixed.
	originalIdentity, freshIdentity := admission.source, *source
	originalIdentity.ExpiresAt, freshIdentity.ExpiresAt = "", ""
	if originalIdentity != freshIdentity || (source.Kind != AuthKindSession && *source != admission.source) {
		return nil, errCollaborationAdmission
	}
	boot, start, err := s.clock.Now()
	previous := admission.freshness.authorization.Load()
	old := previous.deadline
	if err != nil || boot != admission.boot || start < 0 || start >= old || start > time.Duration(math.MaxInt64)-5*time.Minute {
		return nil, errCollaborationAdmission
	}
	capDeadline := previous.credentialCap
	accepted := previous.source
	if *source != previous.source {
		incomingExpiry, e := source.Expiry()
		acceptedExpiry, _ := accepted.Expiry()
		if e != nil || incomingExpiry.IsZero() || !incomingExpiry.After(acceptedExpiry) {
			return nil, errCollaborationAdmission
		}
		remaining := time.Until(incomingExpiry)
		if remaining < time.Millisecond || remaining > time.Duration(math.MaxInt64)-start {
			return nil, errCollaborationAdmission
		}
		capDeadline = start + remaining
		accepted = *source
	}
	next := min(start+5*time.Minute, capDeadline)
	if capDeadline == 0 {
		next = start + 5*time.Minute
	}
	renewed := &collaborationHumanAuthorization{source: accepted, deadline: next, credentialCap: capDeadline}
	// This helper performs a complete SQL transaction on both sides of the
	// preparation gap. No mutex is held while waiting for the SQL writer.
	check := func() error {
		if ctx.Err() != nil || !binding.operation.active.Load() {
			return errCollaborationAdmission
		}
		s.mu.Lock()
		current, exists := s.admissions[binding.participant]
		s.mu.Unlock()
		if !exists || current.freshness != admission.freshness {
			return errCollaborationAdmission
		}
		tx, e := s.pages.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = s.owners.Check(ctx, tx, owner); e != nil {
			return e
		}
		if _, e = s.checkCaptured(ctx, tx, admission, admission.write, 0); e != nil {
			return e
		}
		// Fresh middleware provenance is validated against current canonical SQL
		// independently of the still-current original admission on both passes.
		intent := "read"
		if admission.write {
			intent = "write"
		}
		proof, e := s.pages.authorizeCollaborationHumanPage(ctx, tx, s.scope, intent)
		if e != nil || proof.memberID != admission.member || proof.memberRevision != admission.memberRevision {
			return errCollaborationAdmission
		}
		if transport, ok := ctx.Value(collaborationHumanContinuityTransportKey{}).(collaborationHumanContinuityTransport); ok {
			if transport.checkSQL == nil || transport.publish == nil {
				return errCollaborationAdmission
			}
			if e = transport.checkSQL(ctx, tx); e != nil {
				return e
			}
		}
		if e = s.owners.Check(ctx, tx, owner); e != nil {
			return e
		}
		return tx.Commit()
	}
	if err = check(); err != nil {
		return nil, err
	}
	var used atomic.Bool
	return func() error {
		if !used.CompareAndSwap(false, true) {
			return errCollaborationAdmission
		}
		if e := check(); e != nil {
			return e
		}
		publish := func() error {
			currentBoot, now, e := s.clock.Now()
			if e != nil || currentBoot != boot || now < start || now >= old || now >= next || ctx.Err() != nil || !binding.operation.active.Load() {
				return errCollaborationAdmission
			}
			// Publication grants no gate or write; all later protected operations
			// still validate original epochs and current SQL policy.
			if next <= old && accepted == previous.source {
				return nil
			}
			if !admission.freshness.authorization.CompareAndSwap(previous, renewed) {
				return errCollaborationAdmission
			}
			return nil
		}
		if transport, ok := ctx.Value(collaborationHumanContinuityTransportKey{}).(collaborationHumanContinuityTransport); ok {
			return transport.publish(publish)
		}
		return publish()
	}, nil
}

// Keep the original denial metadata while an operation/connection still needs
// exact retirement. A pin is NOT authority: checkCaptured still refuses the
// exhausted horizon, and an unknown participant never acquires this proof.
func (s *collaborationHumanRegistry) pinHumanAdmission(participant string) (collaborationHumanAdmission, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.admissions[participant]
	if !ok {
		return collaborationHumanAdmission{}, nil, errCollaborationAdmission
	}
	if a.freshness == nil {
		return a, func() {}, nil
	} // legacy unit fixtures only
	a.freshness.pins.Add(1)
	return a, sync.OnceFunc(func() { a.freshness.pins.Add(-1) }), nil
}
