-- Hash the capability tokens that are stored in the clear, IN PLACE.
--
-- port_exposures.token and pipeline_webhooks.token are not credentials that
-- accompany an authenticated request — they ARE the request's authorization.
-- `/exposed/{token}/…` reverse-proxies straight into a crew container and
-- `POST /api/v1/webhooks/{token}` fires a pipeline, and neither endpoint has
-- any authentication in front of it by design: knowing the token is the whole
-- of the check. Kept as cleartext, that made anyone who could read the
-- database file — a leaked backup, a copied `.db`, any read primitive — the
-- holder of every live exposure URL and every configured webhook on the
-- instance. cli_tokens has stored a digest instead of the secret since Patch
-- J; this brings these two into line.
--
-- IN PLACE is the point. Rotating the tokens instead would have been a much
-- smaller change and would have broken every published exposure URL and every
-- sender already configured against a webhook, on every instance, with no
-- evidence that any cleartext ever leaked. So the existing tokens keep
-- working: their digest is computed from the cleartext already in the row, and
-- the cleartext is then overwritten.
--
-- WHY THE BACKFILL IS NOT IN THIS FILE. SQLite has no SHA-256, so the digest
-- cannot be computed in SQL. This migration adds the column and the
-- index; the hashing itself runs in Go the first time the owning component is
-- constructed, which on the server path is before anything serves a request:
--
--   * port_exposures     — PortExposeRegistry.LoadFromDB (internal/api)
--   * pipeline_webhooks  — NewWebhookStore              (internal/pipeline)
--
-- Both are idempotent (`WHERE token_hash IS NULL`) and normally read zero
-- rows. Splitting it that way is what let this stay a plain .sql file instead
-- of a Go entry in the legacyMigrations slice.
--
-- THE DIGEST IS UNKEYED SHA-256, hex, behind an `sh1:` scheme prefix. Not an
-- HMAC: a key protects a digest whose input space is small enough to enumerate
-- offline, and these two tokens are 32 bytes of crypto/rand each
-- (internal/pipeline.generateWebhookToken, internal/api.generateExposeToken),
-- so a preimage search is 2^256 wide either way. The key bought no security
-- here and cost a lifecycle problem that could brick the instance: an earlier
-- revision of this migration keyed the HMAC off ENCRYPTION_KEY, and the
-- documented master-key rotation (CREWSHIP_ENCRYPTION_KEY_VERSION +
-- ENCRYPTION_KEY_V2 + POST /admin/reencrypt) RETIRES that variable as its final
-- step — after which every presented token hashes under a different scheme than
-- every stored digest, with the cleartext already overwritten and nothing to
-- recover. The scheme prefix stays, so a stored digest is recognisable as one
-- (replaying it at the public endpoint resolves to nothing) and a future
-- algorithm change can add a prefix rather than rewrite one.
--
-- `hk1:` (the keyed scheme) IS NOT SUPPORTED, deliberately. It existed only on
-- the branch that developed this migration and was never released, so no
-- deployed instance can hold one. A DEV instance that already ran the earlier
-- revision has `hk1:` digests that nothing will ever match: re-mint those
-- webhooks and exposures (`crewship routine webhooks create`, and re-request
-- the exposure), or delete the rows.
--
-- WHAT HAPPENS TO THE CLEARTEXT COLUMN. It stays, holding `redacted:<row id>`.
-- Neither column can be dropped: both are `NOT NULL UNIQUE`, so SQLite's
-- ALTER TABLE DROP COLUMN refuses them (the constraint carries an implicit
-- index), and port_exposures.token is still named in the INSERT that creates
-- an exposure. `SET token = ''` is not available either, for the same UNIQUE
-- reason — one row could be blanked, the second would violate the constraint.
-- Deriving the marker from the primary key keeps it unique, obviously dead,
-- and traceable to the row it belonged to.
--
-- pipeline_waitpoints.token is NOT hashed here, though it is the same kind of
-- secret. It cannot be, without changes outside this table: it is the primary
-- key, it is the handle inbox_items.source_id and RunResult.WaitpointToken
-- carry, and `GET …/pipelines/waitpoints` re-derives the public callback URL
-- by reading the column back out (internal/api/pipelines_exec.go). It is a
-- retrievable shared secret by contract, not a show-once credential, so
-- hashing it means first redesigning that contract. Tracked separately.

-- Nullable with no default: SQLite cannot ADD COLUMN a NOT NULL without a
-- constant default, and there is no constant digest. NULL is also exactly the
-- predicate the Go backfill selects on.
ALTER TABLE port_exposures ADD COLUMN token_hash TEXT;

-- UNIQUE, so two rows can never digest to the same value, and partial so the
-- not-yet-backfilled NULLs do not collide with each other. This index replaces
-- the implicit one on `token` as the lookup path: the proxy resolves an
-- exposure by digest on every request through /exposed/.
CREATE UNIQUE INDEX IF NOT EXISTS idx_port_exposures_token_hash
    ON port_exposures (token_hash) WHERE token_hash IS NOT NULL;

ALTER TABLE pipeline_webhooks ADD COLUMN token_hash TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_webhooks_token_hash
    ON pipeline_webhooks (token_hash) WHERE token_hash IS NOT NULL;
