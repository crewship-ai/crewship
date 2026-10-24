-- Historical checkpoint interpretation cannot be inferred from the current engine.
-- NULL deliberately remains unknown through portable backup/restore.
ALTER TABLE mooring_collab_checkpoints ADD COLUMN checkpoint_metadata_json TEXT
CHECK (checkpoint_metadata_json IS NULL OR (
  typeof(checkpoint_metadata_json) = 'text'
  AND json_valid(checkpoint_metadata_json)
  AND json_type(checkpoint_metadata_json) = 'object'
));

-- Normal writers insert metadata with its checkpoint once. This is an UPDATE
-- guard, not authenticity against arbitrary trusted SQL replacement/restoration.
CREATE TRIGGER mooring_checkpoint_metadata_immutable
BEFORE UPDATE OF checkpoint_metadata_json ON mooring_collab_checkpoints
WHEN NEW.checkpoint_metadata_json IS NOT OLD.checkpoint_metadata_json
BEGIN
  SELECT RAISE(ABORT, 'checkpoint metadata is immutable');
END;
