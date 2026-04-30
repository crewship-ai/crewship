package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Refresh-token rotation must succeed when the inbound JTI matches the
// stored current_refresh_jti — that's the happy-path rotation that
// every legitimate refresh hits.
func TestRotateRefreshJti_HappyPath(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// First rotation: stored is NULL, inbound expectedJti can be
	// anything (we use an empty string here mirroring how the very
	// first refresh-cookie issued at signin has its jti only on the
	// token, not yet in the row).
	if err := store.RotateRefreshJti(ctx, sess.ID, "", "jti-1"); err != nil {
		t.Fatalf("first rotation: %v", err)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CurrentRefreshJti != "jti-1" {
		t.Errorf("CurrentRefreshJti: got %q want jti-1", got.CurrentRefreshJti)
	}

	// Second rotation: must pass the previous JTI.
	if err := store.RotateRefreshJti(ctx, sess.ID, "jti-1", "jti-2"); err != nil {
		t.Fatalf("second rotation: %v", err)
	}
	got, err = store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get after second rotation: %v", err)
	}
	if got.CurrentRefreshJti != "jti-2" {
		t.Errorf("CurrentRefreshJti: got %q want jti-2", got.CurrentRefreshJti)
	}
}

// The whole point of CAS rotation: if a request shows up carrying a
// JTI we've already rotated past, that's a token-theft signal. The
// store must surface ErrJTIMismatch so the caller can revoke the
// session (the caller does the revocation, not the store, to keep
// responsibilities narrow).
func TestRotateRefreshJti_ReplayDetected(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Legitimate rotation moves the jti from NULL → jti-1.
	if err := store.RotateRefreshJti(ctx, sess.ID, "", "jti-1"); err != nil {
		t.Fatalf("rotate 1: %v", err)
	}
	if err := store.RotateRefreshJti(ctx, sess.ID, "jti-1", "jti-2"); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}

	// Attacker now arrives with the old jti-1 (or anything other
	// than jti-2). Must be flagged.
	err = store.RotateRefreshJti(ctx, sess.ID, "jti-1", "attacker-new-jti")
	if !errors.Is(err, ErrJTIMismatch) {
		t.Fatalf("got %v, want ErrJTIMismatch", err)
	}

	// The store leaves the row alone — caller is responsible for
	// revoking. Verify current_refresh_jti is still jti-2 (the
	// legitimate state), not the attacker's jti.
	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CurrentRefreshJti != "jti-2" {
		t.Errorf("attacker's call shouldn't have advanced the jti; got %q want jti-2", got.CurrentRefreshJti)
	}
}

// A revoked session can't be rotated — even with the right JTI. Once
// revoked_at is set, every refresh path must fail; the row is dead.
func TestRotateRefreshJti_RevokedSession(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.RotateRefreshJti(ctx, sess.ID, "", "jti-1"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := store.Revoke(ctx, sess.ID, ReasonAdminForce); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	err = store.RotateRefreshJti(ctx, sess.ID, "jti-1", "jti-2")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("rotate on revoked session: got %v, want ErrNotFound", err)
	}
}

// Unknown session id surfaces as ErrNotFound, not ErrJTIMismatch —
// "session never existed" and "session existed but jti is wrong" are
// different security events. The first is a stale client; the second
// is theft.
func TestRotateRefreshJti_UnknownSession(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	err := store.RotateRefreshJti(context.Background(), "s_does-not-exist", "", "jti-1")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// Concurrent rotations against the same session: only one can win.
// Without atomicity an attacker racing the user could cause both to
// believe their JTI is current. The CAS in RotateRefreshJti has to
// be linearizable.
func TestRotateRefreshJti_ConcurrentSafety(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.RotateRefreshJti(ctx, sess.ID, "", "jti-0"); err != nil {
		t.Fatalf("seed jti: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = store.RotateRefreshJti(ctx, sess.ID, "jti-0", "winner-from-"+string(rune('a'+idx)))
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, ErrJTIMismatch) {
			t.Errorf("unexpected error: %v", e)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 successful rotation, got %d", successes)
	}
}

// First-rotation must work regardless of whether the caller passes
// the empty string or the actual signin-time JTI as expectedJti.
// (The signin path doesn't store a JTI on Create — by design — so the
// first refresh comes in with stored=NULL and expected=<signin-jti>.
// Either form must succeed.)
func TestRotateRefreshJti_FirstRotationAnyExpected(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.RotateRefreshJti(ctx, sess.ID, "anything-at-all", "jti-1"); err != nil {
		t.Errorf("first rotation should accept any expectedJti when row has NULL: %v", err)
	}
}
