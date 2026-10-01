-- Project inputs are explicit immutable versions, never legacy storage paths.
CREATE TABLE project_files (
 id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 name TEXT NOT NULL,revision INTEGER NOT NULL CHECK(revision>0),head_version_id TEXT NOT NULL,
 retired_at TEXT,created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX project_files_live_name ON project_files(project_id,name) WHERE retired_at IS NULL;
CREATE TABLE project_file_versions (
 id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,file_id TEXT NOT NULL,name TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),size_bytes INTEGER NOT NULL CHECK(size_bytes BETWEEN 0 AND 1048576),
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),created_at TEXT NOT NULL,
 retired_at TEXT,UNIQUE(file_id,revision)
);
CREATE TABLE project_file_blobs (
 version_id TEXT PRIMARY KEY REFERENCES project_file_versions(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,content_base64 TEXT NOT NULL CHECK(length(content_base64)<=1398104)
);
CREATE TABLE attempt_project_inputs (
 attempt_id TEXT NOT NULL REFERENCES access_attempts(id) ON DELETE CASCADE,
 version_id TEXT NOT NULL REFERENCES project_file_versions(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL,project_id TEXT NOT NULL,file_id TEXT NOT NULL,
 scope TEXT NOT NULL,file_revision INTEGER NOT NULL,sha256 TEXT NOT NULL,
 PRIMARY KEY(attempt_id,version_id)
);
CREATE INDEX attempt_project_inputs_version ON attempt_project_inputs(version_id);
CREATE TRIGGER project_files_identity_immutable BEFORE UPDATE OF id,workspace_id,project_id,name,created_at ON project_files
BEGIN SELECT RAISE(ABORT,'project file identity is immutable'); END;
CREATE TRIGGER project_files_revision BEFORE UPDATE ON project_files
WHEN NEW.revision<>OLD.revision+1 OR OLD.retired_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'project file revision conflict'); END;
CREATE TRIGGER project_version_identity_immutable BEFORE UPDATE OF id,workspace_id,project_id,file_id,name,revision,size_bytes,sha256,created_at ON project_file_versions
BEGIN SELECT RAISE(ABORT,'project version identity is immutable'); END;
CREATE TRIGGER project_version_retirement BEFORE UPDATE OF retired_at ON project_file_versions
WHEN OLD.retired_at IS NOT NULL OR NEW.retired_at IS NULL
BEGIN SELECT RAISE(ABORT,'project version retirement is permanent'); END;
CREATE TRIGGER project_blob_immutable BEFORE UPDATE ON project_file_blobs
BEGIN SELECT RAISE(ABORT,'project version bytes are immutable'); END;
CREATE TRIGGER project_blob_capacity BEFORE INSERT ON project_file_blobs
WHEN NOT EXISTS(SELECT 1 FROM project_file_blobs WHERE version_id=NEW.version_id) AND (NOT EXISTS(SELECT 1 FROM project_file_versions v WHERE v.id=NEW.version_id AND v.workspace_id=NEW.workspace_id AND v.project_id=NEW.project_id AND v.retired_at IS NULL
 AND length(NEW.content_base64)=4*((v.size_bytes+2)/3))
 OR COALESCE((SELECT SUM(length(content_base64)) FROM project_file_blobs WHERE project_id=NEW.project_id),0)+length(NEW.content_base64)>33554432
 OR COALESCE((SELECT SUM(length(content_base64)) FROM project_file_blobs WHERE workspace_id=NEW.workspace_id),0)+length(NEW.content_base64)>134217728)
BEGIN SELECT RAISE(ABORT,'project file capacity or provenance denied'); END;
CREATE TRIGGER project_file_count BEFORE INSERT ON project_files
WHEN NOT EXISTS(SELECT 1 FROM project_files WHERE id=NEW.id) AND (SELECT COUNT(*) FROM project_files WHERE project_id=NEW.project_id AND retired_at IS NULL)>=256
BEGIN SELECT RAISE(ABORT,'project file capacity exceeded'); END;
CREATE TRIGGER project_version_count BEFORE INSERT ON project_file_versions
WHEN NOT EXISTS(SELECT 1 FROM project_file_versions WHERE id=NEW.id) AND (SELECT COUNT(*) FROM project_file_versions WHERE project_id=NEW.project_id)>=4096
BEGIN SELECT RAISE(ABORT,'project version capacity exceeded'); END;
CREATE TRIGGER attempt_project_input_immutable BEFORE UPDATE ON attempt_project_inputs
BEGIN SELECT RAISE(ABORT,'project input provenance is immutable'); END;
CREATE TRIGGER attempt_project_input_origin BEFORE INSERT ON attempt_project_inputs
WHEN NOT EXISTS(SELECT 1 FROM access_attempts a JOIN project_file_versions v ON v.id=NEW.version_id
 JOIN project_files f ON f.id=v.file_id AND f.head_version_id=v.id AND f.workspace_id=v.workspace_id AND f.project_id=v.project_id AND f.name=v.name AND f.revision=v.revision AND f.retired_at IS NULL
 JOIN project_file_blobs b ON b.version_id=v.id AND b.workspace_id=v.workspace_id AND b.project_id=v.project_id
 WHERE a.id=NEW.attempt_id AND a.completed_at IS NULL AND a.revoked_at IS NULL AND a.workspace_id=NEW.workspace_id
 AND NOT EXISTS(SELECT 1 FROM restricted_launches WHERE attempt_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM restricted_native_sessions WHERE attempt_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM access_context_dependencies WHERE attempt_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM access_context WHERE attempt_id=a.id)
 AND v.workspace_id=a.workspace_id AND v.project_id=NEW.project_id AND v.file_id=NEW.file_id AND v.revision=NEW.file_revision
 AND v.sha256=NEW.sha256 AND v.retired_at IS NULL
 AND EXISTS(SELECT 1 FROM json_each(a.rights) j WHERE json_extract(j.value,'$.kind')='project' AND json_extract(j.value,'$.id')=v.project_id AND json_extract(j.value,'$.operation')='read'))
BEGIN SELECT RAISE(ABORT,'project input authority denied'); END;
CREATE TRIGGER project_input_file_changed AFTER UPDATE ON project_files
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE file_id=NEW.id); END;
CREATE TRIGGER project_input_file_deleted BEFORE DELETE ON project_files
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE file_id=OLD.id); END;
CREATE TRIGGER project_input_version_retired AFTER UPDATE OF retired_at ON project_file_versions
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE version_id=NEW.id); END;
CREATE TRIGGER project_input_version_deleted BEFORE DELETE ON project_file_versions
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE version_id=OLD.id); END;
CREATE TRIGGER project_input_blob_deleted BEFORE DELETE ON project_file_blobs
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE version_id=OLD.version_id); END;
CREATE TRIGGER project_input_input_deleted BEFORE DELETE ON attempt_project_inputs
BEGIN UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id IN(SELECT attempt_id FROM attempt_project_inputs WHERE attempt_id=OLD.attempt_id); END;
CREATE TRIGGER project_file_retire_children AFTER DELETE ON project_files
BEGIN UPDATE project_file_versions SET retired_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE file_id=OLD.id AND retired_at IS NULL;
 DELETE FROM project_file_blobs WHERE version_id IN(SELECT id FROM project_file_versions WHERE file_id=OLD.id); END;
