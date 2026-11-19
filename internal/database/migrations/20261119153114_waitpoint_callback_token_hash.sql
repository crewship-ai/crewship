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
-- WHY THE BACKFILL IS NOT IN THIS FILE. SQLite has no SHA-256. The Go half
-- runs when the waitpoint store is constructed (NewSQLWaitpointStore, before
-- anything serves) and is retried by its sweeper: for every row whose token is
-- not a "wp_" id it stores the digest of the existing token IN PLACE (an
-- external system already holding a callback URL keeps working), replaces
-- the token with a fresh public id, and rewrites the copies of the old value
-- in inbox_items, notification_deliveries and audit_logs — one transaction
-- per row. See internal/pipeline/waitpoints_callback_token.go.
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
