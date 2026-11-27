-- How many cells an undo put back, beside how many it kept: a repeated undo
-- answers both. Additive.
ALTER TABLE craft_version_undos ADD COLUMN restored INTEGER NOT NULL DEFAULT 0;
