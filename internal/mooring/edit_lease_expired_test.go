package mooring

import (
	"errors"
	"testing"
	"time"
)

// A lease that only expired is renewed when nobody took it since: an agent whose model took longer
// than the TTL between its acquire and its apply keeps its generation. Taken by another editor, or
// asked for by a person, it answers ErrEditLeaseExpired, which is still a stale lease to every
// caller that only knows that.
func TestEditLeaseRenewTakesBackAnExpiredLeaseNobodyTook(t *testing.T) {
	s, db, clock, scope, p := editLeaseFixture(t)
	first, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if err = editWrite(t, s, db, first); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("a write over an expired lease: %v", err)
	}
	renewed, err := s.Renew(t.Context(), first)
	if err != nil || renewed.Generation != first.Generation || renewed.Holder != p {
		t.Fatalf("renew of an expired lease nobody took: %+v %v", renewed, err)
	}
	if err = editWrite(t, s, db, renewed); err != nil {
		t.Fatalf("a write after the renew: %v", err)
	}

	// Another tab takes it after expiry: the old holder is told it expired and was taken.
	clock.advance(time.Minute)
	other := EditLeasePrincipal{p.ActorID, "tab-b"}
	next, err := s.Acquire(t.Context(), scope, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Renew(t.Context(), renewed); !errors.Is(err, ErrEditLeaseExpired) || !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("renew of a lease taken since: %v", err)
	}
	if err = editWrite(t, s, db, next); err != nil {
		t.Fatalf("the new holder's write: %v", err)
	}

	// A person asked for the Sheet before the lease expired: it is not taken back.
	if err = s.RequestHandoff(t.Context(), scope, p); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute)
	if _, err = s.Renew(t.Context(), next); !errors.Is(err, ErrEditLeaseExpired) {
		t.Fatalf("renew of an expired lease a person asked for: %v", err)
	}

	// A released lease is never taken back.
	if _, err = db.Exec(`DELETE FROM mooring_edit_lease_requests`); err != nil {
		t.Fatal(err)
	}
	third, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), third); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Renew(t.Context(), third); !errors.Is(err, ErrEditLeaseStale) || errors.Is(err, ErrEditLeaseExpired) {
		t.Fatalf("renew of a released lease: %v", err)
	}
}
