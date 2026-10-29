//go:build crewship_testbarrier && unix

package appengine

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// These tests park a real Execute between candidate and commit (or after
// commit) on FULL-synchronous SQLite and show that every commit-time check
// still runs once the barrier releases.

func revisions(t *testing.T, db interface {
	QueryRow(string, ...any) *sql.Row
}) int {
	t.Helper()
	var n int
	// Every committed revision has one audit row; snapshots are kept only at
	// checkpoints, so gridcraft_revisions does not count revisions.
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_audit`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func parkedEdit(mutation string) Request {
	r := edit()
	r.MutationID = mutation
	return r
}

// Proof 6: the holder outlives its lease while parked. A competitor is still
// refused by the physical slot, the holder is fenced at commit with no write,
// and the competitor's retry then commits.
func TestBarrierLeaseExpiryWhileParkedFencesHolder(t *testing.T) {
	dir := activateBarrier(t)
	_, db, a, k := fixture(t)
	s := New(db.DB, Options{Enabled: true, LeaseDuration: 300 * time.Millisecond})
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	holder := make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), k, parkedEdit("barrier-6"), a, e); holder <- err }()
	release := waitParked(t, dir, k, "user:owner", "barrier-6", barrierPreCommit)
	time.Sleep(400 * time.Millisecond)
	var state string
	var lease int64
	if err := db.QueryRow(`SELECT state,lease_until FROM gridcraft_operations WHERE mutation_id='barrier-6'`).Scan(&state, &lease); err != nil || state != "in_progress" || lease > time.Now().UnixMilli() {
		t.Fatalf("parked operation state=%s lease=%d err=%v", state, lease, err)
	}
	competitor := parkedEdit("competitor")
	if _, err := s.Execute(t.Context(), k, competitor, a, e); !errors.Is(err, ErrCapacity) {
		t.Fatalf("competitor during park: %v", err)
	}
	touch(t, release)
	if err := <-holder; !errors.Is(err, ErrFence) {
		t.Fatalf("holder after lease expiry: %v", err)
	}
	if n := revisions(t, db); n != 1 {
		t.Fatalf("fenced holder wrote: %d", n)
	}
	r, err := s.Execute(t.Context(), k, competitor, a, e)
	if err != nil || r.Rev != 2 {
		t.Fatalf("competitor retry: %+v %v", r, err)
	}
}

// The commit-time base check. Through the API the base cannot move while the
// physical slot is held (see the lease test), so an out-of-band writer stands
// in for it; this pins that the check runs after release.
func TestBarrierBaseAdvancedWhileParkedFailsPrecondition(t *testing.T) {
	dir := activateBarrier(t)
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	done := make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), k, parkedEdit("barrier-base"), a, e); done <- err }()
	release := waitParked(t, dir, k, "user:owner", "barrier-base", barrierPreCommit)
	if _, err := db.Exec(`UPDATE gridcraft_heads SET head_rev=head_rev+1 WHERE page_id=?`, k.PageID); err != nil {
		t.Fatal(err)
	}
	touch(t, release)
	if err := <-done; !errors.Is(err, ErrPrecondition) {
		t.Fatalf("got %v", err)
	}
	if n := revisions(t, db); n != 1 {
		t.Fatalf("stale base wrote: %d", n)
	}
}

// Proof 7 shape: the mandate is revoked while the candidate waits to commit.
// Release must not write. A credential revoked the same way (which cancels
// the request) reports the credential loss.
func TestBarrierAuthorityRevokedWhileParkedPreventsWrite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lost   error
		cancel bool
	}{
		{"mandate revoked", ErrForbidden, false},
		{"credential revoked cancels request", ErrUnauthenticated, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := activateBarrier(t)
			s, db, a, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, a, k, e)
			var revoked atomic.Bool
			auth := func(ctx context.Context, tx *sql.Tx) (Principal, error) {
				if revoked.Load() {
					return Principal{}, tc.lost
				}
				return a(ctx, tx)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := s.Execute(ctx, k, parkedEdit("barrier-7"), auth, e); done <- err }()
			release := waitParked(t, dir, k, "user:owner", "barrier-7", barrierPreCommit)
			revoked.Store(true)
			if tc.cancel {
				cancel()
			} else {
				touch(t, release)
			}
			if err := <-done; !errors.Is(err, tc.lost) {
				t.Fatalf("got %v want %v", err, tc.lost)
			}
			if n := revisions(t, db); n != 1 {
				t.Fatalf("revoked authority wrote: %d", n)
			}
		})
	}
}

// Lost acknowledgement: the commit is durable, the caller disappears before
// the receipt reaches it, and an identical retry returns the original receipt
// without running the engine again.
func TestBarrierLostAckAfterCommitRetryReturnsOriginalReceipt(t *testing.T) {
	dir := activateBarrier(t)
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	req := parkedEdit("barrier-ack")
	go func() { _, err := s.Execute(ctx, k, req, a, e); done <- err }()
	// Every barrier- operation parks at both points, in order.
	touch(t, waitParked(t, dir, k, "user:owner", "barrier-ack", barrierPreCommit))
	waitParked(t, dir, k, "user:owner", "barrier-ack", barrierPostCommit)
	status, err := s.Status(t.Context(), k, "barrier-ack", a)
	if err != nil || status.State != "applied" || status.Receipt == nil {
		t.Fatalf("not durable while parked: %+v %v", status, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller should see no receipt: %v", err)
	}
	calls := e.calls.Load()
	retry, err := s.Execute(t.Context(), k, req, a, e)
	if err != nil || !reflect.DeepEqual(retry, *status.Receipt) || e.calls.Load() != calls {
		t.Fatalf("retry=%+v err=%v original=%+v calls=%d->%d", retry, err, *status.Receipt, calls, e.calls.Load())
	}
	if n := revisions(t, db); n != 2 {
		t.Fatalf("revisions=%d", n)
	}
}
