package crashreport

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

func setupDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open("file:" + filepath.Join(t.TempDir(), "cr.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

// fakeBackend records what Capture/Flush received so the tests can assert
// on behaviour without standing up a real Sentry client.
type fakeBackend struct {
	mu       sync.Mutex
	inited   bool
	dsn      string
	captured []error
	flushes  int
}

func (f *fakeBackend) Init(dsn, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inited = true
	f.dsn = dsn
	return nil
}

func (f *fakeBackend) Capture(err error, _ map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = append(f.captured, err)
}

func (f *fakeBackend) Flush(_ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
}

// resetState resets package globals between tests since they leak across
// subtests via the atomic state holder and SetBackend.
func resetState(t *testing.T) {
	t.Helper()
	state.store(nil)
	t.Cleanup(func() {
		state.store(nil)
		// Restore the real Sentry backend default for downstream tests
		// that don't override.
		SetBackend(nil)
	})
}

// TestStatus_NotAsked confirms an untouched app_settings table reports the
// "first run" state cmd_start uses to drive the consent prompt.
func TestStatus_NotAsked(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	enabled, asked, _, err := Status(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if enabled {
		t.Error("enabled should be false when not asked")
	}
	if asked {
		t.Error("asked should be false on a fresh DB")
	}
}

// TestSetOptIn_GeneratesInstallID guards the privacy invariant: opting in
// must produce an install ID, opting out must not.
func TestSetOptIn_GeneratesInstallID(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	ctx := context.Background()

	on, idA, err := SetOptIn(ctx, db.DB, true)
	if err != nil {
		t.Fatalf("SetOptIn(true): %v", err)
	}
	if !on || idA == "" {
		t.Fatalf("expected enabled=true and non-empty ID, got %v %q", on, idA)
	}
	if len(idA) != 32 {
		t.Errorf("install ID should be 32 hex chars, got %d (%q)", len(idA), idA)
	}

	// Toggling off then on must produce the SAME install ID — re-rolling
	// every time would make crash grouping unstable.
	off, _, err := SetOptIn(ctx, db.DB, false)
	if err != nil {
		t.Fatalf("SetOptIn(false): %v", err)
	}
	if off {
		t.Error("SetOptIn(false) should return enabled=false")
	}

	_, idB, err := SetOptIn(ctx, db.DB, true)
	if err != nil {
		t.Fatalf("SetOptIn(true) #2: %v", err)
	}
	if idB != idA {
		t.Errorf("install ID changed across opt-in cycles: %q -> %q", idA, idB)
	}
}

// TestInit_NoDSN simulates a build with DSN unset (the default for local
// `go test`). Even with opt-in=true, crashreport must stay off — we won't
// silently route to a non-existent endpoint.
func TestInit_NoDSN(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, _, err := SetOptIn(context.Background(), db.DB, true); err != nil {
		t.Fatalf("SetOptIn: %v", err)
	}

	prev := DSN
	DSN = ""
	t.Cleanup(func() { DSN = prev })

	if err := Init(context.Background(), db.DB, "v0.1.0", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if IsEnabled() {
		t.Error("crashreport must stay disabled when DSN is empty")
	}
	if fake.inited {
		t.Error("backend.Init should NOT be called without DSN")
	}
}

// TestInit_OptedOut confirms the consent gate: backend.Init must not run
// when the operator has not opted in, regardless of DSN presence.
func TestInit_OptedOut(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	if err := Init(context.Background(), db.DB, "v0.1.0", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if IsEnabled() {
		t.Error("crashreport must stay disabled when consent absent")
	}
	if fake.inited {
		t.Error("backend.Init must not run without consent")
	}
}

// TestInit_HappyPath: DSN + consent => backend initialised, Capture flows.
func TestInit_HappyPath(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, _, err := SetOptIn(context.Background(), db.DB, true); err != nil {
		t.Fatalf("SetOptIn: %v", err)
	}

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	if err := Init(context.Background(), db.DB, "v0.1.0", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !IsEnabled() {
		t.Fatal("crashreport should be enabled")
	}
	if !fake.inited {
		t.Error("backend.Init should have run")
	}

	Capture(errors.New("boom"), map[string]string{"feature": "test"})
	if len(fake.captured) != 1 {
		t.Errorf("expected 1 captured error, got %d", len(fake.captured))
	}

	Flush(100 * time.Millisecond)
	if fake.flushes != 1 {
		t.Errorf("expected 1 flush, got %d", fake.flushes)
	}
}

// TestCapture_NoOpWhenDisabled is a regression against the "crashreport
// stays on after opt-out" bug class: events captured while disabled must
// not queue up for the next opt-in.
func TestCapture_NoOpWhenDisabled(t *testing.T) {
	resetState(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	// state.value is nil — Capture should bail out cleanly.
	Capture(errors.New("nope"), nil)
	if len(fake.captured) != 0 {
		t.Errorf("Capture should be a no-op when state is nil; got %d", len(fake.captured))
	}
}

// TestShouldScrubHeader makes the scrub list explicit: anything added
// must be exercised by the test, anything removed must update the test
// in the same PR.
func TestShouldScrubHeader(t *testing.T) {
	scrub := []string{
		"Authorization", "authorization", "Cookie", "Set-Cookie",
		"X-API-Key", "x-auth-token", "X-CSRF-Token",
		"X-Amz-Security-Token", "Proxy-Authorization",
		"X-Crewship-Internal-Token",
	}
	keep := []string{
		"Content-Type", "Accept", "User-Agent", "Host", "Referer",
	}
	for _, h := range scrub {
		if !ShouldScrubHeader(h) {
			t.Errorf("ShouldScrubHeader(%q) = false, want true", h)
		}
	}
	for _, h := range keep {
		if ShouldScrubHeader(h) {
			t.Errorf("ShouldScrubHeader(%q) = true, want false", h)
		}
	}
}

func TestShouldScrubQueryKey(t *testing.T) {
	scrub := []string{"token", "access_token", "api_key", "session", "auth", "code"}
	keep := []string{"limit", "offset", "page", "sort", "filter"}
	for _, k := range scrub {
		if !ShouldScrubQueryKey(k) {
			t.Errorf("ShouldScrubQueryKey(%q) = false, want true", k)
		}
	}
	for _, k := range keep {
		if ShouldScrubQueryKey(k) {
			t.Errorf("ShouldScrubQueryKey(%q) = true, want false", k)
		}
	}
}
