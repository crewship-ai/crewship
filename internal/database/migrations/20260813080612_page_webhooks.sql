-- page_webhooks — inbound panel webhooks (docs/prd/pages.md §10b.5c).
--
-- "A panel should be writable by anything, not only by something that can
-- execute the `crewship` binary — a cron on someone else's box, a Zapier step,
-- a PLC gateway, a GitHub Action."
--
-- One row is ONE TOKEN BOUND TO ONE PANEL. That binding is the security model
-- and it is why panel_id is the only address in this table: the inbound request
-- carries a token and a body, and nothing else. There is no field on the wire
-- through which a holder could name a different panel, a different page or a
-- different workspace, so a leaked token can write one panel and nothing else.
--
-- token_hash, not token. The shape is copied from pipeline_webhooks, which has
-- hashed tokens at rest since #1888 (internal/pipeline/webhooks.go:23-46):
-- holding the token IS the authorisation, so a readable column is a credential
-- store — a leaked backup or any read primitive would hand the reader live,
-- working URLs. There is no cleartext column here AT ALL, which is the one
-- improvement on that table's shape: pipeline_webhooks kept `token` NOT NULL
-- UNIQUE and had to overwrite it with a redaction marker during the #1888
-- backfill. A table that never had the column needs no backfill and can never
-- regress into one.
--
-- created_by_user_id NOT NULL is §10b.5c's "issued only by a human" in the
-- schema, exactly as page_public_tokens.created_by_user_id is for publishing
-- and page_grants.granted_by_user_id is for a grant (§7.1b rule 1). The ON
-- DELETE CASCADE is the same rule's second half: a token whose issuer no longer
-- exists is a capability nobody is accountable for, and it dies with them.
--
-- WHY THE ISSUER IS STORED AT ALL, when the handler could have written down a
-- copy of what the issuer was allowed to do. §10b.5c: "The webhook is a
-- `produce` grant in a different coat, and it obeys every rule that grant
-- does." §7.1b's rule for a grant is that it is evaluated against the
-- authorising human's own rights AT USE TIME, never at issue time — "if that
-- human loses access to a crew, every agent grant they issued narrows with
-- them". A stored copy of the authority would be exactly the thing that
-- outlives its issuer. So the row stores WHO issued it, and the fire path
-- re-derives what they may do now, through the same reader every other page
-- write goes through (internal/api/pages_webhooks.go).
--
-- WHY panel_id REFERENCES page_panels(id) AND NOT (page_id, panel_id).
-- The author-chosen panel id ("sluzby") is stable across edits, but a panel
-- DROPPED from the spec is deleted from page_panels (reconcilePanels), and its
-- payload ring goes with it. A token that survived that deletion would silently
-- resurrect the moment somebody re-added a panel with the same name — which is
-- a capability coming back from the dead without a human issuing it. Binding to
-- the row id makes the token die with the panel, the same bargain
-- page_panel_data already takes.
--
-- WHY THERE IS NO page_id COLUMN. It is derivable through page_panels in one
-- indexed hop, and a stored copy is a second answer to "which page is this
-- token on" that can disagree with the first. The listing endpoint joins.
--
-- WHY THERE IS NO rate_limit_per_min COLUMN, when pipeline_webhooks has one.
-- §10b.5c says "rate limited per panel", and the panel's rate is already
-- decided — internal/pages/limits.go, tuned in Settings through
-- internal/ratelimitcfg, plus the floor enforced inside the push transaction. A
-- per-token limit would be a second, quieter number that could be set ABOVE the
-- panel's, i.e. a way to buy your way past §10b.3 by minting a token. The
-- webhook is not a way around the limits, so it does not carry its own.
CREATE TABLE IF NOT EXISTS page_webhooks (
    id                 TEXT PRIMARY KEY,
    -- The one panel this token may write, and the whole of its blast radius.
    panel_id           TEXT NOT NULL REFERENCES page_panels(id) ON DELETE CASCADE,
    -- SHA-256 of the token, through internal/pipeline.HashCapabilityToken, so
    -- there is one digest scheme in this codebase and not two.
    token_hash         TEXT NOT NULL,
    -- A human label ("Zapier: daily close", "PLC gateway hall 2"). Optional,
    -- and the reason `page webhook list` is usable at all: several tokens on
    -- one panel is the intended shape (revoking the PLC's must not break the
    -- GitHub Action's), which is only manageable if they can be told apart.
    name               TEXT,
    created_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at         TEXT NOT NULL DEFAULT (datetime('now')),
    -- Revocation is a mark, not a delete: "was it used after we pulled it" is
    -- the question an incident asks, and a deleted row cannot answer it. The
    -- fire path treats a non-NULL value as "no such token" (404), so revocation
    -- is immediate and does not leak that the token ever existed.
    revoked_at         TEXT,
    -- Written on every accepted fire. Unlike page_public_tokens.last_seen_at
    -- (once a day, because a public view must not turn a read into a write),
    -- this path is ALREADY writing a row — one more UPDATE on the same panel is
    -- not a new cost, and "when did this cron last succeed" is the first
    -- question an operator asks about an integration that went quiet.
    last_fired_at      TEXT,
    fire_count         INTEGER NOT NULL DEFAULT 0
);

-- The digest is the lookup key for every inbound request and must be unique
-- across the instance: two panels sharing one digest is a cross-panel write.
CREATE UNIQUE INDEX IF NOT EXISTS idx_page_webhooks_hash ON page_webhooks(token_hash);
-- Both foreign keys lead their own index, following the blanket rule the Pages
-- migration set out: SQLite scans the child table on every parent DELETE while
-- holding the database-wide write lock, and these tables are read-many,
-- write-rarely.
CREATE INDEX IF NOT EXISTS idx_page_webhooks_panel ON page_webhooks(panel_id);
CREATE INDEX IF NOT EXISTS idx_page_webhooks_created_by ON page_webhooks(created_by_user_id);
