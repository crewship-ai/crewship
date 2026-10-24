-- Inactive, caller-supplied atomic bytes are DATA ONLY, not original-source or
-- replay authority. Existing changes receive no guessed/backfilled bytes.
CREATE TABLE mooring_collab_raw_changes (
 page_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 change_id TEXT NOT NULL,
 format TEXT NOT NULL CHECK(format='atomic-change-data.v1'),
 protocol_version TEXT NOT NULL CHECK(protocol_version='0.4.0'),
 model_version TEXT NOT NULL CHECK(model_version='0.2.0'),
 engine_version TEXT NOT NULL CHECK(engine_version='0.10.0'),
 raw_atomic BLOB NOT NULL CHECK(typeof(raw_atomic)='blob' AND length(raw_atomic) BETWEEN 1 AND 262144),
 raw_digest TEXT NOT NULL CHECK(typeof(raw_digest)='text' AND length(raw_digest)=71 AND substr(raw_digest,1,7)='sha256:' AND substr(raw_digest,8) NOT GLOB '*[^0-9a-f]*'),
 canonical_atomic BLOB NOT NULL CHECK(typeof(canonical_atomic)='blob' AND length(canonical_atomic) BETWEEN 1 AND 262144),
 canonical_digest TEXT NOT NULL CHECK(typeof(canonical_digest)='text' AND length(canonical_digest)=71 AND substr(canonical_digest,1,7)='sha256:' AND substr(canonical_digest,8) NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(page_id,change_id),
 FOREIGN KEY(page_id,change_id) REFERENCES mooring_collab_changes(page_id,change_id) ON DELETE CASCADE
);
CREATE INDEX mooring_collab_raw_changes_workspace ON mooring_collab_raw_changes(workspace_id);

-- Prevent REPLACE from deleting/relabeling old bytes even when recursive
-- triggers are OFF. Portable replacement explicitly deletes content first.
CREATE TRIGGER mooring_raw_change_no_replace BEFORE INSERT ON mooring_collab_raw_changes
WHEN EXISTS(SELECT 1 FROM mooring_collab_raw_changes WHERE page_id=NEW.page_id AND change_id=NEW.change_id)
BEGIN SELECT RAISE(ABORT,'raw atomic data already exists'); END;

CREATE TRIGGER mooring_raw_change_saved_scope BEFORE INSERT ON mooring_collab_raw_changes
WHEN NOT EXISTS(SELECT 1 FROM mooring_collab_changes c
 WHERE c.page_id=NEW.page_id AND c.change_id=NEW.change_id AND c.workspace_id=NEW.workspace_id
 AND CASE WHEN json_valid(c.result_json) THEN json_extract(c.result_json,'$.status')='saved' ELSE 0 END)
BEGIN SELECT RAISE(ABORT,'raw atomic data requires same-scope Saved change'); END;

CREATE TRIGGER mooring_raw_change_immutable BEFORE UPDATE ON mooring_collab_raw_changes
BEGIN SELECT RAISE(ABORT,'raw atomic data is immutable'); END;
