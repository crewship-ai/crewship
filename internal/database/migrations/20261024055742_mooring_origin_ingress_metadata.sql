-- Inactive identity/order metadata only: no ACL, bearer, source admission or issuance.
-- Original runtime inventory, current original author/owner checks, same-writer
-- reservation and COMMIT-before-publication are separate required API work.
-- Target-local tombstones survive portable replacement; physical lineage loss
-- remains unqualified. A matching portable marker never proves burned-ID coverage.
ALTER TABLE mooring_collab_documents ADD COLUMN origin_lineage_id TEXT NOT NULL DEFAULT ''
 CHECK(typeof(origin_lineage_id)='text' AND length(origin_lineage_id)<=200 AND instr(origin_lineage_id,char(0))=0);
CREATE TRIGGER mooring_origin_content_lineage_update BEFORE UPDATE OF origin_lineage_id ON mooring_collab_documents
 WHEN NEW.origin_lineage_id IS NOT OLD.origin_lineage_id
 BEGIN SELECT RAISE(ABORT,'origin content lineage is immutable'); END;
CREATE TRIGGER mooring_origin_content_lineage_replace BEFORE INSERT ON mooring_collab_documents
 WHEN EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id AND (origin_lineage_id<>'' OR origin_lineage_id IS NOT NEW.origin_lineage_id))
 BEGIN SELECT RAISE(ABORT,'origin content lineage cannot be replaced'); END;
CREATE TABLE mooring_origin_documents (
 lineage_id TEXT PRIMARY KEY CHECK(typeof(lineage_id)='text' AND length(lineage_id) BETWEEN 1 AND 200 AND instr(lineage_id,char(0))=0),
 workspace_id TEXT NOT NULL CHECK(typeof(workspace_id)='text' AND length(workspace_id) BETWEEN 1 AND 200 AND instr(workspace_id,char(0))=0),
 page_id TEXT NOT NULL UNIQUE CHECK(typeof(page_id)='text' AND length(page_id) BETWEEN 1 AND 200 AND instr(page_id,char(0))=0),
 application_id TEXT NOT NULL CHECK(application_id='wordcraft'),
 binding_version TEXT NOT NULL CHECK(binding_version='mooring.origin-document.v1'),
 initial_revision TEXT NOT NULL CHECK(typeof(initial_revision)='text' AND length(initial_revision) BETWEEN 1 AND 20 AND initial_revision NOT GLOB '*[^0-9]*' AND (length(initial_revision)=1 OR substr(initial_revision,1,1)<>'0') AND (length(initial_revision)<20 OR initial_revision<='18446744073709551615')),
 initial_checkpoint_digest TEXT NOT NULL CHECK(typeof(initial_checkpoint_digest)='text' AND length(initial_checkpoint_digest)=71 AND substr(initial_checkpoint_digest,1,7)='sha256:' AND substr(initial_checkpoint_digest,8) NOT GLOB '*[^0-9a-f]*'),
 initial_inventory_digest TEXT NOT NULL CHECK(typeof(initial_inventory_digest)='text' AND length(initial_inventory_digest)=71 AND substr(initial_inventory_digest,1,7)='sha256:' AND substr(initial_inventory_digest,8) NOT GLOB '*[^0-9a-f]*'),
 operation_count INTEGER NOT NULL DEFAULT 0 CHECK(typeof(operation_count)='integer' AND operation_count BETWEEN 0 AND 100000),
 operation_bytes INTEGER NOT NULL DEFAULT 0 CHECK(typeof(operation_bytes)='integer' AND operation_bytes BETWEEN 0 AND 67108864)
);
CREATE INDEX mooring_origin_documents_workspace ON mooring_origin_documents(workspace_id);
CREATE TRIGGER mooring_origin_documents_content BEFORE INSERT ON mooring_origin_documents
 WHEN NEW.operation_count<>0 OR NEW.operation_bytes<>0 OR NOT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=NEW.page_id AND workspace_id=NEW.workspace_id AND application_id=NEW.application_id AND origin_lineage_id=NEW.lineage_id)
 BEGIN SELECT RAISE(ABORT,'origin content lineage does not match'); END;
