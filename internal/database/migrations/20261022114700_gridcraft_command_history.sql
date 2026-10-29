-- Live workbook editing records each confirmed command and its writer, and keeps
-- full snapshots only at checkpoints and imports (the head stays a full snapshot).
-- Rows written earlier carry no command: they are marked as snapshot revisions so
-- cell-level concurrency never rebases a mutation across unknown history.
ALTER TABLE gridcraft_audit ADD COLUMN command BLOB CHECK(command IS NULL OR json_valid(command));
ALTER TABLE gridcraft_audit ADD COLUMN kind TEXT NOT NULL DEFAULT 'snapshot' CHECK(kind IN ('snapshot','cells') AND (kind='snapshot' OR command IS NOT NULL));
ALTER TABLE gridcraft_audit ADD COLUMN writer TEXT NOT NULL DEFAULT '';
UPDATE gridcraft_audit SET writer=authority_id;
