package database

// migrationAddMemoryVersions (v91) introduces an append-only audit
// trail for every memory write. Each call to memory.WriteFile that
// successfully persists content records a row here + a content-
// addressed blob at {memoryRoot}/versions/{sha256[:2]}/{sha256} on
// disk. The combination satisfies EU AI Act Art. 14 oversight
// requirements (enforcement Aug 2 2026) and matches the immutable-
// memory-versions pattern Anthropic Managed Agents shipped April 2026.
//
// Design choices:
//
//   - Append-only: rows are never UPDATED. A restore creates a new
//     row pointing at the historical sha as the "fresh" current
//     version, so the chain stays linear and the audit trail never
//     loses lineage.
//
//   - Content-addressed: payload_ref is the on-disk blob path under
//     {memoryRoot}/versions/{sha[:2]}/{sha}. Two writes of identical
//     content share the blob — dedupe is automatic and bounded by
//     sha collision (i.e. cryptographically negligible).
//
//   - parent_sha is the previous version's sha at the same path,
//     NULL for the first write. The (path, written_at) index keeps
//     "log this file" queries fast; parent_sha is for cleaner UI
//     rendering ("file → ancestors") without a self-join.
//
//   - Tier CHECK constrains the discriminator to the five memory
//     surfaces orchestrator + consolidator + sidecar know how to
//     produce. Adding a new tier requires a CHECK widen — caught at
//     INSERT time so a typo can't quietly write rows the UI doesn't
//     know how to render.
//
//   - written_by carries the actor identity: agent_id for sidecar-
//     initiated writes, 'consolidator' for HITL approve merges,
//     user_id (or ”) for CLI restores. Free-text TEXT column (no
//     CHECK or FK) so an unexpected new caller doesn't break the
//     INSERT path.
//
//   - bytes is redundant with the on-disk blob length but lets log/
//     show queries render size without a stat. The blob is the
//     source of truth; bytes is a denormalised hint.
//
//   - Retention: callers (a daily sweep in the consolidate runner)
//     are responsible for pruning rows older than the configured
//     window AND for sweeping orphan blobs whose sha is no longer
//     referenced. Schema does not enforce the retention timer —
//     keeps the migration trivially reversible.
//
//   - Indexes: (workspace_id, path, written_at DESC) covers the hot
//     `memory log <path>` query path; (sha256) covers the dedup
//     existence check before blob write.
const migrationAddMemoryVersions = `
CREATE TABLE IF NOT EXISTS memory_versions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,
    tier         TEXT NOT NULL
                   CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,
    parent_sha   TEXT,
    payload_ref  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_memory_versions_ws_path_ts
    ON memory_versions (workspace_id, path, written_at DESC);

CREATE INDEX IF NOT EXISTS idx_memory_versions_sha
    ON memory_versions (sha256);
`
