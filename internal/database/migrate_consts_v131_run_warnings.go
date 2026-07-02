package database

// migrationRunWarnings (v131) adds a structured, non-fatal warnings
// surface to pipeline_runs.
//
// v131, not v129: at authoring time two other open PRs (#760
// chat_unread, #774 issue_creator_attribution) both claimed v129; the
// runner hard-fails on a version/name mismatch against an
// already-migrated install, so this migration skipped ahead to 131 and
// left 129/130 for them (now landed as v129 issue_creator_attribution
// and v130 chat_unread).
//
// Lifecycle hooks (after_all / on_failure) run best-effort: a failing
// teardown hook (e.g. a credential-release or cost-meter-close step)
// must never flip a COMPLETED run to FAILED, so its error was
// previously only logged via slog.Warn — invisible to the run record,
// the API, the UI, and the CLI. An operator had no way to discover a
// teardown leak short of grepping server logs.
//
// warnings_json holds a JSON array of {stage, message, at} objects,
// appended to (never overwritten) by RunStore.AppendWarning. Additive
// and defaulted so existing rows read back as an empty list.
const migrationRunWarnings = `
ALTER TABLE pipeline_runs ADD COLUMN warnings_json TEXT NOT NULL DEFAULT '[]';
`
