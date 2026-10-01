-- Classified immutable output versions. No shared crew filesystem is imported.
CREATE TABLE access_files (
 id TEXT PRIMARY KEY,
 attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 principal_id TEXT NOT NULL REFERENCES users(id),
 scope TEXT NOT NULL,
 agent_id TEXT NOT NULL REFERENCES agents(id),
 name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 240),
 content BLOB NOT NULL,
 size_bytes INTEGER NOT NULL CHECK(size_bytes=length(content) AND size_bytes BETWEEN 0 AND 1048576),
 sha256 TEXT NOT NULL,
 created_at TEXT NOT NULL,
 UNIQUE(attempt_id,name)
);
CREATE INDEX access_files_scope ON access_files(scope,agent_id,created_at,id);
CREATE INDEX access_files_actor ON access_files(workspace_id,principal_id);
CREATE TRIGGER access_files_immutable BEFORE UPDATE ON access_files BEGIN
 SELECT RAISE(ABORT,'classified file versions are immutable');
END;
CREATE TRIGGER access_files_capacity BEFORE INSERT ON access_files BEGIN
 SELECT CASE WHEN NEW.size_bytes + COALESCE((SELECT SUM(size_bytes) FROM access_files WHERE workspace_id=NEW.workspace_id AND principal_id=NEW.principal_id),0)>33554432
 OR NEW.size_bytes + COALESCE((SELECT SUM(size_bytes) FROM access_files),0)>268435456
 OR (SELECT COUNT(*) FROM access_files)>=8192
 THEN RAISE(ABORT,'classified file capacity exhausted') END;
END;
