// Package crashreport routes Go-side crash and error events to an external
// crash-reporting backend (Sentry by default) when the operator has opted in.
//
// The package is built so that:
//
//   - No DSN at build time → fully no-op. The Init/Capture/Flush calls become
//     cheap function dispatches and no network connections are opened. Local
//     dev builds and air-gapped deployments cost nothing.
//   - DSN present but opt_in=false → no-op until the operator runs
//     `crewship telemetry on`. The setting lives in the app_settings table
//     and survives binary upgrades.
//   - DSN present + opt_in=true → events flow to the backend, with strict
//     scrubbing of request bodies, headers, query params, and env vars.
//
// The package never reads credentials from disk and never sends payloads
// the operator hasn't opted into. It is named "crashreport" rather than
// "telemetry" because the project's internal/telemetry package is the
// OpenTelemetry tracing pipeline — a different concern.
package crashreport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DSN is the Sentry/etc endpoint, injected at build time via ldflags:
//
//	go build -ldflags="-X github.com/crewship-ai/crewship/internal/crashreport.DSN=https://...@sentry.io/..."
//
// Goreleaser pipes the value from the SENTRY_DSN GitHub Actions secret. Local
// builds leave it empty, which short-circuits Init() into no-op mode.
var DSN = ""

// SettingOptIn is the app_settings key that stores the operator's
// telemetry consent. Values: "1" = opted in, "0" = opted out, absent = not
// yet asked.
const SettingOptIn = "telemetry_opt_in"

// SettingInstallID is the anonymous identifier generated on first opt-in.
// Lets the backend group crashes by install without ever seeing user data.
const SettingInstallID = "telemetry_install_id"

// State holds the resolved consent + identity state for the current process.
// It's set by Init() and read by Capture(). Nil State means "not yet
// initialized" — Capture is a safe no-op until Init runs.
type State struct {
	enabled   bool
	installID string
	version   string
	logger    *slog.Logger
}

var (
	state   atomicState
	backend Backend = noopBackend{}
)

// atomicState gives a lock-free read path for Capture(), which can be called
// from any goroutine in the hot path. Init() writes once on startup; readers
// see a consistent snapshot.
type atomicState struct {
	mu    sync.RWMutex
	value *State
}

func (a *atomicState) load() *State {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.value
}

func (a *atomicState) store(s *State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.value = s
}

// Backend is the minimal surface crash reporting needs from whatever
// provider is plugged in. Keeping it abstract lets us swap Sentry for a
// self-hosted endpoint later without touching call sites.
type Backend interface {
	Init(dsn, installID, version string) error
	Capture(err error, tags map[string]string)
	Flush(timeout time.Duration)
}

// SetBackend lets tests swap in a recording fake, and lets the real Sentry
// adapter register itself from sentry_adapter.go in an init() block.
func SetBackend(b Backend) {
	if b == nil {
		backend = noopBackend{}
		return
	}
	backend = b
}

// Init reads the consent state from the DB, generates an install ID on
// first opt-in, and primes the configured backend. Returns nil on success
// or on a "telemetry is off" decision — only DB errors are returned.
//
// Safe to call multiple times; each call refreshes state, which matters
// after `crewship telemetry on/off` flips the setting at runtime.
func Init(ctx context.Context, db *sql.DB, version string, logger *slog.Logger) error {
	enabled, err := optInEnabled(ctx, db)
	if err != nil {
		return err
	}
	if !enabled {
		state.store(&State{enabled: false, version: version, logger: logger})
		return nil
	}

	installID, err := ensureInstallID(ctx, db)
	if err != nil {
		return err
	}

	if DSN == "" {
		// Opt-in is on but no DSN was baked in. Log once and stay quiet —
		// this is the expected state for unsigned local builds.
		logger.Info("crashreport: opt-in is on but no DSN compiled in; staying off")
		state.store(&State{enabled: false, installID: installID, version: version, logger: logger})
		return nil
	}

	if err := backend.Init(DSN, installID, version); err != nil {
		// A backend failure is not fatal — the binary must still boot.
		logger.Warn("crashreport backend init failed; continuing without telemetry", "error", err)
		state.store(&State{enabled: false, installID: installID, version: version, logger: logger})
		return nil
	}

	logger.Info("crashreport enabled", "install_id_prefix", installID[:8], "version", version)
	state.store(&State{enabled: true, installID: installID, version: version, logger: logger})
	return nil
}

