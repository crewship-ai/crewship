package mooring

import (
	"errors"
	"testing"
	"time"
)

// A co-write grant lets an agent write beside the live lease of the person who consented,
// never changing that lease. Each check requires that lease held still by that person at the same
// generation, or no live lease at all; another holder or a takeover is busy and nothing is written.
func TestEditLeaseCoWriteBesideThePersonsLease(t *testing.T) {
	s, db, clock, scope, person := editLeaseFixture(t)
	if _, err := db.Exec(`INSERT INTO users(id,email) VALUES('agent-x','agent-x@example.invalid'),('u-other','u-other@example.invalid')`); err != nil {
		t.Fatal(err)
	}
	agent := EditLeasePrincipal{"agent-x", "run-1"}
	row := func() (actor, session string, generation, expires int64) {
		if err := db.QueryRow(`SELECT actor_id,session_id,generation,expires_ns FROM mooring_edit_leases WHERE page_id=?`, scope.ArtifactID).Scan(&actor, &session, &generation, &expires); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := s.CoWrite(t.Context(), scope, agent, person.ActorID); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("co-write without the person's lease: %v", err)
	}
	held, err := s.Acquire(t.Context(), scope, person)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CoWrite(t.Context(), scope, agent, "u-other"); !errors.Is(err, ErrEditLeaseBusy) {
		t.Fatalf("co-write beside a lease of another person: %v", err)
	}
	grant, err := s.CoWrite(t.Context(), scope, agent, person.ActorID)
	if err != nil || grant.Holder != agent || grant.CoWrite != person || grant.Generation != held.Generation {
		t.Fatalf("grant %+v %v", grant, err)
	}
	a1, s1, g1, e1 := row()
	if err = editWrite(t, s, db, grant); err != nil {
		t.Fatalf("co-write beside the person's lease: %v", err)
	}
	if err = editWrite(t, s, db, held); err != nil {
		t.Fatalf("the person's own write: %v", err)
	}
	clock.advance(time.Second)
	if _, err = s.Renew(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	if a2, s2, g2, e2 := row(); a2 != a1 || s2 != s1 || g2 != g1 || e2 != e1 {
		t.Fatalf("the person's lease changed: %s/%s/%d/%d → %s/%s/%d/%d", a1, s1, g1, e1, a2, s2, g2, e2)
	}
	// Another tab of the person takes the Sheet over after expiry: a new generation, the grant is busy.
	clock.advance(5 * time.Second)
	if _, err = s.Acquire(t.Context(), scope, EditLeasePrincipal{person.ActorID, "tab-b"}); err != nil {
		t.Fatal(err)
	}
	if err = editWrite(t, s, db, grant); !errors.Is(err, ErrEditLeaseBusy) {
		t.Fatalf("co-write after a takeover: %v", err)
	}
	// Nobody holds a live lease: the grant writes (the version CAS still orders it).
	clock.advance(6 * time.Second)
	if err = editWrite(t, s, db, grant); err != nil {
		t.Fatalf("co-write with no live lease: %v", err)
	}
}
