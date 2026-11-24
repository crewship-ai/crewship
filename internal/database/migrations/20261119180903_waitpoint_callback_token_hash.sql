-- Separate a waitpoint's identity from its callback capability (#1910).
--
-- pipeline_waitpoints.token was the primary key, inbox_items.source_id, a
-- WAITING run's waitpoint_token and the authed approve path segment — AND
-- the bearer credential for the unauthenticated
-- `POST /api/v1/waitpoint-tokens/{token}` callback. As the identity it was on
-- every member surface and in the database in the clear, so reading either
-- was enough to approve any pending gate.
--
-- From here on `token` is a PUBLIC id ("wp_" + 32 hex). The callback
-- capability is a separate secret, minted on demand by a MANAGER, shown once,
-- and stored only as this digest: "sh1:" + SHA-256, the scheme
-- 20260810171000_hash_capability_tokens uses for webhooks and port exposures
-- (unkeyed, for the reasons recorded there).
--
-- WHY THE CONVERSION IS NOT IN THIS FILE. The old token is copied into
-- inbox_items.id ("ibx_<ws>_waitpoint_<token>"), inbox_item_reads,
-- notification_deliveries.dedup_key and audit_logs, and every copy has to
-- move to the new id in one transaction per bounded batch — easier to state and test in
-- Go than as a chain of SQL string surgery. It runs when the waitpoint store
-- is constructed (NewSQLWaitpointStore, before anything serves) and on every
-- sweep. Old secrets are NOT hashed forward: any member could read them before
-- this change, so a waitpoint pending at upgrade time needs a newly minted
-- callback URL. See internal/pipeline/waitpoints_callback_token.go.
--
-- The column keeps the name `token` on purpose: every internal lookup keys on
-- it, and none of them needed a secret.

-- Nullable with no default: a waitpoint has no callback capability until one
-- is minted, and NULL is the predicate "nothing can complete this without
-- authentication".
ALTER TABLE pipeline_waitpoints ADD COLUMN callback_token_hash TEXT;

-- UNIQUE, so one presented secret can only ever resolve one waitpoint, and
-- partial, so the many NULLs do not collide. This is the public callback's
-- lookup path.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_waitpoints_callback_token_hash
    ON pipeline_waitpoints (callback_token_hash) WHERE callback_token_hash IS NOT NULL;

-- The conversion joins deliveries only by workspace and the old source id.
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_waitpoint_source
    ON notification_deliveries (workspace_id, source_id) WHERE source_kind = 'waitpoint';
