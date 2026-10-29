-- Each workbook revision records the hash of its resulting snapshot, so a
-- revision rebuilt from a checkpoint and the recorded commands can be verified.
-- Rows written earlier have no hash; they are not rebuilt from commands.
ALTER TABLE gridcraft_audit ADD COLUMN result_hash TEXT CHECK(result_hash IS NULL OR (length(result_hash)=71 AND result_hash GLOB 'sha256:*'));
