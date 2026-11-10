-- Library publication and consumer pins (#5189).
--
-- Both tables are foreign-key children of artifact_version_heads with ON
-- DELETE CASCADE. A later migration that rebuilds artifact_version_heads must
-- copy them aside first (TestArtifactVersionHeadsForeignKeyChildrenAreKnown).
--
-- A Library is an artifact project whose design-system.json other projects
-- consume. Publishing is a separate right from editing: only the library's
-- owner moves the publication pointer. Restoring or editing a library appends
-- versions and never touches this pointer or any consumer pin.
CREATE TABLE artifact_library_publications (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version>0),
 published_by TEXT NOT NULL CHECK(length(published_by) BETWEEN 1 AND 256),
 published_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);

-- A consumer keeps a self-contained copy of the library's design-system.json
-- at the pinned version. Rendering the consumer reads only this row, so a
-- deleted, restored or no longer readable library never breaks it. The
-- library page id deliberately has no foreign key: the copy outlives the
-- library. One pin per consumer project.
CREATE TABLE artifact_library_pins (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 library_page_id TEXT NOT NULL CHECK(length(library_page_id) BETWEEN 1 AND 128),
 library_version INTEGER NOT NULL CHECK(library_version>0),
 library_name TEXT NOT NULL DEFAULT '' CHECK(length(library_name) <= 512),
 content BLOB NOT NULL CHECK(length(content) BETWEEN 2 AND 32768),
 sha256 TEXT NOT NULL CHECK(length(sha256)=64),
 copied_by TEXT NOT NULL CHECK(length(copied_by) BETWEEN 1 AND 256),
 copied_at TEXT NOT NULL,
 PRIMARY KEY(workspace_id,page_id),
 CHECK(library_page_id<>page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_version_heads(workspace_id,page_id) ON DELETE CASCADE
);
CREATE INDEX idx_artifact_library_pins_library ON artifact_library_pins(workspace_id,library_page_id);

-- Pins count against the aggregate artifact storage budget like every other
-- durable artifact row (artifact_storage_charge_apply); the expression is
-- storageLaterSourcesSQL's in internal/artifacts/storage_source_sql.go.
CREATE TRIGGER artifact_library_pins_storage_insert AFTER INSERT ON artifact_library_pins BEGIN INSERT INTO artifact_storage_charge VALUES('i',NULL,NEW.workspace_id,0,coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.library_page_id AS BLOB)),0)+coalesce(length(CAST(NEW.library_name AS BLOB)),0)+coalesce(length(CAST(NEW.copied_by AS BLOB)),0)+256); END;
CREATE TRIGGER artifact_library_pins_storage_delete AFTER DELETE ON artifact_library_pins BEGIN INSERT INTO artifact_storage_charge VALUES('d',OLD.workspace_id,NULL,coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.library_page_id AS BLOB)),0)+coalesce(length(CAST(OLD.library_name AS BLOB)),0)+coalesce(length(CAST(OLD.copied_by AS BLOB)),0)+256,0); END;
CREATE TRIGGER artifact_library_pins_storage_update AFTER UPDATE ON artifact_library_pins BEGIN INSERT INTO artifact_storage_charge VALUES('u',OLD.workspace_id,NEW.workspace_id,coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.library_page_id AS BLOB)),0)+coalesce(length(CAST(OLD.library_name AS BLOB)),0)+coalesce(length(CAST(OLD.copied_by AS BLOB)),0)+256,coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.library_page_id AS BLOB)),0)+coalesce(length(CAST(NEW.library_name AS BLOB)),0)+coalesce(length(CAST(NEW.copied_by AS BLOB)),0)+256); END;
