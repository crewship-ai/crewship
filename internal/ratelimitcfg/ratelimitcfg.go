// Package ratelimitcfg is the single source of truth for every tunable rate
// limiter in the system. Historically each limiter's value was a hardcoded
// constant scattered across the codebase (the per-IP HTTP buckets in
// router.go, the login lockout in lockout.go, the notification anti-storm
// bucket, crew provisioning, agent webhooks). That made them impossible to
// inspect or adjust without a redeploy — and the tight defaults occasionally
// bit real users (a burst of dashboard refreshes draining the auth bucket).
//
// This package moves the VALUES (never the enforcement) into one registry
// backed by an optional DB override table. Each limiter still enforces itself
// exactly where it did before; it just reads its current number from here.
//
// Two access shapes:
//
//   - Explicit *Store — held by the router and the admin handler. Used to
//     List/Set/Reset overrides and to register live-apply callbacks.
//   - Ambient package funcs Int()/Dur() — for the handful of limiters wired
//     deep in call stacks (lockout, notify, provisioning, webhook) where
//     threading a *Store would mean touching a dozen constructors. They read
//     the process-global store set once at server boot, and fall back to the
//     registry DEFAULT when no store is installed (CLI processes, unit tests)
//     — so a limiter is never left reading zero.
package ratelimitcfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Meta describes one tunable limiter value: its identity, human-facing
// labels for the admin table, and the inclusive bounds an override must fall
// within. Values are whole numbers (req/min, attempts, seconds, jobs) — no
// limiter needs sub-integer precision, and integers keep the admin UI and CLI
// unambiguous.
type Meta struct {
	Key         string `json:"key"`
	Group       string `json:"group"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Default     int    `json:"default"`
	Min         int    `json:"min"`
	Max         int    `json:"max"`
}

// Registry keys. Exported so call sites reference a symbol, not a string
// literal that could drift from the registry and silently read the default.
const (
	KeyHTTPAuthPerMin      = "http.auth_per_min"
	KeyHTTPAPIPerMin       = "http.api_per_min"
	KeyHTTPCredTestPerMin  = "http.cred_test_per_min"
	KeyLoginLockoutThresh  = "login.lockout_threshold"
	KeyLoginLockoutDurSec  = "login.lockout_duration_sec"
	KeyNotifyBurst         = "notify.burst"
	KeyNotifyRefillSec     = "notify.refill_interval_sec"
	KeyProvMaxConcurrentWS = "provisioning.max_concurrent_per_ws"
	KeyProvMaxStartsPerMin = "provisioning.max_starts_per_min"
	KeyWebhookAgentPerMin  = "webhook.agent_per_min"
)

// hardMax is a generous shared ceiling. It exists only to reject absurd
// input (a fat-fingered 1e9 that would allocate a giant burst bucket), not to
// constrain legitimate tuning — an operator raising a per-IP bucket to 5000
// is fine.
const hardMax = 100000

// registry is the ordered, authoritative list. Order is the admin-table
// display order, grouped by subsystem. Defaults mirror the values these
// limiters shipped with before they became tunable — installing this package
// changes NO behaviour until an operator sets an override.
var registry = []Meta{
	{KeyHTTPAuthPerMin, "HTTP (per-IP)", "Auth endpoints", "Login / token-refresh / bootstrap, per client IP. Read-only session polls do NOT count against this.", "req/min", 10, 1, hardMax},
	{KeyHTTPAPIPerMin, "HTTP (per-IP)", "General API", "Every other /api/* route, per client IP. Authenticated CLI tokens are exempt.", "req/min", 120, 1, hardMax},
	{KeyHTTPCredTestPerMin, "HTTP (per-IP)", "Credential test", "The credential-validation test endpoints, per IP — tighter to blunt their use as a key-validation oracle.", "req/min", 60, 1, hardMax},
	{KeyLoginLockoutThresh, "Login", "Account lockout threshold", "Consecutive failed sign-ins on one account before it locks. Layered on top of the per-IP auth bucket.", "attempts", 10, 1, hardMax},
	{KeyLoginLockoutDurSec, "Login", "Account lockout duration", "How long a locked account stays frozen before a legitimate user can retry.", "seconds", 300, 1, 86400},
	{KeyNotifyBurst, "Notifications", "Notification burst", "Max notifications one recipient can absorb on a single (channel, category) before throttling kicks in.", "tokens", 5, 1, hardMax},
	{KeyNotifyRefillSec, "Notifications", "Notification refill interval", "After a burst, one notification token is restored every N seconds.", "seconds/token", 30, 1, 86400},
	{KeyProvMaxConcurrentWS, "Provisioning", "Concurrent provisions / workspace", "How many crew provisioning jobs a single workspace may run at once.", "jobs", 8, 1, hardMax},
	{KeyProvMaxStartsPerMin, "Provisioning", "Provision starts / minute", "How many crew provisioning jobs a workspace may START per minute.", "starts/min", 20, 1, hardMax},
	{KeyWebhookAgentPerMin, "Webhooks", "Agent webhook fires", "Default cap on agent-webhook triggers per agent per minute.", "req/min", 60, 1, hardMax},
}

var byKey = func() map[string]Meta {
	m := make(map[string]Meta, len(registry))
	for _, meta := range registry {
		m[meta.Key] = meta
	}
	return m
}()

// Lookup returns the registry metadata for a key.
func Lookup(key string) (Meta, bool) {
	m, ok := byKey[key]
	return m, ok
}

// DefaultFor returns the shipped default for a key (0 for an unknown key —
// callers pass registry constants so that branch is unreachable in practice).
func DefaultFor(key string) int {
	if m, ok := byKey[key]; ok {
		return m.Default
	}
	return 0
}

// State is one row of the admin table: the metadata plus the currently
// effective value and whether an override is in force.
type State struct {
	Meta
	Value      int  `json:"value"`
	Overridden bool `json:"overridden"`
}

// Store is the DB-backed override cache. The in-memory map is authoritative
// for reads (loaded once at boot, kept current on every Set/Reset), so the
// hot path — a limiter asking for its value on every request — never touches
// the database.
type Store struct {
	db        *sql.DB
	mu        sync.RWMutex
	overrides map[string]int
	onChange  []func()
}

// New builds a Store over db. Call Load before serving to populate the cache
// from any persisted overrides.
func New(db *sql.DB) *Store {
	return &Store{db: db, overrides: make(map[string]int)}
}

// Load replaces the in-memory override cache from the rate_limit_overrides
// table. Unknown keys (left over from a removed limiter) are ignored so a
// stale row can never resurrect a limiter that no longer exists.
func (s *Store) Load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM rate_limit_overrides`)
	if err != nil {
		return fmt.Errorf("ratelimitcfg: load overrides: %w", err)
	}
	defer rows.Close()

	next := make(map[string]int)
	for rows.Next() {
		var key string
		var val int
		if err := rows.Scan(&key, &val); err != nil {
			return fmt.Errorf("ratelimitcfg: scan override: %w", err)
		}
		if _, ok := byKey[key]; ok {
			next[key] = val
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("ratelimitcfg: iterate overrides: %w", err)
	}

	s.mu.Lock()
	s.overrides = next
	s.mu.Unlock()
	return nil
}

