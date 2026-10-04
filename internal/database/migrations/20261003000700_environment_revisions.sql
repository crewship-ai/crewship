-- Build provenance survives mutable crew configuration and in-memory jobs.
-- This is metadata history, not image retention or a backup of the artifact.
CREATE TABLE environment_revisions (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 definition_hash TEXT NOT NULL,
 build_hash TEXT NOT NULL,
 image_id TEXT NOT NULL,
 toolchain_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(workspace_id, crew_id, definition_hash, image_id)
);
CREATE INDEX environment_revisions_crew ON environment_revisions(workspace_id,crew_id,created_at DESC,id DESC);
CREATE INDEX environment_revisions_crew_fk ON environment_revisions(crew_id);
CREATE TRIGGER environment_revision_immutable BEFORE UPDATE ON environment_revisions
BEGIN SELECT RAISE(ABORT,'environment revision is immutable'); END;
