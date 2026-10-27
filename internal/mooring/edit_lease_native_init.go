package mooring

import (
	"context"
	"database/sql"
	"errors"
)

// AcquireInTransaction joins explicit native initialization to its first
// manifest. The caller owns commit/rollback, and must roll back on any error.
// It grants no authority beyond the same checks as Acquire.
func (s *EditLeaseStore) AcquireInTransaction(ctx context.Context, tx *sql.Tx, scope EditLeaseScope, p EditLeasePrincipal) (EditLease, error) {
	if err := s.ready(); err != nil {
		return EditLease{}, err
	}
	if tx == nil {
		return EditLease{}, ErrInvalid
	}
	if err := ownerFence(ctx, tx); err != nil {
		return EditLease{}, err
	}
	if err := editLeaseDurable(ctx, tx); err != nil {
		return EditLease{}, err
	}
	if err := s.authorize(ctx, tx, scope, p, "acquire"); err != nil {
		return EditLease{}, err
	}
	old, oldBoot, expires, err := editLeaseRow(ctx, tx, scope)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return EditLease{}, err
	}
	boot, now, clockErr := s.clock()
	if clockErr != nil {
		return EditLease{}, clockErr
	}
	if err == nil && editLeaseLive(old, oldBoot, expires, boot, now) {
		if old.Holder != p {
			return EditLease{}, ErrEditLeaseBusy
		}
		if _, _, err = s.check(ctx, tx, old, "acquire"); err != nil {
			return EditLease{}, err
		}
		return presentEditLease(old, expires, now), nil
	}
	l, err := s.install(ctx, tx, scope, p, old.Generation, boot, now)
	if err != nil {
		return EditLease{}, err
	}
	_, _, err = s.check(ctx, tx, l, "acquire")
	return l, err
}
