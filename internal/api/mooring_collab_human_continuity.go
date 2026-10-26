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
	deadline      atomic.Int64
	credentialCap time.Duration
	pins          atomic.Int32
}

func (a collaborationHumanAdmission) currentDeadline() time.Duration {
	if a.freshness == nil {
		return a.deadline
	}
	return time.Duration(a.freshness.deadline.Load())
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
	if !ok || admission.freshness == nil || source == nil || *source != admission.source {
		return nil, errCollaborationAdmission
	}
	boot, start, err := s.clock.Now()
	old := admission.currentDeadline()
	if err != nil || boot != admission.boot || start < 0 || start >= old || start > time.Duration(math.MaxInt64)-5*time.Minute {
		return nil, errCollaborationAdmission
	}
	next := start + 5*time.Minute
	if cap := admission.freshness.credentialCap; cap != 0 && next > cap {
		next = cap
	}
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
			if next <= old {
				return nil
			}
			if !admission.freshness.deadline.CompareAndSwap(int64(old), int64(next)) {
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
