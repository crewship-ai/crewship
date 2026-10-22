package appengine

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
)

// A request cancelled mid-candidate is reported by its cause: lost authority
// as an authorization error, a vanished caller as cancellation, and in
// neither case as an unavailable engine. Nothing is written either way.
func TestCancelledOperationReportsAuthorityLossNotEngine(t *testing.T) {
	for _, tc := range []struct {
		name string
		lost error
		want error
	}{
		{"credential revoked", ErrUnauthenticated, ErrUnauthenticated},
		{"access revoked", ErrForbidden, ErrForbidden},
		{"caller gone", nil, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, a, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, a, k, e)
			var lost atomic.Bool
			auth := func(ctx context.Context, tx *sql.Tx) (Principal, error) {
				if lost.Load() && tc.lost != nil {
					return Principal{}, tc.lost
				}
				return a(ctx, tx)
			}
			ctx, cancel := context.WithCancel(t.Context())
			e.prepare = func(ctx context.Context, _ Binding, _ State, _ Command) (Candidate, error) {
				lost.Store(true)
				cancel() // revocation cancels the in-flight request
				<-ctx.Done()
				return Candidate{}, ErrEngineUnavailable // as WorkerClient reports it
			}
			_, err := s.Execute(ctx, k, edit(), auth, e)
			if !errors.Is(err, tc.want) || errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM gridcraft_revisions`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("write after interruption: %d %v", n, err)
			}
		})
	}
}
