-- Catalog classification is metadata, never an access grant. Existing Pages
-- remain unmarked and unarchived; no historical inference or backfill.
ALTER TABLE pages ADD COLUMN created_by_test INTEGER NOT NULL DEFAULT 0 CHECK (created_by_test IN (0, 1));
ALTER TABLE pages ADD COLUMN archived_at TEXT;
