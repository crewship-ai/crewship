package database

// migrationPersonaRename (v102) lands the PR-Z Z.3 follow-up that was
// deferred so PR-E (PRD §6 F6) could touch the same call sites in one
// pass. Two pieces:
//
//  1. Rename agents.system_prompt → agents.system_prompt_legacy.
//     The legacy column is retained for one minor release so the
//     migrator can drain old values into PERSONA.md on first write
//     (see internal/memory/persona.go BackfillFromLegacy). After
//     that window the column will be dropped in a separate
//     migration. Renaming (not dropping) preserves audit history
//     for crews that haven't been visited since the rollout.
//
//  2. Widen the memory_versions.tier CHECK constraint to accept the
//     new 'persona' and 'peer' tiers. SQLite forbids in-place CHECK
//     mutation, so we do the canonical recreate dance: build a new
//     table with the widened CHECK, copy rows, drop the old table,
//     rename the new one back, recreate indexes.
//
// Numbering note: this PR was authored as v100 but bumped to v102 to
// leave room for PR-C (v100, keeper Phase 2) and PR-D (v101, ephemeral
// lifecycle) which are landing in parallel. If PR-C/D slip, drop this
// to v100 on rebase before merge.
const migrationPersonaRename = `
-- (1) system_prompt column rename. SQLite 3.25+ supports ALTER TABLE
-- RENAME COLUMN natively (modernc.org/sqlite tracks recent releases).
ALTER TABLE agents RENAME COLUMN system_prompt TO system_prompt_legacy;

-- (2) memory_versions.tier CHECK widen — recreate dance.
CREATE TABLE memory_versions_new (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL
                   CHECK (tier IN ('agent','crew','workspace','pins','learned','persona','peer')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);

INSERT INTO memory_versions_new (id, workspace_id, path, tier, sha256, bytes, written_at, written_by, parent_sha, payload_ref)
SELECT id, workspace_id, path, tier, sha256, bytes, written_at, written_by, parent_sha, payload_ref
FROM memory_versions;

DROP TABLE memory_versions;
ALTER TABLE memory_versions_new RENAME TO memory_versions;

CREATE INDEX IF NOT EXISTS idx_memory_versions_ws_path_ts
    ON memory_versions (workspace_id, path, written_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_versions_sha
    ON memory_versions (sha256);
`
