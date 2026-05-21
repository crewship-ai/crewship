package database

// migrationPeerConsent (v103) lands the GDPR primitives for PRD §6 F6
// per-user peer cards. Three additions:
//
//  1. user_peer_consent — per-(user, workspace) opt-out flag. A row
//     with opted_out=1 signals two things to the runtime:
//     (a) the PeerCardSync routine MUST skip extraction for this
//     user in this workspace, AND
//     (b) any existing peer cards mentioning this user MUST be
//     purged on the next routine sweep.
//     The (user_id, workspace_id) composite PK is the natural shape:
//     consent is workspace-scoped (a user can consent in workspace A
//     and opt out in workspace B). Per PRD §6 F6, opt-out is a
//     hard-stop primitive — the API rejects new cards immediately
//     rather than waiting for the next routine.
//
//  2. peer_cards — index table mirroring the on-disk peer card files
//     at /output/{agent}/.memory/peers/{user_slug}.md. The slug is
//     sha256(user_id || workspace_id)[:16] so it never carries PII
//     into the filename. Mirror exists so:
//     - GET /api/v1/users/me/peer-cards can query "all cards
//     anywhere about me" without walking every agent's
//     filesystem, and
//     - DELETE /api/v1/users/me/peer-cards has an atomic index to
//     iterate before the disk sweep.
//     Stored fields kept minimal (path, slug, agent, user, sizes)
//     because content is on disk under the existing flock/version
//     protocol; the table is metadata only.
//
//  3. peer_card_audit — append-only log keyed by user_id for the
//     GDPR view / delete endpoints. Distinct from audit_logs because
//     it intentionally records target_user_id (data subject) rather
//     than user_id (actor) as the primary axis, so a SAR (subject
//     access request) "show me everything you logged about user X"
//     query is a single index hit instead of a JSON probe across
//     audit_logs.metadata.
const migrationPeerConsent = `
CREATE TABLE IF NOT EXISTS user_peer_consent (
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id   TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    opted_out      INTEGER NOT NULL DEFAULT 0 CHECK (opted_out IN (0,1)),
    opted_out_at   TEXT,
    updated_at     TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    PRIMARY KEY (user_id, workspace_id)
);

CREATE INDEX IF NOT EXISTS idx_peer_consent_ws_optout
    ON user_peer_consent (workspace_id, opted_out);

CREATE TABLE IF NOT EXISTS peer_cards (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_slug     TEXT NOT NULL,
    path          TEXT NOT NULL,
    bytes         INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    UNIQUE (agent_id, user_slug)
);

CREATE INDEX IF NOT EXISTS idx_peer_cards_user_ws
    ON peer_cards (user_id, workspace_id);
CREATE INDEX IF NOT EXISTS idx_peer_cards_agent
    ON peer_cards (agent_id);

CREATE TABLE IF NOT EXISTS peer_card_audit (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    actor_user_id   TEXT REFERENCES users(id) ON DELETE SET NULL,
    actor_kind      TEXT NOT NULL CHECK (actor_kind IN ('user','agent','system')),
    action          TEXT NOT NULL CHECK (action IN ('write','read','delete','opt_out','opt_in')),
    target_user_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id        TEXT REFERENCES agents(id) ON DELETE SET NULL,
    metadata        TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE INDEX IF NOT EXISTS idx_peer_audit_target_ws
    ON peer_card_audit (target_user_id, workspace_id, created_at);
CREATE INDEX IF NOT EXISTS idx_peer_audit_ws_time
    ON peer_card_audit (workspace_id, created_at);
`
