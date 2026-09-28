-- Existing links retain the shared-file behavior they already granted.
-- The two directions are independent; communication direction still gates use.
ALTER TABLE crew_connections ADD COLUMN forward_file_access TEXT NOT NULL DEFAULT 'read_write'
    CHECK (forward_file_access IN ('none', 'read', 'read_write'));
ALTER TABLE crew_connections ADD COLUMN reverse_file_access TEXT NOT NULL DEFAULT 'read_write'
    CHECK (reverse_file_access IN ('none', 'read', 'read_write'));
ALTER TABLE crew_connections ADD COLUMN access_version INTEGER NOT NULL DEFAULT 1;
