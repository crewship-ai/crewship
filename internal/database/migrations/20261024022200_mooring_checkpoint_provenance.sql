-- Legacy checkpoint creator generation cannot be reconstructed from the live owner.
-- NULL is retained through backup/restore and is not eligible for session activation.
ALTER TABLE mooring_collab_checkpoints ADD COLUMN checkpoint_generation TEXT
CHECK (checkpoint_generation IS NULL OR (
  typeof(checkpoint_generation) = 'text'
  AND length(checkpoint_generation) BETWEEN 1 AND 20
  AND checkpoint_generation NOT GLOB '*[^0-9]*'
  AND substr(checkpoint_generation, 1, 1) BETWEEN '1' AND '9'
  AND (length(checkpoint_generation) < 20 OR checkpoint_generation <= '18446744073709551615')
));

CREATE TRIGGER mooring_checkpoint_generation_immutable
BEFORE UPDATE OF checkpoint_generation ON mooring_collab_checkpoints
WHEN NEW.checkpoint_generation IS NOT OLD.checkpoint_generation
BEGIN
  SELECT RAISE(ABORT, 'checkpoint provenance is immutable');
END;
