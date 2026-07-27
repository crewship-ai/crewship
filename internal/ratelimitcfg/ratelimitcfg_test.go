package ratelimitcfg

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// tableDDL mirrors migrate_consts_v168_rate_limit_overrides.go. Duplicated
// here so the store unit tests don't drag in the full migration stack; the
// backup totality guard keeps the real schema honest.
const tableDDL = `
CREATE TABLE rate_limit_overrides (
    key         TEXT PRIMARY KEY,
    value       INTEGER NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_by  TEXT
);`

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(tableDDL); err != nil {
		t.Fatalf("create table: %v", err)
	}
	s := New(db)
	if err := s.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func TestStore_DefaultsBeforeAnyOverride(t *testing.T) {
	s := newTestStore(t)

	if got := s.Value(KeyHTTPAuthPerMin); got != 10 {
		t.Errorf("auth default = %d, want 10", got)
	}
	if got := s.Value(KeyHTTPAPIPerMin); got != 120 {
		t.Errorf("api default = %d, want 120", got)
	}
	if got := s.Dur(KeyLoginLockoutDurSec); got != 5*time.Minute {
		t.Errorf("lockout duration default = %s, want 5m", got)
	}

	// Every registry entry lists un-overridden with its default.
	for _, st := range s.List() {
		if st.Overridden {
			t.Errorf("%s reported overridden with no override set", st.Key)
		}
		if st.Value != st.Default {
			t.Errorf("%s value %d != default %d", st.Key, st.Value, st.Default)
		}
	}
}

func TestStore_SetOverridesValueAndPersists(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, KeyHTTPAuthPerMin, 40, "admin@x"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := s.Value(KeyHTTPAuthPerMin); got != 40 {
		t.Errorf("after set, value = %d, want 40", got)
	}

	// A fresh store loading the same DB must see the persisted override —
	// proves it hit the table, not just the in-memory cache.
	reloaded := New(s.db)
	if err := reloaded.Load(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Value(KeyHTTPAuthPerMin); got != 40 {
		t.Errorf("reloaded value = %d, want 40 (override must persist)", got)
	}

	var state State
	for _, st := range reloaded.List() {
		if st.Key == KeyHTTPAuthPerMin {
			state = st
		}
	}
	if !state.Overridden || state.Value != 40 {
		t.Errorf("List() = %+v, want overridden=true value=40", state)
	}
}

func TestStore_ResetRevertsToDefault(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, KeyHTTPAPIPerMin, 500, ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Reset(ctx, KeyHTTPAPIPerMin, "admin@x"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := s.Value(KeyHTTPAPIPerMin); got != 120 {
		t.Errorf("after reset, value = %d, want default 120", got)
	}

	// Resetting a key with no override is a no-op success.
	if err := s.Reset(ctx, KeyHTTPAPIPerMin, ""); err != nil {
		t.Errorf("reset of unset key should be a no-op, got %v", err)
	}
}

func TestStore_SetRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Set(ctx, "no.such.key", 5, ""); err == nil || !IsValidation(err) {
		t.Errorf("unknown key: got err=%v, want a validation error", err)
	}
	if err := s.Set(ctx, KeyHTTPAuthPerMin, 0, ""); err == nil || !IsValidation(err) {
		t.Errorf("below min: got err=%v, want a validation error", err)
	}
	if err := s.Set(ctx, KeyLoginLockoutDurSec, 999999, ""); err == nil || !IsValidation(err) {
		t.Errorf("above max: got err=%v, want a validation error", err)
	}
	// A rejected write must not have touched the value.
	if got := s.Value(KeyHTTPAuthPerMin); got != 10 {
		t.Errorf("value changed after a rejected set: %d", got)
	}
}

func TestStore_OnChangeFiresOnSetAndReset(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	calls := 0
	s.OnChange(func() { calls++ })

	if err := s.Set(ctx, KeyHTTPAuthPerMin, 25, ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Reset(ctx, KeyHTTPAuthPerMin, ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if calls != 2 {
		t.Errorf("onChange fired %d times, want 2 (one per Set/Reset)", calls)
	}

	// A rejected Set must NOT fire the callback.
	before := calls
	_ = s.Set(ctx, KeyHTTPAuthPerMin, -1, "")
	if calls != before {
		t.Errorf("onChange fired on a rejected set")
	}
}

func TestAmbient_IntFallsBackToDefaultWithoutGlobal(t *testing.T) {
	SetGlobal(nil)
	if got := Int(KeyProvMaxConcurrentWS); got != 8 {
		t.Errorf("ambient Int without global = %d, want default 8", got)
	}
	if got := Dur(KeyNotifyRefillSec); got != 30*time.Second {
		t.Errorf("ambient Dur without global = %s, want 30s", got)
	}
}

func TestAmbient_IntReadsInstalledGlobal(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.Set(ctx, KeyProvMaxConcurrentWS, 32, ""); err != nil {
		t.Fatalf("set: %v", err)
	}
	SetGlobal(s)
	t.Cleanup(func() { SetGlobal(nil) })

	if got := Int(KeyProvMaxConcurrentWS); got != 32 {
		t.Errorf("ambient Int with global = %d, want 32", got)
	}
}

func TestStore_LoadIgnoresStaleKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// A row for a key no longer in the registry (a removed limiter) must be
	// ignored, never resurrected into the effective set.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO rate_limit_overrides (key, value) VALUES ('removed.limiter', 7)`); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	if err := s.Load(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, st := range s.List() {
		if st.Key == "removed.limiter" {
			t.Fatal("stale key surfaced in List()")
		}
	}
}
