package mooring

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func editLeaseFixture(t *testing.T) (*EditLeaseStore, *sql.DB, *ownerTestClock, EditLeaseScope, EditLeasePrincipal) {
	t.Helper()
	_, db, clock, key := ownerFixture(t)
	authority := func(ctx context.Context, tx *sql.Tx, a EditLeaseAccess) error {
		var suspended sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT suspended_at FROM users WHERE id=?`, a.Principal.ActorID).Scan(&suspended); err != nil {
			return err
		}
		if suspended.Valid {
			return ErrEditLeaseDenied
		}
		return nil
	}
	s := NewEditLeaseStore(db.DB, EditLeaseConfig{Enabled: true, Clock: clock, TTL: 5 * time.Second, Authority: authority})
	return s, db.DB, clock, EditLeaseScope{key.WorkspaceID, key.ArtifactID}, EditLeasePrincipal{"u", "tab-a"}
}
func editWrite(t *testing.T, s *EditLeaseStore, db *sql.DB, l EditLease) error {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = s.CheckWrite(t.Context(), tx, l); err != nil {
		return err
	}
	_, err = tx.ExecContext(t.Context(), `UPDATE pages SET name='saved' WHERE id=?`, l.Scope.ArtifactID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func TestEditLeaseDefaultOff(t *testing.T) {
	s := NewEditLeaseStore(nil, EditLeaseConfig{})
	if _, err := s.Acquire(t.Context(), EditLeaseScope{}, EditLeasePrincipal{}); !errors.Is(err, ErrEditLeaseDisabled) {
		t.Fatal(err)
	}
	if err := s.CheckWrite(t.Context(), nil, EditLease{}); !errors.Is(err, ErrEditLeaseDisabled) {
		t.Fatal(err)
	}
}
func TestEditLeaseFencesTabsAndExpiredGeneration(t *testing.T) {
	s, db, clock, scope, p := editLeaseFixture(t)
	first, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = editWrite(t, s, db, first); err != nil {
		t.Fatal(err)
	}
	secondTab := EditLeasePrincipal{p.ActorID, "tab-b"}
	if _, err = s.Acquire(t.Context(), scope, secondTab); !errors.Is(err, ErrEditLeaseBusy) {
		t.Fatal(err)
	}
	// Reading or switching visibility never releases; only a live authorized renewal extends.
	clock.advance(4 * time.Second)
	renewed, err := s.Renew(t.Context(), first)
	if err != nil || renewed.Generation != first.Generation {
		t.Fatalf("renew: %+v %v", renewed, err)
	}
	clock.advance(4 * time.Second)
	if err = editWrite(t, s, db, first); err != nil {
		t.Fatal("client deadline must not override renewed server deadline", err)
	}
	clock.advance(time.Second)
	next, err := s.Acquire(t.Context(), scope, secondTab)
	if err != nil || next.Generation != first.Generation+1 {
		t.Fatalf("takeover: %+v %v", next, err)
	}
	if err = editWrite(t, s, db, first); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("late generation accepted", err)
	}
	if _, err = s.Renew(t.Context(), first); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal(err)
	}
}
func TestEditLeaseHandoffRequiresRequestAndFencesOldWrites(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	first, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	target := EditLeasePrincipal{"u", "tab-b"}
	if _, err = s.Handoff(t.Context(), first, target); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("unrequested handoff", err)
	}
	if err = s.RequestHandoff(t.Context(), scope, target); err != nil {
		t.Fatal(err)
	}
	next, err := s.Handoff(t.Context(), first, target)
	if err != nil {
		t.Fatal(err)
	}
	if next.Holder != target || next.Generation != first.Generation+1 {
		t.Fatal(next)
	}
	if err = editWrite(t, s, db, first); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal(err)
	}
	if err = editWrite(t, s, db, next); err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), first); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("old close released new holder", err)
	}
}
func TestEditLeaseReleaseAndRestartNeverReuseGeneration(t *testing.T) {
	s, db, clock, scope, p := editLeaseFixture(t)
	first, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Release(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	next, err := s.Acquire(t.Context(), scope, p)
	if err != nil || next.Generation != first.Generation+1 {
		t.Fatalf("release lost generation: %+v %v", next, err)
	}
	clock.boot = "boot-b"
	rebooted := NewEditLeaseStore(db, s.config)
	if err = editWrite(t, rebooted, db, next); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("reboot revived lease", err)
	}
	last, err := rebooted.Acquire(t.Context(), scope, p)
	if err != nil || last.Generation != next.Generation+1 {
		t.Fatalf("restart: %+v %v", last, err)
	}
}
func TestEditLeaseRevocationAndForgeryCannotBypassWrite(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	l, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*EditLease){
		func(l *EditLease) { l.Holder.SessionID = "tab-b" },
		func(l *EditLease) { l.Generation++ },
		func(l *EditLease) { l.Scope.WorkspaceID = "other" },
		func(l *EditLease) { l.Holder.ActorID = "unknown" },
	} {
		forged := l
		mutate(&forged)
		if err = editWrite(t, s, db, forged); err == nil {
			t.Fatal("forged lease accepted", forged)
		}
	}
	if _, err = db.Exec(`UPDATE users SET suspended_at='2026-10-27' WHERE id='u'`); err != nil {
		t.Fatal(err)
	}
	if err = editWrite(t, s, db, l); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("revoked write accepted", err)
	}
	if _, err = s.Renew(t.Context(), l); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("revoked renewal accepted", err)
	}
	if _, err = s.Read(t.Context(), scope, p); !errors.Is(err, ErrEditLeaseDenied) {
		t.Fatal("revoked read delivered", err)
	}
}
func TestEditLeaseGuardAndPublicationRollbackTogether(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	l, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckWrite(t.Context(), tx, l); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE pages SET name='uncommitted' WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = db.QueryRow(`SELECT name FROM pages WHERE id='p'`).Scan(&name); err != nil || name != "P" {
		t.Fatalf("publication escaped rollback: %s %v", name, err)
	}
}

func TestEditLeaseRevocationFencesRestoredRightsAndRollsBackWithAuthority(t *testing.T) {
	s, db, _, scope, p := editLeaseFixture(t)
	l, err := s.Acquire(t.Context(), scope, p)
	if err != nil {
		t.Fatal(err)
	}
	admin := EditLeasePrincipal{"admin", "admin-session"}
	if _, err = db.Exec(`INSERT INTO users(id,email) VALUES('admin','admin@example.test')`); err != nil {
		t.Fatal(err)
	}
	revoke := func(commit bool) {
		t.Helper()
		tx, e := db.BeginTx(t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.Exec(`UPDATE users SET suspended_at='revoked' WHERE id='u'`); e != nil {
			t.Fatal(e)
		}
		if e = s.Revoke(t.Context(), tx, scope, admin, l.Generation); e != nil {
			t.Fatal(e)
		}
		if commit {
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
		}
	}
	revoke(false)
	if err = editWrite(t, s, db, l); err != nil {
		t.Fatal("rolled back revocation killed lease", err)
	}
	revoke(true)
	if _, err = db.Exec(`UPDATE users SET suspended_at=NULL WHERE id='u'`); err != nil {
		t.Fatal(err)
	}
	if err = editWrite(t, s, db, l); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("restored rights revived original generation", err)
	}
	next, err := s.Acquire(t.Context(), scope, p)
	if err != nil || next.Generation != l.Generation+1 {
		t.Fatalf("new authority must acquire new generation: %+v %v", next, err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = s.Revoke(t.Context(), tx, scope, admin, l.Generation); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal("late revocation killed new holder", err)
	}
}
