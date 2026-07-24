package database

// migrationRunRetentionDays (v158) adds the per-workspace override for the
// pipeline_runs retention sweep (internal/pipeline/retention.go). NULL (the
// default for every existing row) means "use pipeline.DefaultRunRetentionDays
// (90)"; a workspace can set its own window, mirroring the existing
// workspaces.memory_config.versions_retention_days pattern for memory, but
// as a plain typed column since this feature needs exactly one integer
// setting rather than a JSON bag.
//
// See issue #1407.
const migrationRunRetentionDays = `
ALTER TABLE workspaces ADD COLUMN run_retention_days INTEGER;
`
