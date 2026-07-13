package database

// migrationKeeperGovModel (v142) adds the per-workspace Keeper *governance
// model* selection to the M0 governance row (issue #1001, M2a). It extends the
// existing keeper_governance_settings table rather than adding a parallel one —
// the resolver still returns a single Settings struct (decision #4).
//
//   - gov_model_provider: "" (default) | "ollama" | "anthropic" | "openai_compat".
//     Empty means "use the server/env default" — backward-compatible with today's
//     env-wired access gatekeeper and aux slots, so this migration is purely
//     additive and preserves the opt-in contract.
//   - gov_model_id: the wire model identifier passed to the provider.
//   - gov_model_credential_id: an optional vault credential (ENDPOINT_URL / API_KEY)
//     the resolved provider sources its endpoint/key from. ON DELETE SET NULL is
//     the DB half of the revoke-safety contract (§4.4): deleting the credential
//     nulls the ref rather than leaving a dangling id, and the resolver then
//     degrades to the default OLLAMA judge + a WARN — never a broken evaluator.
//
// SQLite runs all three ALTER TABLE … ADD COLUMN statements from one migration
// string in a single ExecContext (see Migrate: tx.ExecContext(ctx, m.sql)).
const migrationKeeperGovModel = `
ALTER TABLE keeper_governance_settings ADD COLUMN gov_model_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_governance_settings ADD COLUMN gov_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_governance_settings ADD COLUMN gov_model_credential_id TEXT REFERENCES credentials(id) ON DELETE SET NULL;
`