// Value returns the currently effective value for key: the override if one is
// set, otherwise the registry default. An unknown key returns 0 (unreachable
// with the exported Key* constants).
func (s *Store) Value(key string) int {
	s.mu.RLock()
	v, ok := s.overrides[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	return DefaultFor(key)
}

// Dur is Value interpreted as a whole number of seconds.
func (s *Store) Dur(key string) time.Duration {
	return time.Duration(s.Value(key)) * time.Second
}

// List returns every limiter's current state in registry (display) order.
func (s *Store) List() []State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]State, 0, len(registry))
	for _, meta := range registry {
		v, overridden := s.overrides[meta.Key]
		if !overridden {
			v = meta.Default
		}
		out = append(out, State{Meta: meta, Value: v, Overridden: overridden})
	}
	return out
}

// StateFor returns the current state of a single limiter. ok is false for an
// unknown key.
func (s *Store) StateFor(key string) (State, bool) {
	meta, ok := byKey[key]
	if !ok {
		return State{}, false
	}
	s.mu.RLock()
	v, overridden := s.overrides[key]
	s.mu.RUnlock()
	if !overridden {
		v = meta.Default
	}
	return State{Meta: meta, Value: v, Overridden: overridden}, true
}

// ErrUnknownKey / ErrOutOfRange are returned by Set for a bad key or value so
// the admin handler can map them to 404 / 400 respectively.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func newUnknownKey(key string) error {
	return &validationError{fmt.Sprintf("ratelimitcfg: unknown limiter key %q", key)}
}
func newOutOfRange(meta Meta, val int) error {
	return &validationError{fmt.Sprintf("ratelimitcfg: %s = %d out of range [%d, %d]", meta.Key, val, meta.Min, meta.Max)}
}

