package database

// migrationRoutineState (v155) introduces pipeline_routine_state — durable
// cross-run key/value state for a routine, scoped per schedule (#1420).
//
// Before this, a run had only run.metadata (per-run scratch, gone the moment
// the run ends) and step outputs (visible only within the same run). There was
// no way to carry a value FORWARD to the next run — the watermark pattern
// ("last processed id / timestamp") that every incremental/polling routine
// needs. pipeline_routine_state closes that gap:
//
//   - keyed by (pipeline_id, schedule_id, key). schedule_id is the occurrence
//     owner so two schedules of the same routine keep INDEPENDENT cursors;
//     non-schedule runs (manual/webhook) share the empty-string bucket per
//     pipeline. A run READS the whole (pipeline, schedule) bucket at start as
//     the {{ routine.state.* }} namespace and WRITES back via a step's
//     `state_write` binding.
//   - value is a plain string (the rendered template result); the row is
//     upserted on write and survives process restart, so run N+1 reads what
//     run N wrote regardless of any restart in between.
//   - ON DELETE CASCADE from pipelines so a deleted routine drops its state.
const migrationRoutineState = `
CREATE TABLE IF NOT EXISTS pipeline_routine_state (
    pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    schedule_id  TEXT NOT NULL DEFAULT '',
    key          TEXT NOT NULL,
    value        TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (pipeline_id, schedule_id, key)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_routine_state_bucket
    ON pipeline_routine_state (pipeline_id, schedule_id);
`