CREATE TABLE mooring_origin_exclusions (
 lineage_id TEXT NOT NULL REFERENCES mooring_origin_documents(lineage_id) ON DELETE RESTRICT,
 origin INTEGER NOT NULL CHECK(typeof(origin)='integer' AND origin BETWEEN 0 AND 9007199254740991),
 kind TEXT NOT NULL CHECK(kind IN ('baseline','issued')),
 source_id TEXT UNIQUE,
 first_clock INTEGER,
 PRIMARY KEY(lineage_id,origin),
 CHECK((kind='baseline' AND source_id IS NULL AND typeof(first_clock)='integer' AND first_clock BETWEEN 1 AND 4294967295)
    OR (kind='issued' AND origin>0 AND typeof(source_id)='text' AND length(source_id) BETWEEN 1 AND 200 AND instr(source_id,char(0))=0 AND first_clock IS NULL))
);
CREATE TRIGGER mooring_origin_exclusions_lineage BEFORE INSERT ON mooring_origin_exclusions
 WHEN NOT EXISTS(SELECT 1 FROM mooring_origin_documents WHERE lineage_id=NEW.lineage_id)
 BEGIN SELECT RAISE(ABORT,'origin lineage is missing'); END;
CREATE TRIGGER mooring_origin_exclusions_capacity BEFORE INSERT ON mooring_origin_exclusions
 WHEN (SELECT COUNT(*) FROM mooring_origin_exclusions WHERE lineage_id=NEW.lineage_id)>=4096
 BEGIN SELECT RAISE(ABORT,'bounded origin exclusion capacity exhausted'); END;
CREATE TABLE mooring_ingress_sources (
 source_id TEXT PRIMARY KEY CHECK(typeof(source_id)='text' AND length(source_id) BETWEEN 1 AND 200 AND instr(source_id,char(0))=0),
 lineage_id TEXT NOT NULL REFERENCES mooring_origin_documents(lineage_id) ON DELETE RESTRICT,
 origin INTEGER NOT NULL CHECK(typeof(origin)='integer' AND origin BETWEEN 1 AND 9007199254740991),
 kind TEXT NOT NULL CHECK(kind IN ('human','agent')),
 binding_version TEXT NOT NULL CHECK(binding_version='mooring.origin-author-binding.v1'),
 author_binding_digest TEXT NOT NULL CHECK(typeof(author_binding_digest)='text' AND length(author_binding_digest)=71 AND substr(author_binding_digest,1,7)='sha256:' AND substr(author_binding_digest,8) NOT GLOB '*[^0-9a-f]*'),
 runtime_ref TEXT NOT NULL CHECK(typeof(runtime_ref)='text' AND length(runtime_ref) BETWEEN 1 AND 200 AND instr(runtime_ref,char(0))=0),
 session_id TEXT NOT NULL CHECK(typeof(session_id)='text' AND length(session_id) BETWEEN 1 AND 200 AND instr(session_id,char(0))=0),
 host_incarnation TEXT NOT NULL CHECK(typeof(host_incarnation)='text' AND length(host_incarnation) BETWEEN 1 AND 200 AND instr(host_incarnation,char(0))=0),
 owner_generation INTEGER NOT NULL CHECK(typeof(owner_generation)='integer' AND owner_generation>0),
 owner_token_hash BLOB NOT NULL CHECK(typeof(owner_token_hash)='blob' AND length(owner_token_hash)=32),
 checkpoint_revision TEXT NOT NULL CHECK(typeof(checkpoint_revision)='text' AND length(checkpoint_revision) BETWEEN 1 AND 20 AND checkpoint_revision NOT GLOB '*[^0-9]*' AND (length(checkpoint_revision)=1 OR substr(checkpoint_revision,1,1)<>'0') AND (length(checkpoint_revision)<20 OR checkpoint_revision<='18446744073709551615')),
 checkpoint_digest TEXT NOT NULL CHECK(typeof(checkpoint_digest)='text' AND length(checkpoint_digest)=71 AND substr(checkpoint_digest,1,7)='sha256:' AND substr(checkpoint_digest,8) NOT GLOB '*[^0-9a-f]*'),
 next_ordinal INTEGER NOT NULL DEFAULT 1 CHECK(typeof(next_ordinal)='integer' AND next_ordinal>0),
 resolved_prefix INTEGER NOT NULL DEFAULT 0 CHECK(typeof(resolved_prefix)='integer' AND resolved_prefix>=0 AND resolved_prefix<next_ordinal),
 closed INTEGER NOT NULL DEFAULT 0 CHECK(typeof(closed)='integer' AND closed IN (0,1)),
 UNIQUE(lineage_id,origin),
 FOREIGN KEY(lineage_id,origin) REFERENCES mooring_origin_exclusions(lineage_id,origin) ON DELETE RESTRICT
);
CREATE INDEX mooring_ingress_sources_lineage ON mooring_ingress_sources(lineage_id);
CREATE TRIGGER mooring_ingress_sources_reservation BEFORE INSERT ON mooring_ingress_sources
 WHEN NEW.next_ordinal<>1 OR NEW.resolved_prefix<>0 OR NEW.closed<>0 OR NOT EXISTS(
 SELECT 1 FROM mooring_origin_exclusions WHERE lineage_id=NEW.lineage_id AND origin=NEW.origin AND kind='issued' AND source_id=NEW.source_id)
 BEGIN SELECT RAISE(ABORT,'fresh source reservation does not match'); END;
