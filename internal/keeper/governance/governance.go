// Package governance resolves the per-workspace Keeper watchdog settings
// (issue #1001, M0): the in-app OWNER/ADMIN toggle, the named security
// contact the watchdog snitches to, and the risk threshold at which a DENY
// decision also lands in the inbox.
//
// Resolution contract: an explicit workspace row always wins; no row means
// the watchdog is OFF for that workspace — it is opt-in and default OFF, only
// running once an OWNER/ADMIN enables it. The resolver is read on hot paths
// (the behavior hook fires per sampled tool call), so Resolve never returns an
// error — a failed read falls back to disabled (fail-safe: monitoring off,
// never a spurious escalation) and the caller's next sample retries naturally.
package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// DefaultDenyNotifyMinRisk is the risk score (1–10) at or above which a DENY
// decision is snitched to the inbox when a workspace has no explicit setting.
const DefaultDenyNotifyMinRisk = 7

// MaxAutoLeaseSeconds caps the auto-issued credential lease at 30 days,
// mirroring the manual `credential assign --ttl` cap in internal/api. A lease is
// a session-scoped construct; a multi-month "lease" is a standing grant in
// disguise and defeats the ephemerality guarantee the setting exists to provide.
const MaxAutoLeaseSeconds = 30 * 24 * 60 * 60

// MinAutoLeaseSeconds is the floor for a configured auto-lease. Below a minute
// the lease lapses inside a single gatekeeper round-trip (the evaluator is an
// LLM call) and every ALLOW would be immediately followed by a refusal at the
// injection point — a self-inflicted outage dressed up as a security control.
const MinAutoLeaseSeconds = 60

// The behaviour monitor's sampling cadence (#1001 M3): the watchdog reviews
// every Nth tool call per crew rather than all of them, because each review is a
// governance-model call.
//
// The bounds are what keeps a *rate* from becoming a disguised off switch at
// either end:
//
//   - MinBehaviorSampleEvery is 1, not 2. "Review every tool call" is a real
//     posture and the hook already implements it; what must never be settable
//     here is 0, because behaviorhook.SetSampleEvery(<=0) makes the hook a
//     no-op — a workspace would read "watchdog enabled" while nothing was ever
//     evaluated. Off is the enabled toggle (`crewship keeper disable`). A tight
//     cadence is expensive rather than broken, so it is allowed with an advisory
//     (WarnBehaviorSampleEveryBelow) instead of a rejection.
//   - MaxBehaviorSampleEvery is 100. The per-crew counter lives in memory and
//     starts at zero each boot, so a cadence larger than the number of tool
//     calls a crew makes in a run means the monitor never fires at all — the
//     same silent-off failure the floor rules out, arrived at from the other
//     side. At 100 a long run still gets sampled.
const (
	// DefaultBehaviorSampleEvery is the cadence a workspace that has never set
	// one runs on. Must match behaviorhook.DefaultSampleEvery — pinned by a test
	// in cmd/crewship, since this package cannot import the hook.
	DefaultBehaviorSampleEvery = 5
	MinBehaviorSampleEvery     = 1
	MaxBehaviorSampleEvery     = 100
	// WarnBehaviorSampleEveryBelow is the cadence at or under which the API
	// returns a non-blocking cost advisory: below every 3rd call, a majority of
	// the workspace's tool calls each carry a judge round-trip.
	WarnBehaviorSampleEveryBelow = 3
)

// EffectiveBehaviorSampleEvery maps the stored value to the cadence actually in
// force: 0 is the "never configured" sentinel, and resolves to the built-in
// default rather than to "off". The single place that mapping is written.
func EffectiveBehaviorSampleEvery(stored int) int {
	if stored <= 0 {
		return DefaultBehaviorSampleEvery
	}
	return stored
}

