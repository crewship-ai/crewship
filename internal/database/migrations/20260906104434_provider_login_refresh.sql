-- Provider logins (docs/prd/provider-logins.md §5.3, §10.4): the refresh
-- state of a PROVIDER_LOGIN credential whose access token the server renews
-- from the sealed refresh token it keeps.
--
-- A table of its own rather than six more columns on credentials: only a
-- subscription login of a provider with a refresh flow (OpenAI today, Google
-- next) ever has a row here, and every reader of the credentials row is spared
-- a branch for state that does not apply to it. One row per credential; the
-- row is the single-flight lock too (in_progress_until), so two run starts in
-- the same second cannot both rotate a token that only rotates once.
CREATE TABLE IF NOT EXISTS provider_login_refresh (
    credential_id     TEXT    PRIMARY KEY REFERENCES credentials(id) ON DELETE CASCADE,
    -- ok | pending | failed | needs_relogin — the values the API's
    -- login.refresh.status carries verbatim.
    status            TEXT    NOT NULL DEFAULT 'ok',
    last_at           TEXT,
    next_at           TEXT,
    error             TEXT,
    failures          INTEGER NOT NULL DEFAULT 0,
    in_progress_until TEXT,
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK (status IN ('ok', 'pending', 'failed', 'needs_relogin'))
);
