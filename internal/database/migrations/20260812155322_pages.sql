-- Pages — the six tables behind docs/prd/pages.md §10.
--
-- A Page is a workspace-scoped, slug-addressable record holding an ordered list
-- of panels. It holds NO query, no datasource, no connection string and no
-- credentials: it renders the last payload a producer pushed, plus the metadata
-- the server attached to that push. Everything a page could ever display is
-- reachable because the producer already runs next to that data inside a crew
-- container, with credentials the page never sees.
--
-- That property is what moves the whole security model into this file. There is
-- no query to authorise, so four columns carry the rules instead, and each of
-- them fails silently when it is wrong:
--
--   page_panels.owner_crew_id        the ACL (§7.1 rule 2), NOT NULL
--   page_grants.granted_by_user_id   only a human issues a grant (§7.1b rule 1)
--   page_public_tokens.expires_at    every public link expires (§7.3.2 rule 4)
--   page_public_tokens.created_by_user_id  only a human publishes (rule 3)
--
-- WHY THE CHECKS ARE HERE AND THE SIZE CAPS ARE NOT.
-- §10 is explicit: payload and spec size caps are enforced in Go at the handler,
-- never as a DB CHECK, because a size cap in the schema cannot produce the 422
-- rejection envelope the API owes the caller and cannot be raised without a
-- migration. What IS here is the closed vocabulary — panel schema, producer
-- kind, grant subject and level, push state — because those are enums whose
-- membership is a release decision, and because a handler that forgets one of
-- them is the way a "closed set" quietly opens.
--
-- WHY freshness IS NOT A COLUMN.
-- §4 has three states, and two of them (fresh, stale) are a function of the
-- clock, not of the data: they are computed server-side on every read from
-- produced_at. Storing them would mean a row that says "fresh" forever, which
-- is exactly the Pushgateway failure the freshness contract exists to avoid.
-- page_panel_data.state therefore records only what the PRODUCER said — 'ok' or
-- an explicit failure push — and there is deliberately no column in which a
-- producer could supply its own timestamp (§4 rule 2, rule 5).

