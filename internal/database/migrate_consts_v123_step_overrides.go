package database

// migrationStepOverrides (v123) adds a per-step prompt/model override
// layer (trigger.dev "prompts-as-code with dashboard override" parity).
//
// An operator can tweak a single step's prompt or model WITHOUT bumping
// the routine version: the override is applied at run start over the
// versioned DSL. This advances the AI-authored-pipelines thesis — the
// durable, versioned routine stays the source of truth, while a thin
// override layer lets a human nudge one step's prompt/tier live.
//
// Keyed by (pipeline_id, step_id); workspace_id carried for scoping +
// cascade. NULL prompt/model means "don't override that field". All
// additive.
const migrationStepOverrides = `
CREATE TABLE IF NOT EXISTS routine_step_overrides (
    pipeline_id    TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    workspace_id   TEXT NOT NULL,
    step_id        TEXT NOT NULL,
    prompt         TEXT,
    model_override TEXT,
    updated_at     TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    PRIMARY KEY (pipeline_id, step_id)
);
CREATE INDEX IF NOT EXISTS idx_step_overrides_ws ON routine_step_overrides (workspace_id);
`