CREATE TABLE mooring_ingress_operations (
 lineage_id TEXT NOT NULL REFERENCES mooring_origin_documents(lineage_id) ON DELETE RESTRICT,
 op_id TEXT NOT NULL CHECK(typeof(op_id)='text' AND length(op_id) BETWEEN 1 AND 200 AND instr(op_id,char(0))=0),
 source_id TEXT NOT NULL REFERENCES mooring_ingress_sources(source_id) ON DELETE RESTRICT,
 ordinal INTEGER NOT NULL CHECK(typeof(ordinal)='integer' AND ordinal BETWEEN 1 AND 9223372036854775806),
 change_id TEXT NOT NULL CHECK(typeof(change_id)='text' AND length(change_id) BETWEEN 1 AND 200 AND instr(change_id,char(0))=0),
 membership_hash TEXT NOT NULL CHECK(typeof(membership_hash)='text' AND length(membership_hash)=71 AND substr(membership_hash,1,7)='sha256:' AND substr(membership_hash,8) NOT GLOB '*[^0-9a-f]*'),
 fingerprint TEXT NOT NULL CHECK(typeof(fingerprint)='text' AND length(fingerprint)=71 AND substr(fingerprint,1,7)='sha256:' AND substr(fingerprint,8) NOT GLOB '*[^0-9a-f]*'),
 raw_digest TEXT NOT NULL CHECK(typeof(raw_digest)='text' AND length(raw_digest)=71 AND substr(raw_digest,1,7)='sha256:' AND substr(raw_digest,8) NOT GLOB '*[^0-9a-f]*'),
 raw_bytes INTEGER NOT NULL CHECK(typeof(raw_bytes)='integer' AND raw_bytes BETWEEN 1 AND 262144),
 frontier_digest TEXT NOT NULL CHECK(typeof(frontier_digest)='text' AND length(frontier_digest)=71 AND substr(frontier_digest,1,7)='sha256:' AND substr(frontier_digest,8) NOT GLOB '*[^0-9a-f]*'),
 envelope_digest TEXT NOT NULL CHECK(typeof(envelope_digest)='text' AND length(envelope_digest)=71 AND substr(envelope_digest,1,7)='sha256:' AND substr(envelope_digest,8) NOT GLOB '*[^0-9a-f]*'),
 state TEXT NOT NULL DEFAULT 'accepted' CHECK(state IN ('accepted','resolved')),
 resolution_digest TEXT,
 PRIMARY KEY(lineage_id,op_id),
 UNIQUE(source_id,ordinal),
 CHECK((state='accepted' AND resolution_digest IS NULL) OR (state='resolved' AND typeof(resolution_digest)='text' AND length(resolution_digest)=71 AND substr(resolution_digest,1,7)='sha256:' AND substr(resolution_digest,8) NOT GLOB '*[^0-9a-f]*'))
);
CREATE INDEX mooring_ingress_operations_source ON mooring_ingress_operations(source_id,ordinal);
CREATE TRIGGER mooring_ingress_operations_admit BEFORE INSERT ON mooring_ingress_operations
 WHEN NEW.state<>'accepted' OR NEW.resolution_digest IS NOT NULL OR NOT EXISTS(
 SELECT 1 FROM mooring_ingress_sources WHERE source_id=NEW.source_id AND lineage_id=NEW.lineage_id AND closed=0 AND next_ordinal=NEW.ordinal AND next_ordinal<9223372036854775807)
 BEGIN SELECT RAISE(ABORT,'original source ordinal is unavailable'); END;