-- ---------------------------------------------------------------------------
-- pages
-- ---------------------------------------------------------------------------
-- owner_user_id XOR owner_crew_id. §10's column list shows only owner_user_id,
-- but §7.1 rule 1 and §15 decision 3 both settle on the pair: a crew-owned page
-- is the natural home for a crew's own status board and needs no personal owner
-- at all. The CHECK is what stops a third state ("owned by both", "owned by
-- nobody") from ever existing, since every read path would have to guess which
-- owner wins.
--
-- WHY owner_user_id IS ON DELETE RESTRICT AND NOT SET NULL.
-- §7.1 rule 1b: when a user owner leaves, the page TRANSFERS to a crew — it is
-- never deleted and never orphaned. SET NULL would leave a page owned by
-- nobody (and would violate the XOR CHECK on the way there); CASCADE would
-- delete the page, which the same rule forbids in as many words. RESTRICT makes
-- the transfer a precondition of the delete, so the rule cannot be skipped by a
-- code path nobody remembered to update.
CREATE TABLE IF NOT EXISTS pages (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug                TEXT NOT NULL,
    name                TEXT NOT NULL,
    description         TEXT,
    owner_user_id       TEXT REFERENCES users(id) ON DELETE RESTRICT,
    owner_crew_id       TEXT REFERENCES crews(id) ON DELETE RESTRICT,
    -- An agent never owns permissions, it acts under one: an agent-created page
    -- records the authorising human as owner and the agent here (§7.1 rule 1).
    created_by_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    -- The validated spec. JSON-in-TEXT is the house style.
    spec_json           TEXT NOT NULL,
    created_at          TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now')),
    -- Slug-addressable from the first migration — obstacle 10: SavedView
    -- drift-detects on name because its table has no slug column, and that
    -- mistake is not being repeated here.
    UNIQUE (workspace_id, slug),
    CHECK ((owner_user_id IS NOT NULL) <> (owner_crew_id IS NOT NULL))
);

-- Every foreign-key child column in these six tables is the leading column of
-- some index, and that is a deliberate blanket rule rather than a per-column
-- judgement. SQLite has to find a child row on every parent DELETE, and an
-- unindexed child column turns that into a full scan while holding the single
-- database-wide write lock. Elsewhere in this schema the rule is applied
-- selectively, because indexing a hot child table costs write amplification on
-- the hot path — but the Pages tables are read-many, write-rarely (a page is
-- saved by a human; only page_panel_data is written often, and its two FKs are
-- covered), so the trade-off that makes selectivity right there does not arise
-- here. The invariant test in migrate_index_hot_foreign_keys_test.go counts the
-- exceptions; this migration adds none.
--
-- Both owner columns are also read directly: by the departure transfer, by the
-- crew's own page list, and by SQLite itself when it enforces the RESTRICT.
CREATE INDEX IF NOT EXISTS idx_pages_owner_user ON pages(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_pages_owner_crew ON pages(owner_crew_id);
CREATE INDEX IF NOT EXISTS idx_pages_created_by_agent ON pages(created_by_agent_id);

-- ---------------------------------------------------------------------------
-- page_panels
-- ---------------------------------------------------------------------------
-- The panel is a sensor, not a display: a producer pushes a typed payload and a
-- threshold on it wakes an agent. Four of these columns are that contract —
-- schema (what shape), owner_crew_id (who may see it), producer_kind/ref (who
-- may write it), sla_seconds (when silence becomes a fault).
--
-- WHY schema CARRIES ALL SIX NAMES TODAY.
-- The set is closed and a new panel kind is a server release (§3). embed.v1 is
-- not built in 1.0 — §3.1 places it at v1.2 — but the name is reserved here so
-- admitting it later is an INSERT, not a breaking change to the constraint.
--
-- WHY owner_crew_id IS NOT NULL AND ON DELETE RESTRICT.
-- It is the ACL, not a label (§7.1 rule 2, §2.3): the panel is filtered
-- server-side against the viewer's membership of exactly this crew. A nullable
-- column would make "no crew" mean "everyone", which is the widest possible
-- reading of an absent value; SET NULL would convert deleting a crew into
-- publishing its panels. §10b.4 wants a panel whose crew is gone to switch to
-- failed and STAY on the page, which is what RESTRICT delivers — the panel
-- outlives the removal instead of losing its anchor.
CREATE TABLE IF NOT EXISTS page_panels (
    id            TEXT PRIMARY KEY,
    page_id       TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    -- The author-chosen id from the YAML spec ("sluzby"). It is the address a
    -- producer pushes to, so it is stable across edits while `id` is not.
    panel_id      TEXT NOT NULL,
    schema        TEXT NOT NULL CHECK (schema IN (
                      'metric.v1', 'series.v1', 'status.v1',
                      'table.v1', 'narrative.v1', 'embed.v1')),
    title         TEXT,
    owner_crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE RESTRICT,
    -- Producer authority is separate from viewer authority (§7.1 rule 4): only
    -- the declared producer may write this panel's payload. 'sql' and its
    -- relatives are absent from the set on purpose — a page holds no query.
    producer_kind TEXT NOT NULL CHECK (producer_kind IN ('routine', 'script', 'agent', 'webhook')),
    producer_ref  TEXT NOT NULL,
    -- §4 rule 1: every panel declares an SLA and there is no default that means
    -- "never mind". Zero would be exactly that default wearing a number.
    sla_seconds   INTEGER NOT NULL CHECK (sla_seconds > 0),
    -- Grid span; the renderer maps it to col-span-n on a 12-column grid (§9).
    span          INTEGER NOT NULL DEFAULT 12 CHECK (span BETWEEN 1 AND 12),
    config_json   TEXT NOT NULL DEFAULT '{}',
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (page_id, panel_id)
);

-- The per-viewer filter runs on every page read, for every panel: "is this
-- viewer in this crew". UNIQUE(page_id, panel_id) already covers lookups by
-- page, so that index is not repeated here.
CREATE INDEX IF NOT EXISTS idx_page_panels_owner_crew ON page_panels(owner_crew_id);

-- ---------------------------------------------------------------------------
-- page_panel_data
-- ---------------------------------------------------------------------------
-- The bounded payload ring (§5, §10b.3): newest 200 payloads per panel, hard
-- age cut at 7 days, whichever comes first. Enough for a sparkline and for
-- "what did this look like before it broke"; deliberately not enough to be a
-- time-series database. A panel pushed every 5 s would otherwise produce
-- ~120 000 rows a week, per panel.
--
-- The eviction itself lives in Go (internal/pages) rather than in a trigger:
-- it is two rules with an ordering between them, it has to be tested against a
-- clock, and a trigger firing on every push would pay for the age scan on the
-- hot write path.
CREATE TABLE IF NOT EXISTS page_panel_data (
    panel_id        TEXT NOT NULL REFERENCES page_panels(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    payload_json    TEXT NOT NULL,
    -- Server clock, always. Freshness is computed from this column and never
    -- from anything the producer sent (§4 rule 2).
    produced_at     TEXT NOT NULL,
    -- Provenance, server-attached (§4 rule 5). NULL for producers that are not
    -- routine runs — a script in a crew container, or an inbound webhook.
    producer_run_id TEXT REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    -- What the PRODUCER said. 'fresh' and 'stale' are not storable values here:
    -- they are a function of produced_at and the clock, and a stored 'fresh'
    -- would still read fresh a year later.
    state           TEXT NOT NULL CHECK (state IN ('ok', 'failed')),
    PRIMARY KEY (panel_id, seq)
);

-- The age cut sweeps every panel's ring by timestamp; without this it is a full
-- scan of the largest table Pages owns.
CREATE INDEX IF NOT EXISTS idx_page_panel_data_produced_at ON page_panel_data(produced_at);
-- run_retention_days deletes runs in bulk. Each deleted run has to find its
-- referencing payloads to NULL them, and an unindexed child column turns that
-- into one full scan per run while holding the single write lock.
CREATE INDEX IF NOT EXISTS idx_page_panel_data_run ON page_panel_data(producer_run_id);

-- ---------------------------------------------------------------------------
-- page_versions
-- ---------------------------------------------------------------------------
-- Every save is a version (§10b.1), following the pipeline_versions precedent.
-- Several agents may rewrite one page and the one who breaks it is rarely the
-- one who notices, so `crewship page rollback --to <seq>` is not a nicety.
-- Retain the last 50; the retention itself is Go's job, for the same reason the
-- ring's is.
--
-- Panel DATA is not versioned. A rollback restores structure and never numbers
-- (§10b.1): resurrecting an old payload and showing it as current is precisely
-- the lie §4 exists to prevent, and a rollback is when someone is most likely
-- to believe it.
--
-- Authorship is nullable on both arcs: the SET NULLs fire on a hard delete of
-- the author, and a version whose author was erased is still a version worth
-- keeping. Both are indexed under the blanket rule above — agents ARE hard
-- deleted (the compensating deletes in agents_hire.go), and this table is
-- written once per page save.
CREATE TABLE IF NOT EXISTS page_versions (
    page_id         TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    spec_json       TEXT NOT NULL,
    author_user_id  TEXT REFERENCES users(id) ON DELETE SET NULL,
    author_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (page_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_page_versions_author_user ON page_versions(author_user_id);
CREATE INDEX IF NOT EXISTS idx_page_versions_author_agent ON page_versions(author_agent_id);

-- ---------------------------------------------------------------------------
-- page_grants
-- ---------------------------------------------------------------------------
-- The first per-object ACL in this codebase (§7.2), and deliberately scoped to
-- Pages alone until a second consumer exists. The shape is Directus's, not
-- Retool's (§2.3): visibility is a property of the ROW, resolved by the
-- ordinary membership check, never an expression a page author writes into a
-- component. Permission logic that lives in one table stays auditable.
--
-- subject_id carries no foreign key. SQLite has no polymorphic references, and
-- three typed columns with three FKs would need a CHECK to keep the
-- discriminator honest for a table whose rows are written by exactly one
-- handler. The same trade-off is documented at credential_bindings, which went
-- the other way because ITS cascade had to be automatic.
--
-- WHY granted_by_user_id IS NOT NULL, AND WHY IT CASCADES.
-- §7.1b rule 1: only a human issues a grant. An agent with `write` may rebuild
-- a page freely but can never widen who reaches it — that is what closes the
-- escalation path where an injected agent grows its own blast radius one grant
-- at a time. NOT NULL is that rule in the schema rather than in a comment. The
-- CASCADE is the second half of the same rule: an agent's authority is a subset
-- of the authorising human's, evaluated at USE time, so a grant whose issuer no
-- longer exists is authority delegated by nobody.
CREATE TABLE IF NOT EXISTS page_grants (
    page_id            TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    subject_type       TEXT NOT NULL CHECK (subject_type IN ('user', 'crew', 'agent')),
    subject_id         TEXT NOT NULL,
    -- read: may see the page. produce: may push payloads into named panels.
    -- write: may edit the page spec. Layout and data are separate authorities
    -- (§7.1b rule 2) — `write` is authority over arrangement, never content.
    level              TEXT NOT NULL CHECK (level IN ('read', 'produce', 'write')),
    -- JSON array of panel ids; NULL = every panel. Only meaningful for produce,
    -- and the CHECK makes that literal: a panel scope stored against a read
    -- grant reads to a human reviewer as a scope while the code ignores it.
    panel_ids          TEXT,
    granted_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    granted_at         TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (page_id, subject_type, subject_id, level),
    CHECK (panel_ids IS NULL OR level = 'produce')
);

-- "Which pages may I see" is the hot question and it is asked by subject, not
-- by page; the primary key only answers the other direction.
CREATE INDEX IF NOT EXISTS idx_page_grants_subject ON page_grants(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_page_grants_granted_by ON page_grants(granted_by_user_id);

-- ---------------------------------------------------------------------------
-- page_public_tokens
-- ---------------------------------------------------------------------------
-- The highest-risk surface in the feature (§7.3): a page served to somebody
-- with no account, from a separate URL space (/p/{token}) that shares no
-- session, no cookie and no workspace context with the app.
--
-- token_hash, not token. The shape is copied from pipeline_webhooks, which has
-- hashed tokens at rest since #1888: holding the token is the authorisation, so
-- a readable column is a credential store. Several tokens per page is
-- deliberate — revoking the accountant's link must not break the client's.
--
-- expires_at NOT NULL is rule 4 in the schema: a public link that never expires
-- is the one that is still live when nobody remembers it exists. The default
-- (30 days) and maximum (1 year) belong to the handler, which can explain a
-- refusal; the column only guarantees that some expiry was chosen.
--
-- show_provenance defaults to 0 because run ids, agent slugs, crew slugs and
-- producer names are internal vocabulary (rule 5). The default is the value
-- that leaks nothing, so forgetting to set it cannot be the disclosure.
CREATE TABLE IF NOT EXISTS page_public_tokens (
    id                 TEXT PRIMARY KEY,
    page_id            TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    token_hash         TEXT NOT NULL,
    -- Optional per token, hashed with the same primitives the auth layer uses.
    -- Never reversible, never in the URL (§7.3.3).
    password_hash      TEXT,
    expires_at         TEXT NOT NULL,
    show_provenance    INTEGER NOT NULL DEFAULT 0 CHECK (show_provenance IN (0, 1)),
    created_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    revoked_at         TEXT,
    -- Written at most once a day per token (§7.3.2 rule 6) so the owner can see
    -- the link is being used, without turning every public view into a write.
    last_seen_at       TEXT,
    created_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

-- The hash is the lookup key for every public request, and it must be unique
-- across the instance: two pages sharing one hash is a cross-page read.
CREATE UNIQUE INDEX IF NOT EXISTS idx_page_public_tokens_hash ON page_public_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_page_public_tokens_page ON page_public_tokens(page_id);
CREATE INDEX IF NOT EXISTS idx_page_public_tokens_created_by ON page_public_tokens(created_by_user_id);