// Settings is the per-workspace watchdog configuration.
type Settings struct {
	// Enabled gates the behavioral watchdog layer (behavior monitoring,
	// DENY-notify). It does NOT gate the credential-access gatekeeper
	// enforcement path, which stays server-configured (KEEPER_ENABLED) —
	// a workspace toggle must not be able to weaken credential isolation.
	Enabled bool `json:"enabled"`
	// SecurityContactUserID targets snitch inbox items at a named admin.
	// Empty = legacy TargetRole MANAGER fanout.
	SecurityContactUserID string `json:"security_contact_user_id"`
	// DenyNotifyMinRisk is the risk score (1–10) at or above which a DENY
	// decision also lands in the inbox. ESCALATE always does.
	DenyNotifyMinRisk int `json:"deny_notify_min_risk"`
	// WatchSpec is the OWNER/ADMIN-authored free-form natural-language watch
	// rules (issue #1001, M1). Empty = fall back to the evaluator's built-in
	// anti-pattern list. Injected into the Keeper evaluator prompts via
	// CompileWatchSpec.
	WatchSpec string `json:"watch_spec"`
	// WatchPresets is the set of enabled preset keys (see WatchPresets catalog).
	// Stored as a JSON array in watch_presets; nil/empty = no presets.
	WatchPresets []string `json:"watch_presets"`
	// RequireSecondApprover is the credential-escalation "four-eyes" toggle
	// (issue #1084). When true, the user recorded as the initiating agent's
	// owner (agents.created_by_user_id) cannot resolve a CREDENTIAL
	// escalation that agent raised — approver must differ from initiator.
	// Enforced in ResolveEscalation (internal/api/escalation_handler.go), not
	// here: this package only resolves the setting. OWNER is NOT exempt.
	// Default false — existing single-approver workflows are unaffected
	// until an OWNER/ADMIN opts in.
	RequireSecondApprover bool `json:"require_second_approver"`

	// GovModelProvider selects the Keeper governance model's provider (M2a,
	// #1001): "" = use the server/env default (backward-compatible), else
	// "ollama" | "anthropic" | "openai_compat". Resolved via ResolveGovModel.
	GovModelProvider string `json:"gov_model_provider"`
	// GovModelID is the wire model identifier passed to the chosen provider.
	GovModelID string `json:"gov_model_id"`
	// AutoLeaseSeconds is the credential-lease auto-issuance TTL (#1373).
	// 0 (the default) means auto-issuance is OFF and a grant stays standing
	// exactly as before. A positive value makes a Keeper ALLOW — and the
	// approval of an agent-proposed CREDENTIAL escalation — re-issue the
	// requesting agent's grant as a short-lived LEASE of that many seconds,
	// so credential access decays instead of persisting. Which tiers this
	// applies to is decided by keeper.TierPolicy.SelfServiceDelivery — today
	// L3/L4 are leased and L1/L2 are not — rather than by a threshold restated
	// here; see that field for why. Clamped to MaxAutoLeaseSeconds on write.
	// Enforced in internal/api (issueCredentialLease + the delivery-path
	// gates), not here — this package only resolves the setting.
	AutoLeaseSeconds int `json:"auto_lease_seconds"`

	// BehaviorSampleEvery is how often the behaviour monitor reviews a tool call
	// (#1001 M3): the evaluator fires on every Nth call per crew. 0 is the
	// "never configured" sentinel and means the built-in default — see
	// EffectiveBehaviorSampleEvery, which is the only place that mapping lives.
	// A written value is bounded by [MinBehaviorSampleEvery,
	// MaxBehaviorSampleEvery]; both ends stop the rate becoming a silent off
	// switch (see the const block). Consumed by the post-tool-call observer,
	// which reads this row per observation and hands the cadence to the hook —
	// so an edit applies to the next tool call, not the next restart (#1556).
	BehaviorSampleEvery int `json:"behavior_sample_every"`

	// GovModelCredentialID optionally points the provider at a vault credential
	// (ENDPOINT_URL / API_KEY) for its endpoint/key. Empty = no credential.
	// Revoke-safety (§4.4): a revoke is a soft delete (credentials.deleted_at),
	// so the FK's ON DELETE SET NULL does NOT fire — the id stays set but
	// CredentialLookup reports the credential unavailable at resolve time and
	// ResolveGovModel degrades to the default OLLAMA judge + a WARN. (ON DELETE
	// SET NULL only nulls this on a hard-delete purge of the credential row.)
	GovModelCredentialID string `json:"gov_model_credential_id"`
}

