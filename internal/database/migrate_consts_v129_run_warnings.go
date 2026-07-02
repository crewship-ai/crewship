package database

// migrationRunWarnings (v129) adds a structured, non-fatal warnings
// surface to pipeline_runs.
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
