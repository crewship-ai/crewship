-- Existing memberships are the explicit trusted compatibility profile.
-- Restricted membership must never inherit role-wide resource access.
ALTER TABLE workspace_members ADD COLUMN access_mode TEXT NOT NULL DEFAULT 'trusted'
    CHECK (access_mode IN ('trusted', 'restricted'));
ALTER TABLE workspace_members ADD COLUMN access_revision INTEGER NOT NULL DEFAULT 1
    CHECK (access_revision > 0);

CREATE TABLE access_grants (
    id TEXT PRIMARY KEY,
    member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
    resource_kind TEXT NOT NULL CHECK (resource_kind IN ('agent','project')),
    agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    created_by TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    CHECK ((resource_kind='agent' AND agent_id IS NOT NULL AND project_id IS NULL)
        OR (resource_kind='project' AND project_id IS NOT NULL AND agent_id IS NULL)),
    UNIQUE (member_id, agent_id, operation),
    UNIQUE (member_id, project_id, operation)
);

-- Membership identity, not user/workspace alone, fences removal + rejoin.
-- Revisions fence queued work even when a revoked grant is granted again.
CREATE TRIGGER access_grant_insert AFTER INSERT ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=NEW.member_id;
END;
CREATE TRIGGER access_grant_delete AFTER DELETE ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=OLD.member_id;
END;
CREATE TRIGGER access_grant_update AFTER UPDATE ON access_grants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id IN (OLD.member_id,NEW.member_id);
END;
CREATE TRIGGER access_member_update AFTER UPDATE OF role, capabilities, access_mode ON workspace_members
WHEN OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1 WHERE id=NEW.id;
END;

CREATE TABLE access_attempts (
    id TEXT PRIMARY KEY,
    handle_hash TEXT NOT NULL UNIQUE,
    member_id TEXT NOT NULL REFERENCES workspace_members(id) ON DELETE CASCADE,
    member_revision INTEGER NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    principal_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    parent_id TEXT REFERENCES access_attempts(id),
    generation INTEGER NOT NULL CHECK(generation > 0),
    rights TEXT NOT NULL,
    created_at TEXT NOT NULL,
    revoked_at TEXT,
    UNIQUE(chat_id,generation)
);
CREATE INDEX access_attempts_member ON access_attempts(member_id);
CREATE INDEX access_attempts_parent ON access_attempts(parent_id);

-- A resource recreated under an old ID must not inherit old grants.
CREATE TRIGGER access_agent_delete BEFORE DELETE ON agents BEGIN
    DELETE FROM access_grants WHERE resource_kind='agent' AND agent_id=OLD.id;
END;
CREATE TRIGGER access_agent_scope AFTER UPDATE OF deleted_at,workspace_id ON agents
WHEN NEW.deleted_at IS NOT NULL OR OLD.workspace_id IS NOT NEW.workspace_id BEGIN
    DELETE FROM access_grants WHERE resource_kind='agent' AND agent_id=OLD.id;
END;
CREATE TRIGGER access_project_delete BEFORE DELETE ON projects BEGIN
    DELETE FROM access_grants WHERE resource_kind='project' AND project_id=OLD.id;
END;
CREATE TRIGGER access_project_scope AFTER UPDATE OF workspace_id ON projects
WHEN OLD.workspace_id IS NOT NEW.workspace_id BEGIN
    DELETE FROM access_grants WHERE resource_kind='project' AND project_id=OLD.id;
END;
CREATE TRIGGER access_chat_participant_delete AFTER DELETE ON chat_participants BEGIN
    UPDATE workspace_members SET access_revision=access_revision+1
    WHERE user_id=OLD.user_id AND workspace_id=(SELECT workspace_id FROM chats WHERE id=OLD.chat_id);
    UPDATE access_attempts SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
    WHERE chat_id=OLD.chat_id AND principal_id=OLD.user_id AND revoked_at IS NULL;
END;
CREATE TRIGGER access_chat_audience AFTER UPDATE OF visibility,created_by,agent_id,workspace_id ON chats
WHEN OLD.visibility IS NOT NEW.visibility OR OLD.created_by IS NOT NEW.created_by
    OR OLD.agent_id IS NOT NEW.agent_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
    UPDATE access_attempts SET revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
    WHERE chat_id=NEW.id AND revoked_at IS NULL;
END;
