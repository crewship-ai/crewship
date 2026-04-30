package database

// SQL constant for migration v60: subscription-aware paymaster.
//
// Rationale (recorded in CLAUDE.md commit log + plan file z-rove-ty-…):
// the platform's billing model is heterogeneous — one workspace can mix
// metered API keys (pay-per-token) with flat-rate subscription credentials
// (Anthropic Max 20×, Cursor Pro/Ultra, ChatGPT Plus+Codex, Google AI
// Pro/Ultra, Copilot Pro+, Factory Droid). We can only meaningfully bill
// per-token for the metered class. Subscription calls are either invisible
// (HTTPS CONNECT tunnel = TLS end-to-end) or marginally-zero ($0/token
// because the user already paid the flat fee).
//
// Conflating both into a single $-tracked stream produced wrong rollups
// and over-strict budget enforcement. This migration introduces:
//
//   - billing_mode: 'metered' (default, $-tracked) | 'flat_rate' (subscription)
//   - quota_remaining_pct / quota_window: live rate-limit-header signal,
//     populated for metered API-key calls (sidecar parses anthropic-ratelimit-*
//     and x-ratelimit-* headers from upstream responses)
//   - subscription_plan: human label for flat-rate rows ("Anthropic Max 20×")
//   - rate_*_per_m: rate-card snapshot at write time (Langfuse pattern). Lets
//     pricing.go change without retroactively rewriting historical $ figures.
//   - cost_confidence: 'precise' (provider returned usage) | 'estimate' (we
//     approximated from request body length) | 'unknown' (flat-rate, no $).
//     UI shows a badge per row — no number is ever rendered without provenance.
//
// All columns are nullable / defaulted so existing rows remain valid; the
// migration is purely additive. SQLite's ALTER TABLE ADD COLUMN does not
// support adding NOT NULL without DEFAULT, so the few NOT NULL columns
// carry their default inline.
const migrationAddPaymasterBillingModes = `
ALTER TABLE cost_ledger ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'metered'
    CHECK(billing_mode IN ('metered','flat_rate'));
ALTER TABLE cost_ledger ADD COLUMN quota_remaining_pct REAL;
ALTER TABLE cost_ledger ADD COLUMN quota_window TEXT;
ALTER TABLE cost_ledger ADD COLUMN subscription_plan TEXT;
ALTER TABLE cost_ledger ADD COLUMN rate_input_per_m REAL;
ALTER TABLE cost_ledger ADD COLUMN rate_output_per_m REAL;
ALTER TABLE cost_ledger ADD COLUMN rate_cached_in_per_m REAL;
ALTER TABLE cost_ledger ADD COLUMN rate_cache_write_per_m REAL;
ALTER TABLE cost_ledger ADD COLUMN cost_confidence TEXT NOT NULL DEFAULT 'estimate'
    CHECK(cost_confidence IN ('precise','estimate','unknown'));

-- Partial index so the Subscription-plans UI panel can fetch flat-rate rows
-- without scanning the full ledger. Most rows will be 'metered'; the index
-- is small.
CREATE INDEX IF NOT EXISTS idx_cost_billing_mode
    ON cost_ledger(workspace_id, billing_mode, ts DESC)
    WHERE billing_mode = 'flat_rate';
`
