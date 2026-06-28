package database

// migrationPipelineTags (v123) tags the routine DEFINITION (not just
// runs) for cross-crew discovery — the "pipelines are workspace-scoped
// shared assets" thesis. A workspace can browse routines by tag
// (`routine list --tag billing`) independent of any run.
//
// Separate from run_tags (v120): those label individual runs; these
// label the reusable routine. Keyed (pipeline_id, tag), workspace_id for
// scope + cascade. Additive.
const migrationPipelineTags = `
CREATE TABLE IF NOT EXISTS pipeline_tags (
    pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    tag          TEXT NOT NULL,
    PRIMARY KEY (pipeline_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_pipeline_tags_ws_tag ON pipeline_tags (workspace_id, tag);
`
