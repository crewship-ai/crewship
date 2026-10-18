package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestStreamCredentialLegacyAndCleanup(t *testing.T) {
	if !StreamCredentialCurrent(context.Background()) {
		t.Fatal("untracked legacy context denied")
	}
	var r StreamCredentials
	ctx, cleanup := r.Track(context.Background(), r.Epoch(), "u", "s", "", time.Time{})
	if !StreamCredentialCurrent(ctx) {
		t.Fatal("valid no-expiry session denied")
	}
	cleanup()
	cleanup()
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Fatal("cleanup did not invalidate and cancel")
	}
	r.mu.Lock()
	n := len(r.active)
	r.mu.Unlock()
	if n != 0 {
		t.Fatal("cleanup retained entry")
	}
}

func TestStreamCredentialRevocationKinds(t *testing.T) {
	cases := []struct {
		name                  string
		notify                func(*StreamCredentials)
		session, token, other bool
	}{
		{"session", func(r *StreamCredentials) { r.NotifySessionRevoked("s") }, false, true, true},
		{"user-sessions", func(r *StreamCredentials) { r.NotifyUserRevoked("u") }, false, true, true},
		{"token", func(r *StreamCredentials) { r.NotifyTokenRevoked("t") }, true, false, true},
		{"user-tokens-keeps-browser-session", func(r *StreamCredentials) { r.NotifyTokensRevoked("u") }, true, false, true},
		{"account", func(r *StreamCredentials) { r.NotifyAccountRevoked("u") }, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r StreamCredentials
			s, sc := r.Track(t.Context(), r.Epoch(), "u", "s", "", time.Time{})
			defer sc()
			token, tcl := r.Track(t.Context(), r.Epoch(), "u", "", "t", time.Time{})
			defer tcl()
			other, oc := r.Track(t.Context(), r.Epoch(), "other", "s-other", "", time.Time{})
			defer oc()
			before := r.Epoch()
			tc.notify(&r)
			if r.Epoch() <= before {
				t.Fatal("notification did not advance epoch")
			}
			for _, item := range []struct {
				ctx  context.Context
				want bool
			}{{s, tc.session}, {token, tc.token}, {other, tc.other}} {
				if StreamCredentialCurrent(item.ctx) != item.want {
					t.Fatal("wrong revocation audience")
				}
				if !item.want && item.ctx.Err() == nil {
					t.Fatal("notification returned before cancellation")
				}
			}
		})
	}
}

func TestStreamCredentialStaleAdmissionAndInvalidIdentity(t *testing.T) {
	var r StreamCredentials
	epoch := r.Epoch()
	r.NotifyTokenRevoked("unrelated")
	ctx, cleanup := r.Track(t.Context(), epoch, "u", "s", "", time.Time{})
	defer cleanup()
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Fatal("validation epoch race admitted stale stream")
	}
	for _, ids := range [][3]string{{"", "s", ""}, {"u", "", ""}, {"u", "s", "t"}} {
		ctx, cleanup := r.Track(t.Context(), r.Epoch(), ids[0], ids[1], ids[2], time.Time{})
		cleanup()
		if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
			t.Fatal("invalid identity admitted")
		}
	}
	r.mu.Lock()
	n := len(r.active)
	r.mu.Unlock()
	if n != 0 {
		t.Fatal("rejected admission retained entries")
	}
}

func TestStreamCredentialExpiryAndParentCancel(t *testing.T) {
	var r StreamCredentials
	past, pc := r.Track(t.Context(), r.Epoch(), "u", "s", "", time.Now().Add(-time.Second))
	defer pc()
	if StreamCredentialCurrent(past) || past.Err() == nil {
		t.Fatal("expired credential admitted")
	}
	parent, cancel := context.WithCancel(t.Context())
	ctx, cleanup := r.Track(parent, r.Epoch(), "u", "", "t", time.Time{})
	defer cleanup()
	cancel()
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Fatal("parent cancellation ignored")
	}
}

func TestStreamCredentialExpiryDoesNotDependOnTimer(t *testing.T) {
	var r StreamCredentials
	expires := time.Now().Add(250 * time.Millisecond)
	ctx, cleanup := r.Track(t.Context(), r.Epoch(), "u", "", "t", expires)
	defer cleanup()
	// Deliberately suppress the delivery timer. The per-frame predicate must
	// still refuse an expired credential before any cancellation callback runs.
	state := ctx.Value(streamCredentialKey{}).(*streamCredentialState)
	if !state.timer.Stop() {
		t.Fatal("expiry fixture timer already fired")
	}
	time.Sleep(time.Until(expires) + time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("fixture cancellation masked per-frame expiry guard")
	}
	if StreamCredentialCurrent(ctx) {
		t.Fatal("expired credential allowed with delayed timer")
	}
}

func TestStreamCredentialExpiryCancelsIdleStream(t *testing.T) {
	var r StreamCredentials
	ctx, cleanup := r.Track(t.Context(), r.Epoch(), "u", "s", "", time.Now().Add(20*time.Millisecond))
	defer cleanup()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle stream expiry did not cancel")
	}
	if StreamCredentialCurrent(ctx) {
		t.Fatal("expired stream current")
	}
}

func TestStreamCredentialConcurrentLifecycle(t *testing.T) {
	var r StreamCredentials
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ctx, cleanup := r.Track(context.Background(), r.Epoch(), "u", "s", "", time.Time{})
				_ = StreamCredentialCurrent(ctx)
				r.NotifySessionRevoked("s")
				cleanup()
			}
		}()
	}
	wg.Wait()
	r.mu.Lock()
	n := len(r.active)
	r.mu.Unlock()
	if n != 0 {
		t.Fatal("lifecycle retained history")
	}
}

func TestStreamCredentialCanceledUntrackedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if StreamCredentialCurrent(ctx) {
		t.Fatal("canceled context is not current even without a lease")
	}
}
