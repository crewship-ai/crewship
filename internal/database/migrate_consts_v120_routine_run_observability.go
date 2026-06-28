package database

// migrationRoutineRunObservability (v120) adds the observability surface
// borrowed from trigger.dev's runs model. All additive — existing rows
// default cleanly.
//
//   - pipeline_runs.metadata_json: a typed scratchpad threaded through a
//     run. Set at invoke, readable from steps as {{ run.metadata.X }},
//     surfaced in the run detail + inbox. Defaults to '{}'.
//   - pipeline_runs.is_replay: 1 when this run was created by replaying a
//     prior run. Injected into the render context as {{ run.is_replay }}
//     so a step can short-circuit side effects on replay.
//   - pipeline_runs.replay_of: the run_id this run replays (provenance),
//     NULL for original runs.
//
// run_tags is a join table (one row per run/tag) so the runs list can
// filter/group by tag with an index, mirroring trigger.dev's tags. Tags
// are workspace-scoped strings attached at invoke; max enforced in the
// handler, not the schema.
//
// error_fingerprint already exists + is indexed (v83) — this migration
// does not touch it; the bulk-replay grouping reads that column.
const migrationRoutineRunObservability = `
ALTER TABLE pipeline_runs ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE pipeline_runs ADD COLUMN is_replay INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pipeline_runs ADD COLUMN replay_of TEXT;

CREATE TABLE IF NOT EXISTS run_tags (
    run_id       TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    tag          TEXT NOT NULL,
    PRIMARY KEY (run_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_run_tags_ws_tag ON run_tags (workspace_id, tag);
`
