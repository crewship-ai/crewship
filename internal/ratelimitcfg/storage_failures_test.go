package ratelimitcfg

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestFailedMutationKeepsLiveRateLimitsAndCallbacksUnchanged(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Set(ctx, KeyNotifyBurst, 17, "test-actor"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.OnChange(func() { calls++ })
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"set": s.Set(ctx, KeyNotifyBurst, 18, "test-actor"), "reset": s.Reset(ctx, KeyNotifyBurst, "test-actor"), "load": s.Load(ctx)} {
		if err == nil || IsValidation(err) {
			t.Errorf("%s storage error=%v", name, err)
		}
	}
	if calls != 0 || s.Value(KeyNotifyBurst) != 17 {
		t.Fatalf("failed write changed effective limit: calls=%d value=%d", calls, s.Value(KeyNotifyBurst))
	}
}

func TestMalformedReloadPreservesPreviouslyLoadedMap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Set(ctx, KeyNotifyBurst, 17, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE rate_limit_overrides SET value='not-an-integer' WHERE key=?`, KeyNotifyBurst); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(ctx); err == nil {
		t.Fatal("malformed persisted override accepted")
	}
	if s.Value(KeyNotifyBurst) != 17 {
		t.Fatal("failed reload replaced working limits")
	}
}

func TestLookupAndDurationAccessorsPreserveDefaultsAndOverrides(t *testing.T) {
	s := newTestStore(t)
	old := global.Load()
	SetGlobal(s)
	t.Cleanup(func() { SetGlobal(old) })
	if _, ok := Lookup("unknown-limit"); ok {
		t.Fatal("unknown registry key accepted")
	}
	if state, ok := s.StateFor("unknown-limit"); ok || state.Value != 0 {
		t.Fatalf("unknown state=%+v %v", state, ok)
	}
	if Int("unknown-limit") != 0 || DefaultFor("unknown-limit") != 0 {
		t.Fatal("unknown key invented a default")
	}
	state, ok := s.StateFor(KeyNotifyRefillSec)
	if !ok || state.Overridden || state.Value != DefaultFor(KeyNotifyRefillSec) {
		t.Fatalf("default state=%+v %v", state, ok)
	}
	if err := s.Set(context.Background(), KeyNotifyRefillSec, 17, ""); err != nil {
		t.Fatal(err)
	}
	if s.Dur(KeyNotifyRefillSec) != 17*time.Second || Dur(KeyNotifyRefillSec) != 17*time.Second {
		t.Fatal("duration accessors ignored override")
	}
	if err := s.Reset(context.Background(), "unknown-limit", ""); err == nil || !IsValidation(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("unknown reset classification=%v", err)
	}
}
