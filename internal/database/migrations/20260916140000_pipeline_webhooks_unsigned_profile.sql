-- Profile "unsigned": bearer-token-only intake for senders who cannot
-- sign (Coolify SendWebhookJob posts plain JSON with no HMAC header).
-- The 20260915111000 migration added ingress_profile with a CHECK that
-- hard-codes ('crewship','github'). SQLite cannot ALTER a CHECK, so this
-- migration rebuilds pipeline_webhooks with the same columns/order and
-- a widened CHECK. Everything is copied verbatim (token_hash digest,
-- consecutive_fire_failures streak, receipts columns untouched); the
-- unique indexes of the previous migrations are recreated.
--
-- The fail-closed contract: 'unsigned' rows keep an EMPTY signing
-- secret and authenticate via the 256-bit random token in the dispatch
-- URL alone; 'crewship' and 'github' rows keep the HMAC verification
-- they have had. Default remains 'crewship'.
CREATE TABLE pipeline_webhooks_new (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name                     TEXT NOT NULL,
    target_pipeline_id       TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    target_pipeline_version  INTEGER,
    token                    TEXT NOT NULL UNIQUE,
    signing_secret           TEXT,
    inputs_template          TEXT NOT NULL DEFAULT '{}',
    enabled                  INTEGER NOT NULL DEFAULT 1,
    rate_limit_per_min       INTEGER NOT NULL DEFAULT 0,
    last_fired_at            TEXT,
    last_status              TEXT,
    last_run_id              TEXT,
    fire_count               INTEGER NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at               TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at               TEXT,
    token_hash               TEXT,
    consecutive_fire_failures INTEGER NOT NULL DEFAULT 0,
    ingress_profile          TEXT NOT NULL DEFAULT 'crewship' CHECK (ingress_profile IN ('crewship','github','unsigned'))
);

INSERT INTO pipeline_webhooks_new SELECT id, workspace_id, name, target_pipeline_id, target_pipeline_version,
       token, signing_secret, inputs_template, enabled, rate_limit_per_min,
       last_fired_at, last_status, last_run_id, fire_count, created_at, updated_at, deleted_at,
       token_hash, consecutive_fire_failures, ingress_profile FROM pipeline_webhooks;

DROP TABLE pipeline_webhooks;
ALTER TABLE pipeline_webhooks_new RENAME TO pipeline_webhooks;

CREATE INDEX IF NOT EXISTS idx_pipeline_webhooks_workspace
    ON pipeline_webhooks (workspace_id, enabled)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pipeline_webhooks_pipeline
    ON pipeline_webhooks (target_pipeline_id)
    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_webhooks_token_hash
    ON pipeline_webhooks (token_hash) WHERE token_hash IS NOT NULL;
