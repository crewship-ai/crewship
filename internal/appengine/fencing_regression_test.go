package appengine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestExpiredReservationWithoutTakeoverCannotCommit(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	e.prepare = func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
		if _, err := db.Exec(`UPDATE gridcraft_operations SET lease_until=0 WHERE mutation_id='edit-1'`); err != nil {
			return Candidate{}, err
		}
		return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"}), nil
	}
	if _, err := s.Execute(t.Context(), k, edit(), a, e); !errors.Is(err, ErrFence) {
		t.Fatalf("expired worker: %v", err)
	}
	state, err := s.Read(t.Context(), k, a)
	if err != nil || state.Rev != 1 {
		t.Fatalf("expired worker changed head: %+v %v", state, err)
	}
	var audits, outbox int
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_audit`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit: %d %v", audits, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_outbox`).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("outbox: %d %v", outbox, err)
	}
}

// Old cleanup must not release the reservation of a newer, still-running
// attempt at the same mutation. Only the new token may confirm it.
func TestStaleWorkerCleanupCannotReleaseLiveTakeover(t *testing.T) {
	s, db, a, k := fixture(t)
	imported(t, s, a, k, &fakeEngine{})
	oldEntered, oldRelease := make(chan struct{}), make(chan struct{})
	newEntered, newRelease := make(chan struct{}), make(chan struct{})
	var oldOnce, newOnce sync.Once
	releaseOld := func() { oldOnce.Do(func() { close(oldRelease) }) }
	releaseNew := func() { newOnce.Do(func() { close(newRelease) }) }
	t.Cleanup(releaseOld)
	t.Cleanup(releaseNew)
	engine := func(entered, release chan struct{}) *fakeEngine {
		return &fakeEngine{prepare: func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
			close(entered)
			<-release
			return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"}), nil
		}}
	}
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), k, edit(), a, engine(oldEntered, oldRelease)); oldDone <- err }()
	select {
	case <-oldEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("old worker did not enter")
	}
	if _, err := db.Exec(`UPDATE gridcraft_operations SET lease_until=0 WHERE mutation_id='edit-1'`); err != nil {
		t.Fatal(err)
	}
	replacement := New(db.DB, Options{Enabled: true})
	go func() {
		_, err := replacement.Execute(t.Context(), k, edit(), a, engine(newEntered, newRelease))
		newDone <- err
	}()
	select {
	case <-newEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not enter")
	}
	releaseOld()
	if err := <-oldDone; !errors.Is(err, ErrFence) {
		t.Fatalf("stale candidate: %v", err)
	}
	var status string
	var token int64
	if err := db.QueryRow(`SELECT state,token FROM gridcraft_operations WHERE mutation_id='edit-1'`).Scan(&status, &token); err != nil || status != "in_progress" || token < 2 {
		t.Fatalf("replacement reservation lost: %s %d %v", status, token, err)
	}
	releaseNew()
	if err := <-newDone; err != nil {
		t.Fatalf("replacement: %v", err)
	}
	state, err := s.Read(t.Context(), k, a)
	if err != nil || state.Rev != 2 {
		t.Fatalf("head: %+v %v", state, err)
	}
	var audits int
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_audit`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("confirmed twice: %d %v", audits, err)
	}
}
