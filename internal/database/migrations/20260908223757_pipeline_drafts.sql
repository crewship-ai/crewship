ALTER TABLE pipelines ADD COLUMN publication_revision INTEGER NOT NULL DEFAULT 1;

-- Count authoring changes, including legacy saves and rollback, but not run counters.
CREATE TRIGGER pipelines_publication_revision
AFTER UPDATE OF definition_json, head_version, name, description, author_crew_id, author_agent_id, status, deleted_at ON pipelines
WHEN NEW.definition_json IS NOT OLD.definition_json OR NEW.head_version IS NOT OLD.head_version
 OR NEW.name IS NOT OLD.name OR NEW.description IS NOT OLD.description
 OR NEW.author_crew_id IS NOT OLD.author_crew_id OR NEW.author_agent_id IS NOT OLD.author_agent_id
 OR NEW.status IS NOT OLD.status OR NEW.deleted_at IS NOT OLD.deleted_at
BEGIN
 UPDATE pipelines SET publication_revision = publication_revision + 1 WHERE id = NEW.id;
END;

CREATE TABLE pipeline_drafts (
 id TEXT PRIMARY KEY,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 slug TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK (revision > 0),
 base_pipeline_id TEXT NOT NULL DEFAULT '',
 base_revision INTEGER NOT NULL DEFAULT 0,
 document_json TEXT NOT NULL CHECK (json_valid(document_json)),
 updated_by TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(workspace_id, slug)
);
