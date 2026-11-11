-- Library pin dependencies (#5248).
--
-- A pin carries, besides its design-system.json copy, the files the system
-- needs to render: font files (decoded bytes) and the licence files that let
-- them travel. A font without a redistributable licence, or over a size cap,
-- is listed with its status and no bytes, so the update review can say why it
-- was left behind. Rows are replaced as a whole with their pin.
--
-- The table is a foreign-key child of artifact_library_pins (itself a child of
-- artifact_version_heads), so deleting the consumer Page removes it. A later
-- migration that rebuilds either parent must copy these rows aside first.
CREATE TABLE artifact_library_pin_files (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 path TEXT NOT NULL CHECK(length(path) BETWEEN 1 AND 512),
 media_type TEXT NOT NULL CHECK(media_type IN ('font/woff2','font/woff','font/ttf','font/otf','text/plain')),
 status TEXT NOT NULL CHECK(status IN ('copied','licence','too_large')),
 content BLOB NOT NULL DEFAULT x'' CHECK(length(content) <= 1048576),
 size INTEGER NOT NULL DEFAULT 0 CHECK(size BETWEEN 0 AND 1048576),
 sha256 TEXT NOT NULL DEFAULT '' CHECK(length(sha256) IN (0,64)),
 family TEXT NOT NULL DEFAULT '' CHECK(length(family) <= 64),
 weight INTEGER NOT NULL DEFAULT 0 CHECK(weight BETWEEN 0 AND 900),
 style TEXT NOT NULL DEFAULT '' CHECK(style IN ('','normal','italic')),
 licence TEXT NOT NULL DEFAULT '' CHECK(length(licence) <= 64),
 PRIMARY KEY(workspace_id,page_id,path),
 CHECK((status='copied' AND length(sha256)=64 AND size=length(content)) OR (status<>'copied' AND length(content)=0 AND size=0 AND sha256='')),
 FOREIGN KEY(workspace_id,page_id) REFERENCES artifact_library_pins(workspace_id,page_id) ON DELETE CASCADE
);

-- Pinned files count against the aggregate artifact storage budget like the
-- pin itself; the expression is storageLaterSourcesSQL's.
CREATE TRIGGER artifact_library_pin_files_storage_insert AFTER INSERT ON artifact_library_pin_files BEGIN INSERT INTO artifact_storage_charge VALUES('i',NULL,NEW.workspace_id,0,coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.path AS BLOB)),0)+coalesce(length(CAST(NEW.family AS BLOB)),0)+128); END;
CREATE TRIGGER artifact_library_pin_files_storage_delete AFTER DELETE ON artifact_library_pin_files BEGIN INSERT INTO artifact_storage_charge VALUES('d',OLD.workspace_id,NULL,coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.path AS BLOB)),0)+coalesce(length(CAST(OLD.family AS BLOB)),0)+128,0); END;
CREATE TRIGGER artifact_library_pin_files_storage_update AFTER UPDATE ON artifact_library_pin_files BEGIN INSERT INTO artifact_storage_charge VALUES('u',OLD.workspace_id,NEW.workspace_id,coalesce(length(CAST(OLD.content AS BLOB)),0)+coalesce(length(CAST(OLD.path AS BLOB)),0)+coalesce(length(CAST(OLD.family AS BLOB)),0)+128,coalesce(length(CAST(NEW.content AS BLOB)),0)+coalesce(length(CAST(NEW.path AS BLOB)),0)+coalesce(length(CAST(NEW.family AS BLOB)),0)+128); END;
