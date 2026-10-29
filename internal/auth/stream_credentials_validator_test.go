package auth

import (
	"context"
	"testing"
	"time"
)

func TestStreamCredentialFreshValidator(t *testing.T) {
	r := &StreamCredentials{}
	calls := 0
	allow := true
	ctx, release := r.Track(t.Context(), r.Epoch(), "u", "s", "", time.Now().Add(time.Hour), func(delivery context.Context) bool {
		calls++
		if delivery.Err() != nil {
			t.Error("validator received a canceled context before the check")
		}
		// Both acquire the registry mutex. A callback under that mutex would
		// deadlock instead of preserving this unrelated credential's validity.
		epoch := r.Epoch()
		r.ReleaseEpoch(epoch)
		r.NotifySessionRevoked("unrelated")
		return allow
	})
	defer release()
	other, releaseOther := r.Track(t.Context(), r.Epoch(), "u", "other", "", time.Time{})
	defer releaseOther()
	if !StreamCredentialCurrent(ctx) || calls != 1 {
		t.Fatalf("healthy validator calls=%d", calls)
	}
	allow = false
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil || calls != 2 {
		t.Fatalf("failed validator did not cancel only its context: calls=%d err=%v", calls, ctx.Err())
	}
	if !StreamCredentialCurrent(other) {
		t.Error("failed check invalidated an unrelated same-user credential")
	}
	if StreamCredentialCurrent(ctx) || calls != 2 {
		t.Error("invalidated state repeated external I/O or became valid")
	}
}

func TestStreamCredentialValidatorRechecksMemoryAfterRead(t *testing.T) {
	r := &StreamCredentials{}
	ctx, release := r.Track(t.Context(), r.Epoch(), "u", "own", "", time.Time{}, func(context.Context) bool {
		r.NotifySessionRevoked("own")
		return true
	})
	defer release()
	if StreamCredentialCurrent(ctx) || ctx.Err() == nil {
		t.Error("concurrent in-process revoke was lost after a successful fresh check")
	}
}
