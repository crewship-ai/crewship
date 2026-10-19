package auth

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestStreamCredentialRevocationDoesNotRejectOtherIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		notify func(*StreamCredentials)
	}{
		{"other-session", func(r *StreamCredentials) { r.NotifySessionRevoked("other-session") }},
		{"other-token", func(r *StreamCredentials) { r.NotifyTokenRevoked("other-token") }},
		{"other-user-sessions", func(r *StreamCredentials) { r.NotifyUserRevoked("other-user") }},
		{"other-user-tokens", func(r *StreamCredentials) { r.NotifyTokensRevoked("other-user") }},
		{"other-account", func(r *StreamCredentials) { r.NotifyAccountRevoked("other-user") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r StreamCredentials
			snapshot := r.Epoch()
			tc.notify(&r)
			ctx, release := r.Track(t.Context(), snapshot, "own-user", "own-session", "", time.Time{})
			defer release()
			if !StreamCredentialCurrent(ctx) {
				t.Fatal("unrelated revocation rejected a valid user's admission")
			}
		})
	}
}

func TestStreamCredentialScopedPendingEpochs(t *testing.T) {
	cases := []struct {
		name           string
		notify         func(*StreamCredentials)
		session, token bool
	}{
		{"session", func(r *StreamCredentials) { r.NotifySessionRevoked("own-session") }, false, true},
		{"token", func(r *StreamCredentials) { r.NotifyTokenRevoked("own-token") }, true, false},
		{"user-sessions", func(r *StreamCredentials) { r.NotifyUserRevoked("own-user") }, false, true},
		{"user-tokens", func(r *StreamCredentials) { r.NotifyTokensRevoked("own-user") }, true, false},
		{"account", func(r *StreamCredentials) { r.NotifyAccountRevoked("own-user") }, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r StreamCredentials
			browser, token := r.Epoch(), r.Epoch()
			tc.notify(&r)
			// Releasing a newer, failed authentication must not forget an older race.
			failed := r.Epoch()
			r.ReleaseEpoch(failed)
			r.ReleaseEpoch(failed)
			b, bc := r.Track(t.Context(), browser, "own-user", "own-session", "", time.Time{})
			defer bc()
			c, cc := r.Track(t.Context(), token, "own-user", "", "own-token", time.Time{})
			defer cc()
			if StreamCredentialCurrent(b) != tc.session || StreamCredentialCurrent(c) != tc.token {
				t.Fatal("scoped revocation lost or widened during pending cleanup")
			}
			r.mu.Lock()
			pending, history := len(r.pending), len(r.revocations)
			r.mu.Unlock()
			if pending != 0 || history != 0 {
				t.Fatalf("completed admissions retain pending=%d history=%d", pending, history)
			}
		})
	}
}

func TestStreamCredentialAbandonedAndReplayedEpochDenied(t *testing.T) {
	var r StreamCredentials
	snapshot := r.Epoch()
	r.NotifyAccountRevoked("own-user")
	r.ReleaseEpoch(snapshot)
	ctx, release := r.Track(t.Context(), snapshot, "own-user", "own-session", "", time.Time{})
	defer release()
	if StreamCredentialCurrent(ctx) {
		t.Fatal("released admission snapshot accepted")
	}
	snapshot = r.Epoch()
	ctx, release = r.Track(t.Context(), snapshot, "own-user", "own-session", "", time.Time{})
	defer release()
	if !StreamCredentialCurrent(ctx) {
		t.Fatal("fresh validated snapshot denied")
	}
	replay, cleanup := r.Track(t.Context(), snapshot, "own-user", "own-session", "", time.Time{})
	defer cleanup()
	if StreamCredentialCurrent(replay) {
		t.Fatal("consumed snapshot replayed")
	}
	snapshot = r.Epoch()
	r.NotifyTokenRevoked("other-token")
	// This named invalid input deliberately exercises nil-context refusal and GC.
	invalidInput := struct{ ctx context.Context }{}
	ctx, cleanup = r.Track(invalidInput.ctx, snapshot, "own-user", "own-session", "", time.Time{})
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Fatal("nil-context admission was not refused")
	}
	cleanup()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) != 0 || len(r.revocations) != 0 {
		t.Fatal("nil-context admission retained pending history")
	}
}

func BenchmarkStreamCredentialAdmission(b *testing.B) {
	for _, count := range []int{1, 1000} {
		b.Run(strconv.Itoa(count)+"-pending", func(b *testing.B) {
			var r StreamCredentials
			pending := make([]uint64, count)
			for i := range pending {
				pending[i] = r.Epoch()
			}
			b.Cleanup(func() {
				for _, epoch := range pending {
					r.ReleaseEpoch(epoch)
				}
			})
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				epoch := r.Epoch()
				ctx, release := r.Track(b.Context(), epoch, "bench-user", "bench-session", "", time.Time{})
				if !StreamCredentialCurrent(ctx) {
					b.Fatal("valid benchmark admission rejected")
				}
				release()
				r.ReleaseEpoch(epoch)
			}
		})
	}
}