// IsEnabled reports the current consent + DSN state.
func IsEnabled() bool {
	s := state.load()
	return s != nil && s.enabled
}

// Capture reports an error to the backend if crash reporting is enabled.
// Tags are optional cardinality-aware labels (e.g. "feature":"backup").
// Caller errors without telemetry-relevant context can call
// Capture(err, nil).
//
// Capture must NOT block the hot path. The Sentry adapter dispatches events
// asynchronously; the noop backend returns instantly.
func Capture(err error, tags map[string]string) {
	if err == nil {
		return
	}
	s := state.load()
	if s == nil || !s.enabled {
		return
	}
	backend.Capture(err, tags)
}

// Flush gives the backend a chance to drain pending events before the
// process exits. Call from cmd_start cleanup.
func Flush(timeout time.Duration) {
	s := state.load()
	if s == nil || !s.enabled {
		return
	}
	backend.Flush(timeout)
}

// SetOptIn writes the consent setting and, if turning on, generates an
// install ID. Returns the resolved (enabled, installID) so callers can
// pretty-print confirmation.
func SetOptIn(ctx context.Context, db *sql.DB, enabled bool) (bool, string, error) {
	val := "0"
	if enabled {
		val = "1"
	}
	if err := upsertSetting(ctx, db, SettingOptIn, val); err != nil {
		return false, "", err
	}
	if !enabled {
		return false, "", nil
	}
	installID, err := ensureInstallID(ctx, db)
	if err != nil {
		return true, "", err
	}
	return true, installID, nil
}

// Status reports the current consent state from the DB. Returns
// (enabled, asked, installID, err). `asked` is false when the operator
// has never been prompted — cmd_start uses this to drive the first-run
// prompt.
func Status(ctx context.Context, db *sql.DB) (enabled, asked bool, installID string, err error) {
	val, found, err := readSetting(ctx, db, SettingOptIn)
	if err != nil {
		return false, false, "", err
	}
	if !found {
		return false, false, "", nil
	}
	installID, _, err = readSetting(ctx, db, SettingInstallID)
	if err != nil {
		return val == "1", true, "", err
	}
	return val == "1", true, installID, nil
}

// --- helpers ---

func optInEnabled(ctx context.Context, db *sql.DB) (bool, error) {
	val, found, err := readSetting(ctx, db, SettingOptIn)
	if err != nil || !found {
		return false, err
	}
	return val == "1", nil
}

func ensureInstallID(ctx context.Context, db *sql.DB) (string, error) {
	id, found, err := readSetting(ctx, db, SettingInstallID)
	if err != nil {
		return "", err
	}
	if found && id != "" {
		return id, nil
	}
	// 16 random bytes → 32 hex chars. Not a UUID (no version bits) but
	// indistinguishable from one for grouping purposes.
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id = hex.EncodeToString(buf)
	if err := upsertSetting(ctx, db, SettingInstallID, id); err != nil {
		return "", err
	}
	return id, nil
}

func readSetting(ctx context.Context, db *sql.DB, key string) (string, bool, error) {
	var val string
	err := db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func upsertSetting(ctx context.Context, db *sql.DB, key, value string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')
	`, key, value)
	return err
}

// --- scrubbing helpers exposed for the Sentry adapter's BeforeSend hook ---

// ShouldScrubHeader returns true for any header that may carry credentials.
// The list is conservative — we'd rather over-scrub than leak a stray bearer
// token into a crash report.
func ShouldScrubHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "set-cookie", "x-api-key",
		"x-auth-token", "x-csrf-token", "x-amz-security-token",
		"proxy-authorization", "x-crewship-internal-token":
		return true
	}
	return false
}

// ShouldScrubQueryKey mirrors ShouldScrubHeader for query-string parameters.
// Anything that *looks* secret-ish is dropped.
func ShouldScrubQueryKey(key string) bool {
	k := strings.ToLower(key)
	for _, needle := range []string{"token", "secret", "password", "auth", "session", "code", "key"} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// --- backends ---

type noopBackend struct{}

func (noopBackend) Init(_, _, _ string) error            { return nil }
func (noopBackend) Capture(_ error, _ map[string]string) {}
func (noopBackend) Flush(_ time.Duration)                {}
