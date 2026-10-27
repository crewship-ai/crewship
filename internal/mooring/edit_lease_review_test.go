package mooring

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestReviewEditLeaseExpiryDuringAuthorityRejectsWriteAndRenew(t *testing.T) {
	for _, intent := range []string{"write", "renew"} {
		t.Run(intent, func(t *testing.T) {
			s, db, clock, scope, principal := editLeaseFixture(t)
			lease, err := s.Acquire(t.Context(), scope, principal)
			if err != nil {
				t.Fatal(err)
			}
			original := s.config.Authority
			s.config.Authority = func(ctx context.Context, tx *sql.Tx, access EditLeaseAccess) error {
				if err := original(ctx, tx, access); err != nil {
					return err
				}
				if access.Intent == intent {
					clock.advance(s.config.TTL)
				}
				return nil
			}
			if intent == "write" {
				err = editWrite(t, s, db, lease)
			} else {
				_, err = s.Renew(t.Context(), lease)
			}
			if !errors.Is(err, ErrEditLeaseStale) {
				t.Fatalf("authority finished after expiry but %s succeeded: %v", intent, err)
			}
			var name string
			if err := db.QueryRow(`SELECT name FROM pages WHERE id='p'`).Scan(&name); err != nil || name != "P" {
				t.Fatalf("expired publication persisted: %q %v", name, err)
			}
		})
	}
}

func TestReviewEditLeaseExpiryDuringTargetAuthorizationRejectsHandoff(t *testing.T) {
	s, db, clock, scope, principal := editLeaseFixture(t)
	lease, err := s.Acquire(t.Context(), scope, principal)
	if err != nil {
		t.Fatal(err)
	}
	target := EditLeasePrincipal{ActorID: principal.ActorID, SessionID: "requested-target"}
	if err := s.RequestHandoff(t.Context(), scope, target); err != nil {
		t.Fatal(err)
	}
	original := s.config.Authority
	s.config.Authority = func(ctx context.Context, tx *sql.Tx, access EditLeaseAccess) error {
		if err := original(ctx, tx, access); err != nil {
			return err
		}
		if access.Principal == target {
			clock.advance(s.config.TTL)
		}
		return nil
	}
	if _, err := s.Handoff(t.Context(), lease, target); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("expired holder handed off: %v", err)
	}
	var generation int64
	if err := db.QueryRow(`SELECT generation FROM mooring_edit_leases WHERE page_id='p'`).Scan(&generation); err != nil || generation != lease.Generation {
		t.Fatalf("failed handoff modified generation: %d %v", generation, err)
	}
}

func TestReviewEditLeaseConcurrentAcquireHasExactlyOneHolder(t *testing.T) {
	s, _, _, scope, principal := editLeaseFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, session := range []string{"tab-a", "tab-b"} {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			<-start
			_, err := s.Acquire(t.Context(), scope, EditLeasePrincipal{ActorID: principal.ActorID, SessionID: session})
			results <- err
		}(session)
	}
	close(start)
	wg.Wait()
	close(results)
	var winners, busy int
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrEditLeaseBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || busy != 1 {
		t.Fatalf("winners=%d busy=%d", winners, busy)
	}
}

func TestReviewEditLeaseExpiredAtBoundaryCannotRenew(t *testing.T) {
	s, _, clock, scope, principal := editLeaseFixture(t)
	lease, err := s.Acquire(t.Context(), scope, principal)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(5 * time.Second)
	if _, err := s.Renew(t.Context(), lease); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("expiry boundary renewed: %v", err)
	}
}

func TestReviewEditLeaseBootChangesDuringAuthorityRejectsWriteAndRenew(t *testing.T) {
	for _, intent := range []string{"write", "renew"} {
		t.Run(intent, func(t *testing.T) {
			s, db, clock, scope, principal := editLeaseFixture(t)
			lease, err := s.Acquire(t.Context(), scope, principal)
			if err != nil {
				t.Fatal(err)
			}
			original := s.config.Authority
			s.config.Authority = func(ctx context.Context, tx *sql.Tx, access EditLeaseAccess) error {
				if err := original(ctx, tx, access); err != nil {
					return err
				}
				if access.Intent == intent {
					clock.mu.Lock()
					clock.boot = "boot-after-authorization"
					clock.mu.Unlock()
				}
				return nil
			}
			if intent == "write" {
				err = editWrite(t, s, db, lease)
			} else {
				_, err = s.Renew(t.Context(), lease)
			}
			if !errors.Is(err, ErrEditLeaseStale) {
				t.Fatalf("boot changed during authority but %s succeeded: %v", intent, err)
			}
		})
	}
}

func TestReviewEditLeaseAcquireUsesPostAuthorityFreshness(t *testing.T) {
	for _, reboot := range []bool{false, true} {
		t.Run(map[bool]string{false: "elapsed", true: "reboot"}[reboot], func(t *testing.T) {
			s, db, clock, scope, principal := editLeaseFixture(t)
			original := s.config.Authority
			slowFirstAuthorization := true
			s.config.Authority = func(ctx context.Context, tx *sql.Tx, access EditLeaseAccess) error {
				if err := original(ctx, tx, access); err != nil {
					return err
				}
				if access.Intent == "acquire" && slowFirstAuthorization {
					slowFirstAuthorization = false
					clock.advance(s.config.TTL + time.Second)
					if reboot {
						clock.mu.Lock()
						clock.boot = "boot-after-authorization"
						clock.mu.Unlock()
					}
				}
				return nil
			}
			lease, err := s.Acquire(t.Context(), scope, principal)
			if err != nil {
				t.Fatal(err)
			}
			if err := editWrite(t, s, db, lease); err != nil {
				t.Fatalf("newly acquired lease was already unusable after authority: %v", err)
			}
		})
	}
}

type editLeaseReviewClockFunc func() (string, time.Duration, error)

func (f editLeaseReviewClockFunc) Now() (string, time.Duration, error) { return f() }

func TestReviewEditLeaseRenewCannotSlideCapAfterValidatedSnapshot(t *testing.T) {
	s, db, clock, scope, principal := editLeaseFixture(t)
	lease, err := s.Acquire(t.Context(), scope, principal)
	if err != nil {
		t.Fatal(err)
	}
	authorized := false
	original := s.config.Authority
	s.config.Authority = func(ctx context.Context, tx *sql.Tx, access EditLeaseAccess) error {
		err := original(ctx, tx, access)
		if err == nil && access.Intent == "renew" {
			authorized = true
		}
		return err
	}
	s.config.Clock = editLeaseReviewClockFunc(func() (string, time.Duration, error) {
		boot, now, err := clock.Now()
		// Simulate descheduling immediately after the final authorization
		// snapshot. A subsequent clock sample may not silently revive it.
		if authorized {
			authorized = false
			clock.advance(s.config.TTL)
		}
		return boot, now, err
	})
	_, err = s.Renew(t.Context(), lease)
	if err != nil && !errors.Is(err, ErrEditLeaseStale) {
		t.Fatal(err)
	}
	s.config.Clock = clock
	if err := editWrite(t, s, db, lease); !errors.Is(err, ErrEditLeaseStale) {
		t.Fatalf("renewal translated expired validated snapshot into a live lease: %v", err)
	}
}
