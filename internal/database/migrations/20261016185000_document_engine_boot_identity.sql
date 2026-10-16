-- Engine restart barrier; the private listener must be owned before use.
ALTER TABLE document_writer_fence ADD COLUMN boot_id TEXT NOT NULL DEFAULT '';
