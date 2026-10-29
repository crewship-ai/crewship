ALTER TABLE mooring_collab_operations ADD COLUMN dependencies_json TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(dependencies_json) AND json_type(dependencies_json)='array');
ALTER TABLE mooring_collab_operations ADD COLUMN target_effects_complete INTEGER NOT NULL DEFAULT 0 CHECK(target_effects_complete IN (0,1));
ALTER TABLE mooring_collab_documents ADD COLUMN target_tracking INTEGER NOT NULL DEFAULT 0 CHECK(target_tracking IN (0,1));
-- Durable host target bookkeeping and immutable effect provenance. No content
-- semantics or runtime capabilities. Canonical u64 decimal TEXT preserves the
-- full unsigned range; numeric ordering is length then BINARY text, never CAST.
CREATE TABLE mooring_collab_target_versions (
 page_id TEXT NOT NULL REFERENCES mooring_collab_documents(page_id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 target_id TEXT NOT NULL,
 revision TEXT NOT NULL CHECK((length(revision) BETWEEN 1 AND 20 AND instr(revision,char(0))=0 AND revision NOT GLOB '*[^0-9]*' AND (revision='0' OR substr(revision,1,1) BETWEEN '1' AND '9') AND (length(revision)<20 OR revision<='18446744073709551615'))),
 structure_revision TEXT NOT NULL CHECK((length(structure_revision) BETWEEN 1 AND 20 AND instr(structure_revision,char(0))=0 AND structure_revision NOT GLOB '*[^0-9]*' AND (structure_revision='0' OR substr(structure_revision,1,1) BETWEEN '1' AND '9') AND (length(structure_revision)<20 OR structure_revision<='18446744073709551615'))),
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 PRIMARY KEY(page_id,target_id)
);
CREATE INDEX mooring_collab_target_versions_workspace ON mooring_collab_target_versions(workspace_id);
CREATE TABLE mooring_collab_target_effects (
 page_id TEXT NOT NULL,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 op_id TEXT NOT NULL,
 target_id TEXT NOT NULL,
 revision TEXT NOT NULL CHECK((length(revision) BETWEEN 1 AND 20 AND instr(revision,char(0))=0 AND revision NOT GLOB '*[^0-9]*' AND (revision='0' OR substr(revision,1,1) BETWEEN '1' AND '9') AND (length(revision)<20 OR revision<='18446744073709551615'))),
 before_revision TEXT CHECK(before_revision IS NULL OR (length(before_revision) BETWEEN 1 AND 20 AND instr(before_revision,char(0))=0 AND before_revision NOT GLOB '*[^0-9]*' AND (before_revision='0' OR substr(before_revision,1,1) BETWEEN '1' AND '9') AND (length(before_revision)<20 OR before_revision<='18446744073709551615'))),
 before_structure_revision TEXT CHECK(before_structure_revision IS NULL OR (length(before_structure_revision) BETWEEN 1 AND 20 AND instr(before_structure_revision,char(0))=0 AND before_structure_revision NOT GLOB '*[^0-9]*' AND (before_structure_revision='0' OR substr(before_structure_revision,1,1) BETWEEN '1' AND '9') AND (length(before_structure_revision)<20 OR before_structure_revision<='18446744073709551615'))),
 content_changed INTEGER NOT NULL CHECK(content_changed IN (0,1)),
 structure_changed INTEGER NOT NULL CHECK(structure_changed IN (0,1)),
 created INTEGER NOT NULL CHECK(created IN (0,1)),
 deleted INTEGER NOT NULL CHECK(deleted IN (0,1)),
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 CHECK(json_type(record_json,'$.workspace_id') IS 'text' AND json_extract(record_json,'$.workspace_id')=workspace_id AND json_type(record_json,'$.page_id') IS 'text' AND json_extract(record_json,'$.page_id')=page_id AND json_type(record_json,'$.op_id') IS 'text' AND json_extract(record_json,'$.op_id')=op_id AND json_type(record_json,'$.target_id') IS 'text' AND json_extract(record_json,'$.target_id')=target_id AND json_type(record_json,'$.revision') IS 'text' AND json_extract(record_json,'$.revision')=revision AND ((before_revision IS NULL AND json_type(record_json,'$.before_revision') IS 'null') OR (before_revision IS NOT NULL AND json_type(record_json,'$.before_revision') IS 'text' AND json_extract(record_json,'$.before_revision')=before_revision)) AND ((before_structure_revision IS NULL AND json_type(record_json,'$.before_structure_revision') IS 'null') OR (before_structure_revision IS NOT NULL AND json_type(record_json,'$.before_structure_revision') IS 'text' AND json_extract(record_json,'$.before_structure_revision')=before_structure_revision)) AND (json_type(record_json,'$.content_changed') IS 'true' OR json_type(record_json,'$.content_changed') IS 'false') AND json_extract(record_json,'$.content_changed')=content_changed AND (json_type(record_json,'$.structure_changed') IS 'true' OR json_type(record_json,'$.structure_changed') IS 'false') AND json_extract(record_json,'$.structure_changed')=structure_changed AND (json_type(record_json,'$.created') IS 'true' OR json_type(record_json,'$.created') IS 'false') AND json_extract(record_json,'$.created')=created AND (json_type(record_json,'$.deleted') IS 'true' OR json_type(record_json,'$.deleted') IS 'false') AND json_extract(record_json,'$.deleted')=deleted),
 PRIMARY KEY(page_id,op_id,target_id),
 FOREIGN KEY(page_id,op_id) REFERENCES mooring_collab_operations(page_id,op_id) ON DELETE CASCADE,
 FOREIGN KEY(page_id,target_id) REFERENCES mooring_collab_target_versions(page_id,target_id) ON DELETE CASCADE,
 CHECK(content_changed=1 OR structure_changed=1),
 CHECK(NOT(created=1 AND deleted=1)),
 CHECK((created=1)=(before_revision IS NULL)),
 CHECK((before_revision IS NULL)=(before_structure_revision IS NULL)),
 CHECK((created=0 AND deleted=0) OR structure_changed=1)
);
CREATE INDEX mooring_collab_target_effects_workspace ON mooring_collab_target_effects(workspace_id);
CREATE INDEX mooring_collab_target_effects_target_order ON mooring_collab_target_effects(page_id,target_id,length(revision),revision COLLATE BINARY);