// Get returns the explicit workspace row. found is false when the workspace
// has never been configured in-app (the watchdog is then off — see Resolve).
func Get(ctx context.Context, db *sql.DB, workspaceID string) (Settings, bool, error) {
	var (
		s            Settings
		enabled      int
		contact      sql.NullString
		presets      string
		secondApprov int
		govCredID    sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT enabled, security_contact_user_id, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver,
		       gov_model_provider, gov_model_id, gov_model_credential_id, auto_lease_seconds, behavior_sample_every
		FROM keeper_governance_settings WHERE workspace_id = ?`, workspaceID).
		Scan(&enabled, &contact, &s.DenyNotifyMinRisk, &s.WatchSpec, &presets, &secondApprov,
			&s.GovModelProvider, &s.GovModelID, &govCredID, &s.AutoLeaseSeconds, &s.BehaviorSampleEvery)
	if err == sql.ErrNoRows {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, nil
	}
	if err != nil {
		return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, fmt.Errorf("governance: get: %w", err)
	}
	s.Enabled = enabled != 0
	s.SecurityContactUserID = contact.String
	s.RequireSecondApprover = secondApprov != 0
	s.GovModelCredentialID = govCredID.String
	if presets != "" {
		if err := json.Unmarshal([]byte(presets), &s.WatchPresets); err != nil {
			return Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}, false, fmt.Errorf("governance: get: decode watch_presets: %w", err)
		}
	}
	return s, true, nil
}

// Upsert writes the workspace row. updatedBy is the acting user (may be
// empty for system writes). DenyNotifyMinRisk outside [1,10] is clamped.
func Upsert(ctx context.Context, db *sql.DB, workspaceID string, s Settings, updatedBy string) error {
	if s.DenyNotifyMinRisk < 1 {
		s.DenyNotifyMinRisk = 1
	}
	if s.DenyNotifyMinRisk > 10 {
		s.DenyNotifyMinRisk = 10
	}
	// Clamp the auto-lease TTL rather than rejecting it: Upsert is a partial-
	// update sink shared by the CLI, the admin API and the settings UI, and a
	// nonsensical value there must degrade to a safe one, never to an error the
	// caller has to special-case. Negative → 0 (off). A positive value below the
	// floor is raised to it so an operator typing `--ttl 5s` gets a usable lease
	// instead of one that lapses before the gatekeeper answers.
	if s.AutoLeaseSeconds < 0 {
		s.AutoLeaseSeconds = 0
	}
	if s.AutoLeaseSeconds > 0 && s.AutoLeaseSeconds < MinAutoLeaseSeconds {
		s.AutoLeaseSeconds = MinAutoLeaseSeconds
	}
	if s.AutoLeaseSeconds > MaxAutoLeaseSeconds {
		s.AutoLeaseSeconds = MaxAutoLeaseSeconds
	}
	// Sampling cadence: clamp for the same reason the lease TTL is clamped —
	// Upsert is a shared sink and a nonsensical value from a non-HTTP writer must
	// degrade to a usable one. Negative → 0, which is the unset sentinel (the
	// built-in default), NOT "off": a stored value that silenced the monitor
	// while the workspace still read "watchdog enabled" is the one outcome this
	// setting must never produce. The API rejects rather than clamps, so an
	// operator still hears about it.
	if s.BehaviorSampleEvery < 0 {
		s.BehaviorSampleEvery = 0
	}
	if s.BehaviorSampleEvery > MaxBehaviorSampleEvery {
		s.BehaviorSampleEvery = MaxBehaviorSampleEvery
	}
	// Marshal presets to a JSON array; empty → "" for a stable default that
	// round-trips back to a nil slice in Get.
	presets := ""
	if len(s.WatchPresets) > 0 {
		b, err := json.Marshal(s.WatchPresets)
		if err != nil {
			return fmt.Errorf("governance: upsert: encode watch_presets: %w", err)
		}
		presets = string(b)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.ExecContext(ctx, `
		INSERT INTO keeper_governance_settings
			(workspace_id, enabled, security_contact_user_id, deny_notify_min_risk, watch_spec, watch_presets, require_second_approver,
			 gov_model_provider, gov_model_id, gov_model_credential_id, auto_lease_seconds, behavior_sample_every, updated_by, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			enabled = excluded.enabled,
			security_contact_user_id = excluded.security_contact_user_id,
			deny_notify_min_risk = excluded.deny_notify_min_risk,
			watch_spec = excluded.watch_spec,
			watch_presets = excluded.watch_presets,
			require_second_approver = excluded.require_second_approver,
			gov_model_provider = excluded.gov_model_provider,
			gov_model_id = excluded.gov_model_id,
			gov_model_credential_id = excluded.gov_model_credential_id,
			auto_lease_seconds = excluded.auto_lease_seconds,
			behavior_sample_every = excluded.behavior_sample_every,
			updated_by = excluded.updated_by,
			updated_at = excluded.updated_at`,
		workspaceID, boolToInt(s.Enabled), s.SecurityContactUserID, s.DenyNotifyMinRisk,
		s.WatchSpec, presets, boolToInt(s.RequireSecondApprover),
		s.GovModelProvider, s.GovModelID, s.GovModelCredentialID, s.AutoLeaseSeconds, s.BehaviorSampleEvery,
		updatedBy, now, now)
	if err != nil {
		return fmt.Errorf("governance: upsert: %w", err)
	}
	return nil
}

// Resolve returns the watchdog settings a caller should act on: the explicit
// workspace row when present, otherwise the opt-in default (disabled, default
// DENY-notify threshold). The watchdog is default-OFF per workspace (#1001) —
// a workspace only participates once an OWNER/ADMIN explicitly enables it, so
// an unconfigured workspace resolves to Enabled=false regardless of the server
// config. This is the single fetch-and-warn seam every read site shares
// (behavior hook, credential DENY-notify, F4 endpoints, sweeps); it never
// errors — a failed read behaves as unconfigured (fail-safe: monitoring off,
// never a spurious escalation). logger may be nil.
func Resolve(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string) Settings {
	def := Settings{DenyNotifyMinRisk: DefaultDenyNotifyMinRisk}
	if db == nil || workspaceID == "" {
		return def
	}
	s, found, err := Get(ctx, db, workspaceID)
	if err != nil {
		if logger != nil {
			logger.Warn("keeper governance: resolve failed", "error", err, "workspace_id", workspaceID)
		}
		return def
	}
	if !found {
		return def
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
