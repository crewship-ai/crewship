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
	"github.com/getsentry/sentry-go"
)

// newEventWithSensitiveData builds a maximally-populated Sentry event so
// the scrubber test can verify every drop rule in one assertion pass.
// New fields added to Sentry's Event type can be appended here when we
// extend scrubEvent.
func newEventWithSensitiveData() *sentry.Event {
	return &sentry.Event{
		Contexts: map[string]sentry.Context{
			"device":                {"arch": "arm64", "num_cpu": 10},
			"runtime":               {"name": "go", "version": "go1.26"},
			"culture":               {"locale": "en_US"},
			"environment_variables": {"DATABASE_URL": "postgres://user:pw@..."},
			"os_user":               {"name": "pavelsrba"},
			"os":                    {"name": "darwin"},
		},
		User: sentry.User{
			ID:       "u-123",
			Email:    "test@example.com",
			Username: "pavel",
		},
		Modules: map[string]string{
			"github.com/foo/bar": "v1.2.3",
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Message: "did a thing", Data: map[string]interface{}{"path": "/api/v1/secret"}},
		},
	}
}

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
//
// We snapshot the backend BEFORE the test runs and restore that exact
// reference in t.Cleanup — pre-fix the cleanup blanket-set
// SetBackend(nil), which downgrades subsequent subtests to noopBackend
// instead of whatever the init()-time real adapter installed. CodeRabbit
// raised the "restore previous backend, don't force noop" note on
// review.
func resetState(t *testing.T) {
	t.Helper()
	prev := CurrentBackend()
	state.store(nil)
	t.Cleanup(func() {
		state.store(nil)
		SetBackend(prev)
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

// TestDefaultOptIn_VersionMatrix is the single source of truth for the
// default-by-version policy: telemetry defaults ON only for prerelease
// (-beta / -rc) and dev/unversioned builds, OFF for stable release
// versions. The README and docs/guides/telemetry.mdx commit to exactly
// this matrix — change them together with this test.
func TestDefaultOptIn_VersionMatrix(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		// dev / unversioned builds → ON
		{"", true},
		{"dev", true},
		{"Dev", true},
		{"  dev  ", true},
		{"a1b2c3d", true}, // bare commit SHA: not a release tag

		// prerelease builds → ON
		{"v0.1.0-beta.4", true},
		{"0.1.0-beta.4", true},
		{"v1.0.0-beta.1", true},
		{"v1.0.0-rc.2", true},
		{"v2.3.4-alpha.1", true}, // any prerelease suffix counts

		// stable release versions → OFF
		{"v1.0.0", false},
		{"1.0.0", false},
		{"v0.1.0", false},
		{"v0.2.7", false},
		{"v10.20.30", false},
		{"v1.0.0+build.5", false}, // build metadata doesn't make it a prerelease
	}
	for _, tc := range cases {
		if got := DefaultOptIn(tc.version); got != tc.want {
			t.Errorf("DefaultOptIn(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

// TestInit_PrereleaseDefaultOn locks in the prerelease opt-out semantic:
// when the operator has never written a consent setting AND a DSN is
// wired in AND the build is a prerelease (-beta/-rc) or dev build, Init
// must enable telemetry AND persist "1" so subsequent boots are
// deterministic.
func TestInit_PrereleaseDefaultOn(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	// No SetOptIn call — simulate first boot of a beta build.
	if err := Init(context.Background(), db.DB, "v0.1.0-beta.4", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !IsEnabled() {
		t.Error("prerelease default: telemetry must be enabled without prior consent setting")
	}
	if !fake.inited {
		t.Error("prerelease default: backend.Init should run on first boot")
	}

	// The "asked" flag must now be persisted so we don't keep treating
	// every subsequent boot as a fresh default decision.
	_, asked, _, err := Status(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !asked {
		t.Error("Init should have persisted consent setting after default-on decision")
	}
}

// TestInit_StableDefaultOff is the v1.0-GA opt-in contract: on a stable
// release version with no recorded consent, Init must NOT enable
// telemetry and must NOT write a consent row — the decision belongs to
// the operator (onboarding consent step or `crewship telemetry on`).
func TestInit_StableDefaultOff(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	// No SetOptIn call — simulate first boot of a stable build.
	if err := Init(context.Background(), db.DB, "v1.0.0", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if IsEnabled() {
		t.Error("stable default: telemetry must stay disabled without explicit consent")
	}
	if fake.inited {
		t.Error("stable default: backend.Init must not run without explicit consent")
	}

	// No consent row may be written: "asked" stays false so onboarding
	// (web wizard / `crewship setup`) knows the user still owes a choice,
	// and a later prerelease downgrade can't misread a phantom opt-out.
	_, asked, _, err := Status(context.Background(), db.DB)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if asked {
		t.Error("Init must not persist a consent row on the stable default-off path")
	}
}

// TestInit_StickyOptInOnStable mirrors TestInit_StickyOptOut from the
// other direction: an explicit `crewship telemetry on` must keep
// telemetry enabled on a stable build even though the version default
// is off. The sticky operator override always wins over the default.
func TestInit_StickyOptInOnStable(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	// Operator runs `crewship telemetry on`.
	if _, _, err := SetOptIn(context.Background(), db.DB, true); err != nil {
		t.Fatalf("SetOptIn(true): %v", err)
	}
	if err := Init(context.Background(), db.DB, "v1.0.0", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !IsEnabled() {
		t.Error("explicit opt-in must win over the stable default-off")
	}
	if !fake.inited {
		t.Error("backend.Init should run after explicit opt-in on a stable build")
	}
}

// TestInit_StickyOptOut guards the higher-priority invariant: if the
// operator ever writes "0" (explicit opt-out), Init must NOT flip it back
// to enabled on the next boot, regardless of the prerelease default-on.
// This is what makes `crewship telemetry off` reliable.
func TestInit_StickyOptOut(t *testing.T) {
	resetState(t)
	db := setupDB(t)
	fake := &fakeBackend{}
	SetBackend(fake)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	prev := DSN
	DSN = "https://fake@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	// Operator runs `crewship telemetry off`.
	if _, _, err := SetOptIn(context.Background(), db.DB, false); err != nil {
		t.Fatalf("SetOptIn(false): %v", err)
	}
	// Prerelease version on purpose: this is the build flavour whose
	// default-on would flip the bit back if stickiness ever regressed.
	if err := Init(context.Background(), db.DB, "v0.1.0-beta.4", logger); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if IsEnabled() {
		t.Error("explicit opt-out must stick across Init — prerelease default cannot override")
	}
	if fake.inited {
		t.Error("backend.Init must not run after explicit opt-out")
	}
}

// TestResolveDSN_EnvOverride locks in the CREWSHIP_SENTRY_DSN escape
// hatch: self-hosted operators who want to route crashes to their OWN
// Sentry (or a self-hosted instance) set the env var, and it takes
// priority over the ldflag-baked vendor DSN.
func TestResolveDSN_EnvOverride(t *testing.T) {
	prev := DSN
	DSN = "https://vendor@sentry.example/1"
	t.Cleanup(func() { DSN = prev })

	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	if got := ResolveDSN(); got != "https://vendor@sentry.example/1" {
		t.Errorf("empty env should yield vendor DSN, got %q", got)
	}

	t.Setenv("CREWSHIP_SENTRY_DSN", "https://operator@self.example/9")
	if got := ResolveDSN(); got != "https://operator@self.example/9" {
		t.Errorf("env var should override, got %q", got)
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

// TestScrubEvent_DropsLeakyContexts pins the scrub list against future
// sentry-go upgrades. If a dep bump adds a new field under any of these
// context keys, the test still passes because we drop them wholesale; if
// someone removes one of the delete() calls in scrubEvent, this test
// fails.
func TestScrubEvent_DropsLeakyContexts(t *testing.T) {
	event := newEventWithSensitiveData()
	got := scrubEvent(event, nil)
	if got == nil {
		t.Fatal("scrubEvent returned nil for non-nil input")
	}
	for _, key := range []string{"device", "runtime", "culture", "environment_variables", "os_user"} {
		if _, present := got.Contexts[key]; present {
			t.Errorf("context %q must be dropped, still present", key)
		}
	}
	// "os" context (GOOS name) IS retained — useful for triage and harmless.
	if _, present := got.Contexts["os"]; !present {
		t.Error(`"os" context should be preserved`)
	}
	if got.User.Email != "" || got.User.ID != "" || got.User.Username != "" {
		t.Errorf("User field must be cleared, got %+v", got.User)
	}
	if len(got.Modules) != 0 {
		t.Errorf("Modules must be cleared, got %d entries", len(got.Modules))
	}
	for i, bc := range got.Breadcrumbs {
		if bc != nil && bc.Data != nil {
			t.Errorf("breadcrumb[%d].Data must be cleared, got %+v", i, bc.Data)
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
