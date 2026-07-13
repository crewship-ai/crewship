package database

// migrationKeeperWatchSpec (v139) adds the admin-authored watch spec to the
// per-workspace Keeper governance row (issue #1001, M1). It extends the M0
// governance table rather than adding a parallel one — the resolver still
// returns a single Settings struct.
//
// watch_spec is free-form natural-language rules the OWNER/ADMIN authors
// ("flag any read of ~/.ssh or id_rsa"); watch_presets is a JSON array of
// stable preset keys (governance.WatchPresets). Both default to empty — an
// unconfigured or watch-spec-less workspace falls back to the evaluator's
// built-in anti-pattern list, so this is purely additive and preserves the
// opt-in, default-OFF contract.
//
// SQLite runs both ALTER TABLE … ADD COLUMN statements from one migration
// string in a single ExecContext (see Migrate: tx.ExecContext(ctx, m.sql)).
const migrationKeeperWatchSpec = `
ALTER TABLE keeper_governance_settings ADD COLUMN watch_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE keeper_governance_settings ADD COLUMN watch_presets TEXT NOT NULL DEFAULT '';
`
