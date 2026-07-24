package database

// migrationRunVerdictFlag (v164) seeds the "run_verdict_summaries"
// feature flag row. The feature_flags/feature_flag_overrides tables
// have existed since v1 (two-tier instance-default + per-workspace
// override), but as of this migration NO flag had ever been seeded —
// this is the first consumer. Default enabled=1 (on instance-wide):
// the post-run outcome verdict (#1403) is a single cheap Haiku call
// gated on this flag; workspaces that don't want it can flip it off
// per-workspace via the existing feature-flags UI/API, no new surface
// needed for the opt-out path. INSERT OR IGNORE keeps this idempotent
// against restore replays, matching the convention used elsewhere in
// this migration set.
const migrationRunVerdictFlag = `
INSERT OR IGNORE INTO feature_flags (id, key, description, enabled, percentage)
VALUES ('ffl_run_verdict_summaries', 'run_verdict_summaries', 'Generate an LLM outcome verdict after each run (#1403)', 1, 100);
`