-- Logical immutable-operation budget, not a physical SQLite-file size promise.
CREATE TRIGGER mooring_ingress_operations_capacity BEFORE INSERT ON mooring_ingress_operations
 WHEN NOT EXISTS(SELECT 1 FROM mooring_origin_documents WHERE lineage_id=NEW.lineage_id AND operation_count<100000 AND operation_bytes+(512 + length(CAST(NEW.lineage_id AS BLOB)) + length(CAST(NEW.op_id AS BLOB)) + length(CAST(NEW.source_id AS BLOB)) + length(CAST(NEW.change_id AS BLOB)) + length(CAST(NEW.membership_hash AS BLOB)) + length(CAST(NEW.fingerprint AS BLOB)) + length(CAST(NEW.raw_digest AS BLOB)) + length(CAST(NEW.frontier_digest AS BLOB)) + length(CAST(NEW.envelope_digest AS BLOB)))<=67108864)
 BEGIN SELECT RAISE(ABORT,'bounded origin operation capacity exhausted'); END;
CREATE TRIGGER mooring_ingress_operations_consume AFTER INSERT ON mooring_ingress_operations
 BEGIN
 UPDATE mooring_ingress_sources SET next_ordinal=next_ordinal+1 WHERE source_id=NEW.source_id;
 UPDATE mooring_origin_documents SET operation_count=operation_count+1,operation_bytes=operation_bytes+(512 + length(CAST(NEW.lineage_id AS BLOB)) + length(CAST(NEW.op_id AS BLOB)) + length(CAST(NEW.source_id AS BLOB)) + length(CAST(NEW.change_id AS BLOB)) + length(CAST(NEW.membership_hash AS BLOB)) + length(CAST(NEW.fingerprint AS BLOB)) + length(CAST(NEW.raw_digest AS BLOB)) + length(CAST(NEW.frontier_digest AS BLOB)) + length(CAST(NEW.envelope_digest AS BLOB))) WHERE lineage_id=NEW.lineage_id;
 END;
CREATE TRIGGER mooring_ingress_operations_resolve AFTER UPDATE OF state ON mooring_ingress_operations
 WHEN OLD.state='accepted' AND NEW.state='resolved'
 BEGIN UPDATE mooring_ingress_sources SET resolved_prefix=NEW.ordinal WHERE source_id=NEW.source_id; END;
