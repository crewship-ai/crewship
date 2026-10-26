package work

import (
	"context"
	"errors"
	"fmt"
)

// referenceClaimBeforeIdlePrecheck freezes Claim at baseline9cf047d. Its
// transactional helpers are unchanged by the idle-poll optimization.
func (s *Store) referenceClaimBeforeIdlePrecheck(ctx context.Context, opts ClaimOptions) (*Claimed, error) {
	if opts.LeaseOwner == "" {
		return nil, errors.New("work: claim requires a lease owner")
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("work: begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()

	// A deadline that passed while the work waited expires it here, before it
	// can consume a slot. Work without a deadline never lands in this branch.
	if err := s.expireOverdueTx(ctx, tx, now); err != nil {
		return nil, err
	}

	live, err := s.countLiveTx(ctx, tx)
	if err != nil {
		return nil, err
	}

	// Server-level gates do not vary per row, so they are settled before the
	// scan rather than rejecting candidates one at a time inside it.
	classes, ok := live.admissibleClasses(opts.Limits, opts.Class)
	if !ok {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("work: commit claim scan: %w", err)
		}
		return nil, ErrNoWork
	}

	candidates, err := s.scanCandidatesTx(ctx, tx, opts, classes, now)
	if err != nil {
		return nil, err
	}

	for _, it := range candidates {
		ok, err := s.admitsTx(ctx, tx, it, opts.Limits, live)
		if err != nil {
			return nil, err
		}
		if !ok {
			// The scan already filtered on every per-row rule, so this branch
			// means the SQL and the Go rules disagree. Keep going — but see the
			// divergence check after the loop, which refuses to report it as
			// "nothing to do".
			continue
		}
		claimed, err := s.startAttemptTx(ctx, tx, it, opts.LeaseOwner, now)
		if errors.Is(err, errAttemptsExhausted) {
			// startAttemptTx already wrote the failure into this transaction.
			// Keep scanning rather than returning: this candidate is dead, but
			// the work behind it is not, and abandoning the scan here would let
			// one exhausted item stall the whole queue.
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("work: commit claim: %w", err)
		}
		return claimed, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("work: commit claim scan: %w", err)
	}
	return nil, ErrNoWork
}
