-- Docs content lineage (#5163). The public Docs epoch tells a browser whether
-- its pending edits still belong to the content it edited. It is derived from
-- this installation's database identity and a per-document content generation,
-- so a plain restart keeps it and pending edits are replayed.
--
-- document_database_identity holds one installation-local value. The
-- migration creates the table empty: the value is created on first use
-- (documents.DatabaseIdentity), so every database gets its own value even
-- when it was copied from one migrated image, and a fresh migration stays
-- deterministic. Backup restore (logical restore, physical instance recovery,
-- pre-migration snapshot restore) replaces it. It is never part of a backup
-- bundle.
--
-- documents.content_generation advances when content is replaced (History
-- restore, schema upgrade, backup restore), never on an engine boot fence.
CREATE TABLE document_database_identity (id INTEGER PRIMARY KEY CHECK (id = 1), value TEXT NOT NULL CHECK (length(value) = 32));
ALTER TABLE documents ADD COLUMN content_generation INTEGER NOT NULL DEFAULT 0;
