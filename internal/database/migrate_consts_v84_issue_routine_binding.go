package database

// migrationAddIssueRoutineBinding (v84) gives every issue an optional
// pointer at a saved routine, plus a JSON blob for the inputs that
// routine should be invoked with.
//
// Why this lives on missions: the issues product reuses the missions
// table — issues are missions with an `identifier`. So any field that
// describes "how this issue gets handled" naturally goes here rather
// than in a side table.
//
// Two columns:
//   - routine_id: the pipeline (== routine) the issue is bound to.
//     Stored as the pipeline_id (not slug) so it survives renames.
//     Nullable — most issues won't have a routine bound.
//   - routine_inputs_json: TEXT '{}' default. Captures the form values
//     filled in at issue-create time so "Run routine" can reproduce
//     the same invocation without prompting again. Mirrors the shape
//     used by pipeline_runs.inputs_json.
//
// We intentionally do NOT add a routine_run_id pointer here. An issue
// can run its routine multiple times (re-run after fix, dry-run vs.
// real run) and pipeline_runs already has triggered_by_id we can
// populate with the issue identifier later if needed. Keeping the
// binding one-way (issue -> routine) avoids a second backfill if we
// later switch to a many-runs-per-issue model.
//
// No FK constraint to pipelines(id): SQLite ALTER TABLE ADD COLUMN
// can't add a REFERENCES clause cleanly without rebuild gymnastics,
// and the integrity guarantee is enforced at the API layer (handlers
// resolve the routine_id and 400 if it's stale). When a pipeline is
// deleted we clear the binding via a same-transaction UPDATE.
const migrationAddIssueRoutineBinding = `
ALTER TABLE missions ADD COLUMN routine_id TEXT;
ALTER TABLE missions ADD COLUMN routine_inputs_json TEXT NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_mission_routine
    ON missions (routine_id)
    WHERE routine_id IS NOT NULL;
`
