-- Actor snapshots preserve agent provenance without pretending an agent is a user.
ALTER TABLE page_project_revisions ADD COLUMN actor_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE page_project_builds ADD COLUMN actor_json TEXT NOT NULL DEFAULT '{}';
