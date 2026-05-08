package database

// migrationAddPipelineWebhooks (v82) introduces pipeline_webhooks —
// the event-driven trigger surface alongside cron schedules.
//
// A webhook binds an inbound HTTP path (the `token` segment is the
// stable identifier) to a saved pipeline. POST /api/v1/webhooks/{token}
// fires the pipeline, passing the request body as the `event` input.
//
// Why a dedicated table (vs. extending pipeline_schedules):
// schedules carry cron + timezone + next_run_at; webhooks carry
// secret/signing + last-fired health. Modeling them on one table
// would make every webhook row carry NULL cron columns and every
// schedule row carry NULL signing columns — a join the UI would
// just have to disambiguate anyway.
//
// Schema notes:
//   - id uses a "pwh_" prefix so log greps disambiguate by entity.
//   - token is the public path segment (stable, opaque, generated).
//     Treated like an API key in URL form: knowing the token is
//     sufficient to fire the pipeline (with optional HMAC layered on
//     top via signing_secret).
//   - signing_secret is the optional HMAC-SHA256 key. When set, the
//     handler validates the X-Crewship-Signature header against a
//     hex digest of the request body. Most webhook senders (Stripe,
//     GitHub, Linear) sign requests this way; we follow the same
//     shape so existing senders work unchanged.
//   - inputs_template lets the webhook reshape the request body into
//     the pipeline's input schema. Empty = pass body through as
//     `event` input (the simplest case).
//   - rate_limit_per_min caps fire frequency from this token; 0 =
//     unlimited. Set to e.g. 60 so a misconfigured Stripe webhook
//     storm can't drain the workspace's run budget.
//   - target_pipeline_version optional pin (NULL = latest head).
//     Same rationale as pipeline_schedules.target_pipeline_version.
const migrationAddPipelineWebhooks = `
CREATE TABLE IF NOT EXISTS pipeline_webhooks (
    id                       TEXT PRIMARY KEY,                            -- "pwh_" + CUID
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,                               -- human-readable
    target_pipeline_id       TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    target_pipeline_version  INTEGER,                                     -- NULL = latest head
    token                    TEXT NOT NULL UNIQUE,                        -- public path segment
    signing_secret           TEXT,                                        -- optional HMAC-SHA256 key (encrypted)
    inputs_template          TEXT NOT NULL DEFAULT '{}',                  -- JSON template; empty = pass body as event
    enabled                  INTEGER NOT NULL DEFAULT 1,
    rate_limit_per_min       INTEGER NOT NULL DEFAULT 0,                  -- 0 = unlimited
    last_fired_at            TEXT,
    last_status              TEXT,                                        -- COMPLETED | FAILED | NULL
    last_run_id              TEXT,
    fire_count               INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT
);
CREATE INDEX IF NOT EXISTS idx_pipeline_webhooks_workspace
    ON pipeline_webhooks (workspace_id, enabled)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pipeline_webhooks_pipeline
    ON pipeline_webhooks (target_pipeline_id)
    WHERE deleted_at IS NULL;
`
