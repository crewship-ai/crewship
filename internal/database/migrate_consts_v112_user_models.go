package database

// migrationUserModels (v112) lands the index table for the evolving
// per-user operator model (PR #10 F6).
//
// Version note: the conversation-search branch takes max+1 (v111). To
// avoid the silent-skip hazard documented in CLAUDE.md (the runner
// keys on version number, not name, so two branches reusing a version
// land non-deterministically), this migration claims max+2 = v112.
//
// user_models — index table mirroring the on-disk model files at
// /crew/shared/.memory/users/{user_slug}.md. The slug is
// sha256(user_id || workspace_id)[:16] so the filename never carries
// PII. Unlike peer_cards, the model is keyed on (user, workspace)
// ALONE — there is no agent_id, because the model captures how an
// operator likes to work across the whole workspace, not how they
// relate to one agent. The mirror exists so:
//
//   - "show me everything stored about me" can query a single index
//     instead of walking every crew's shared filesystem, and
//   - the opt-out / GDPR delete path has an atomic index to iterate
//     before the disk sweep.
//
// Stored fields are metadata only (path, slug, sizes, timestamps);
// content lives on disk under the existing flock protocol. The
// UNIQUE(workspace_id, user_slug) constraint plus ON CONFLICT DO
// UPDATE means a refresh upserts in place rather than creating rows.
//
// The crew_id column records which crew's shared memory holds the file
// (a workspace may host several crews); it is informational for the
// sweep's disk-path resolution and is NOT part of the uniqueness key.
const migrationUserModels = `
CREATE TABLE IF NOT EXISTS user_models (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id       TEXT REFERENCES crews(id) ON DELETE SET NULL,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_slug     TEXT NOT NULL,
    path          TEXT NOT NULL,
    bytes         INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    UNIQUE (workspace_id, user_slug)
);

CREATE INDEX IF NOT EXISTS idx_user_models_user_ws
    ON user_models (user_id, workspace_id);
CREATE INDEX IF NOT EXISTS idx_user_models_ws
    ON user_models (workspace_id);
`