// IsValidation reports whether err is a bad-input error (unknown key or
// out-of-range value) as opposed to an infrastructure failure — lets the
// handler pick 4xx vs 500.
func IsValidation(err error) bool {
	var v *validationError
	return errors.As(err, &v)
}

// Set persists an override for key and refreshes the in-memory cache, then
// fires live-apply callbacks. Validates the key exists and the value is
// within the registry bounds. actor is recorded for the audit trail (may be
// empty).
func (s *Store) Set(ctx context.Context, key string, val int, actor string) error {
	meta, ok := byKey[key]
	if !ok {
		return newUnknownKey(key)
	}
	if val < meta.Min || val > meta.Max {
		return newOutOfRange(meta, val)
	}
	// datetime('now','subsec') is computed in SQL, not formatted in Go, so
	// this never trips the RFC3339-near-SQL timestamp lint.
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO rate_limit_overrides (key, value, updated_at, updated_by)
		VALUES (?, ?, datetime('now','subsec'), ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = datetime('now','subsec'),
			updated_by = excluded.updated_by`,
		key, val, nullIfEmpty(actor)); err != nil {
		return fmt.Errorf("ratelimitcfg: persist %s: %w", key, err)
	}
	s.mu.Lock()
	s.overrides[key] = val
	s.mu.Unlock()
	s.fireOnChange()
	return nil
}

// Reset drops the override for key so it reverts to the registry default,
// then fires live-apply callbacks. Resetting a key with no override is a
// no-op success.
func (s *Store) Reset(ctx context.Context, key, actor string) error {
	if _, ok := byKey[key]; !ok {
		return newUnknownKey(key)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rate_limit_overrides WHERE key = ?`, key); err != nil {
		return fmt.Errorf("ratelimitcfg: reset %s: %w", key, err)
	}
	s.mu.Lock()
	delete(s.overrides, key)
	s.mu.Unlock()
	s.fireOnChange()
	return nil
}

// OnChange registers a callback fired (synchronously) after any Set/Reset
// commits. The router uses this to push new per-IP bucket sizes onto the
// already-running limiters. Callbacks must be cheap and non-blocking.
func (s *Store) OnChange(fn func()) {
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

func (s *Store) fireOnChange() {
	s.mu.RLock()
	cbs := make([]func(), len(s.onChange))
	copy(cbs, s.onChange)
	s.mu.RUnlock()
	for _, fn := range cbs {
		fn()
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- Ambient process-global accessor ---------------------------------------

// global is the store installed at server boot. Nil in CLI processes and unit
// tests, where the ambient readers fall back to registry defaults.
var global atomic.Pointer[Store]

// SetGlobal installs the process-wide store the ambient Int()/Dur() readers
// consult. Called once during server construction. Passing nil clears it
// (used by tests to restore the default-reading state).
func SetGlobal(s *Store) {
	global.Store(s)
}

// Int returns the effective value for key from the process-global store, or
// the registry default when no store is installed. Safe from any goroutine.
func Int(key string) int {
	if s := global.Load(); s != nil {
		return s.Value(key)
	}
	return DefaultFor(key)
}

// Dur is Int interpreted as a whole number of seconds.
func Dur(key string) time.Duration {
	return time.Duration(Int(key)) * time.Second
}