CREATE TRIGGER mooring_origin_documents_no_replace BEFORE INSERT ON mooring_origin_documents
 WHEN EXISTS(SELECT 1 FROM mooring_origin_documents WHERE lineage_id=NEW.lineage_id OR page_id=NEW.page_id)
 BEGIN SELECT RAISE(ABORT,'origin metadata cannot be replaced'); END;
CREATE TRIGGER mooring_origin_documents_no_delete BEFORE DELETE ON mooring_origin_documents
 BEGIN SELECT RAISE(ABORT,'origin metadata tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_origin_exclusions_no_replace BEFORE INSERT ON mooring_origin_exclusions
 WHEN EXISTS(SELECT 1 FROM mooring_origin_exclusions WHERE (lineage_id=NEW.lineage_id AND origin=NEW.origin) OR (NEW.source_id IS NOT NULL AND source_id=NEW.source_id))
 BEGIN SELECT RAISE(ABORT,'origin metadata cannot be replaced'); END;
CREATE TRIGGER mooring_origin_exclusions_no_delete BEFORE DELETE ON mooring_origin_exclusions
 BEGIN SELECT RAISE(ABORT,'origin metadata tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_ingress_sources_no_replace BEFORE INSERT ON mooring_ingress_sources
 WHEN EXISTS(SELECT 1 FROM mooring_ingress_sources WHERE source_id=NEW.source_id OR (lineage_id=NEW.lineage_id AND origin=NEW.origin))
 BEGIN SELECT RAISE(ABORT,'origin metadata cannot be replaced'); END;
CREATE TRIGGER mooring_ingress_sources_no_delete BEFORE DELETE ON mooring_ingress_sources
 BEGIN SELECT RAISE(ABORT,'origin metadata tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_ingress_operations_no_replace BEFORE INSERT ON mooring_ingress_operations
 WHEN EXISTS(SELECT 1 FROM mooring_ingress_operations WHERE (lineage_id=NEW.lineage_id AND op_id=NEW.op_id) OR (source_id=NEW.source_id AND ordinal=NEW.ordinal))
 BEGIN SELECT RAISE(ABORT,'origin metadata cannot be replaced'); END;
CREATE TRIGGER mooring_ingress_operations_no_delete BEFORE DELETE ON mooring_ingress_operations
 BEGIN SELECT RAISE(ABORT,'origin metadata tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_origin_documents_immutable BEFORE UPDATE ON mooring_origin_documents
 WHEN NEW.lineage_id IS NOT OLD.lineage_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.page_id IS NOT OLD.page_id OR NEW.application_id IS NOT OLD.application_id OR NEW.binding_version IS NOT OLD.binding_version OR NEW.initial_revision IS NOT OLD.initial_revision OR NEW.initial_checkpoint_digest IS NOT OLD.initial_checkpoint_digest OR NEW.initial_inventory_digest IS NOT OLD.initial_inventory_digest
 BEGIN SELECT RAISE(ABORT,'original origin metadata is immutable'); END;
CREATE TRIGGER mooring_origin_exclusions_immutable BEFORE UPDATE ON mooring_origin_exclusions
 WHEN NEW.lineage_id IS NOT OLD.lineage_id OR NEW.origin IS NOT OLD.origin OR NEW.kind IS NOT OLD.kind OR NEW.source_id IS NOT OLD.source_id OR NEW.first_clock IS NOT OLD.first_clock
 BEGIN SELECT RAISE(ABORT,'original origin metadata is immutable'); END;
CREATE TRIGGER mooring_ingress_sources_immutable BEFORE UPDATE ON mooring_ingress_sources
 WHEN NEW.source_id IS NOT OLD.source_id OR NEW.lineage_id IS NOT OLD.lineage_id OR NEW.origin IS NOT OLD.origin OR NEW.kind IS NOT OLD.kind OR NEW.binding_version IS NOT OLD.binding_version OR NEW.author_binding_digest IS NOT OLD.author_binding_digest OR NEW.runtime_ref IS NOT OLD.runtime_ref OR NEW.session_id IS NOT OLD.session_id OR NEW.host_incarnation IS NOT OLD.host_incarnation OR NEW.owner_generation IS NOT OLD.owner_generation OR NEW.owner_token_hash IS NOT OLD.owner_token_hash OR NEW.checkpoint_revision IS NOT OLD.checkpoint_revision OR NEW.checkpoint_digest IS NOT OLD.checkpoint_digest
 BEGIN SELECT RAISE(ABORT,'original origin metadata is immutable'); END;
CREATE TRIGGER mooring_ingress_operations_immutable BEFORE UPDATE ON mooring_ingress_operations
 WHEN NEW.lineage_id IS NOT OLD.lineage_id OR NEW.op_id IS NOT OLD.op_id OR NEW.source_id IS NOT OLD.source_id OR NEW.ordinal IS NOT OLD.ordinal OR NEW.change_id IS NOT OLD.change_id OR NEW.membership_hash IS NOT OLD.membership_hash OR NEW.fingerprint IS NOT OLD.fingerprint OR NEW.raw_digest IS NOT OLD.raw_digest OR NEW.raw_bytes IS NOT OLD.raw_bytes OR NEW.frontier_digest IS NOT OLD.frontier_digest OR NEW.envelope_digest IS NOT OLD.envelope_digest
 BEGIN SELECT RAISE(ABORT,'original origin metadata is immutable'); END;
CREATE TRIGGER mooring_ingress_sources_monotonic BEFORE UPDATE ON mooring_ingress_sources
 WHEN NEW.next_ordinal<OLD.next_ordinal OR NEW.resolved_prefix<OLD.resolved_prefix OR NEW.closed<OLD.closed
 OR (NEW.resolved_prefix<>OLD.resolved_prefix AND (NEW.resolved_prefix<>OLD.resolved_prefix+1 OR NOT EXISTS(SELECT 1 FROM mooring_ingress_operations WHERE source_id=OLD.source_id AND ordinal=NEW.resolved_prefix AND state='resolved')))
 BEGIN SELECT RAISE(ABORT,'original source counters cannot rewind or skip resolution'); END;
CREATE TRIGGER mooring_ingress_operations_resolution BEFORE UPDATE ON mooring_ingress_operations
 WHEN NEW.state IS NOT OLD.state OR NEW.resolution_digest IS NOT OLD.resolution_digest
 BEGIN
 SELECT CASE WHEN OLD.state<>'accepted' OR NEW.state<>'resolved' OR NOT EXISTS(SELECT 1 FROM mooring_ingress_sources WHERE source_id=OLD.source_id AND resolved_prefix+1=OLD.ordinal)
 THEN RAISE(ABORT,'original source resolution is immutable and ordered') END;
 END;
CREATE TRIGGER mooring_origin_page_retire BEFORE DELETE ON pages
 BEGIN UPDATE mooring_ingress_sources SET closed=1 WHERE lineage_id IN (SELECT lineage_id FROM mooring_origin_documents WHERE page_id=OLD.id); END;
CREATE TRIGGER mooring_origin_workspace_retire BEFORE DELETE ON workspaces
 BEGIN UPDATE mooring_ingress_sources SET closed=1 WHERE lineage_id IN (SELECT lineage_id FROM mooring_origin_documents WHERE workspace_id=OLD.id); END;
CREATE TRIGGER mooring_origin_documents_monotonic BEFORE UPDATE ON mooring_origin_documents
 WHEN NEW.operation_count<OLD.operation_count OR NEW.operation_bytes<OLD.operation_bytes
 BEGIN SELECT RAISE(ABORT,'origin operation budgets cannot rewind'); END;

CREATE TRIGGER mooring_origin_content_retire BEFORE DELETE ON mooring_collab_documents
 BEGIN UPDATE mooring_ingress_sources SET closed=1 WHERE lineage_id IN (SELECT lineage_id FROM mooring_origin_documents WHERE page_id=OLD.page_id); END;
