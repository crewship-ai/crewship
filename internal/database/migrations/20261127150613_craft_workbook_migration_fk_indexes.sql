-- Lead with each child key so both composite source lookups and workspace
-- cascades can locate conversion receipts without scanning the table.
CREATE INDEX idx_craft_workbook_migrations_workspace ON craft_workbook_migrations(workspace_id);
CREATE INDEX idx_craft_workbook_migrations_source_hash ON craft_workbook_migrations(source_hash, workspace_id, page_id);
CREATE INDEX idx_craft_workbook_migrations_source_version ON craft_workbook_migrations(source_version, workspace_id, page_id);
