-- Existing comments remain private. Sharing a comment is always explicit.
ALTER TABLE mission_comments ADD COLUMN guest_visible INTEGER NOT NULL DEFAULT 0 CHECK (guest_visible IN (0,1));
ALTER TABLE mission_comments ADD COLUMN guest_project_id TEXT REFERENCES projects(id) ON DELETE SET NULL;
CREATE INDEX mission_comments_guest_project ON mission_comments(guest_project_id);
-- Inert historical audiences must survive backup/restore after a project move.
-- Public writers stamp the current project atomically; reads require equality.
CREATE TRIGGER guest_comment_insert BEFORE INSERT ON mission_comments
WHEN NEW.guest_visible=1 AND NEW.guest_project_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM missions m JOIN projects p ON p.workspace_id=m.workspace_id WHERE m.id=NEW.mission_id AND m.mission_type='issue' AND p.id=NEW.guest_project_id)
BEGIN SELECT RAISE(ABORT,'shared comment project mismatch'); END;
CREATE TRIGGER guest_comment_update BEFORE UPDATE OF guest_visible,guest_project_id ON mission_comments
WHEN NEW.guest_visible=1 AND NEW.guest_project_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM missions m JOIN projects p ON p.workspace_id=m.workspace_id WHERE m.id=NEW.mission_id AND m.mission_type='issue' AND p.id=NEW.guest_project_id)
BEGIN SELECT RAISE(ABORT,'shared comment project mismatch'); END;

-- A project grant never lets a Guest choose an arbitrary execution crew.
-- Administrators configure where unassigned BACKLOG requests are filed.
CREATE TABLE project_guest_issue_intake (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX project_guest_issue_intake_crew ON project_guest_issue_intake(crew_id);
CREATE INDEX project_guest_issue_intake_workspace ON project_guest_issue_intake(workspace_id);
CREATE INDEX project_guest_issue_intake_actor ON project_guest_issue_intake(updated_by);
CREATE TRIGGER guest_issue_intake_insert BEFORE INSERT ON project_guest_issue_intake
WHEN NOT EXISTS (SELECT 1 FROM projects p JOIN crews c ON c.workspace_id=p.workspace_id
 WHERE p.id=NEW.project_id AND p.workspace_id=NEW.workspace_id AND c.id=NEW.crew_id)
BEGIN SELECT RAISE(ABORT,'guest issue intake workspace mismatch'); END;
CREATE TRIGGER guest_issue_intake_project_scope AFTER UPDATE OF workspace_id ON projects
WHEN OLD.workspace_id!=NEW.workspace_id
BEGIN DELETE FROM project_guest_issue_intake WHERE project_id=OLD.id; END;
CREATE TRIGGER guest_issue_intake_crew_scope AFTER UPDATE OF workspace_id ON crews
WHEN OLD.workspace_id!=NEW.workspace_id
BEGIN DELETE FROM project_guest_issue_intake WHERE crew_id=OLD.id; END;
CREATE TRIGGER guest_issue_intake_update BEFORE UPDATE OF project_id,workspace_id,crew_id ON project_guest_issue_intake
WHEN NOT EXISTS (SELECT 1 FROM projects p JOIN crews c ON c.workspace_id=p.workspace_id
 WHERE p.id=NEW.project_id AND p.workspace_id=NEW.workspace_id AND c.id=NEW.crew_id)
BEGIN SELECT RAISE(ABORT,'guest issue intake workspace mismatch'); END;
