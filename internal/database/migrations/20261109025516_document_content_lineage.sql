-- Docs content lineage (#5163). The public Docs epoch tells a browser whether
-- its pending edits still belong to the content it edited. It is derived from
-- this installation's database identity and a per-document content generation,
-- so a plain restart keeps it and pending edits are replayed.
--
-- document_database_identity is created once and rotated only by backup
-- restore (logical restore and physical instance recovery). It is
-- installation-local and never part of a backup bundle.
--
-- documents.content_generation advances when content is replaced (History
-- restore, schema upgrade, backup restore), never on an engine boot fence.
CREATE TABLE document_database_identity (id INTEGER PRIMARY KEY CHECK (id = 1), value TEXT NOT NULL CHECK (length(value) = 32));
INSERT INTO document_database_identity(id,value) VALUES (1, lower(hex(randomblob(16))));
ALTER TABLE documents ADD COLUMN content_generation INTEGER NOT NULL DEFAULT 0;
