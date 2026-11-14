-- Operational cached_image may be cleared while configuration is rebuilt.
-- Retention records the last successful selection and its distinct predecessor,
-- independently of immutable build creation order or pending invalidation.
ALTER TABLE crews ADD COLUMN last_selected_image TEXT;
ALTER TABLE crews ADD COLUMN previous_image TEXT;
UPDATE crews SET last_selected_image=cached_image
WHERE length(cached_image)=71 AND substr(cached_image,1,7)='sha256:'
 AND substr(cached_image,8) NOT GLOB '*[^0-9a-f]*';
-- Existing history cannot establish previous selection order. Do not invent it.
CREATE TRIGGER crew_previous_image_selection AFTER UPDATE OF cached_image ON crews
WHEN length(NEW.cached_image)=71 AND substr(NEW.cached_image,1,7)='sha256:'
 AND substr(NEW.cached_image,8) NOT GLOB '*[^0-9a-f]*'
BEGIN
 UPDATE crews SET
  previous_image=CASE WHEN last_selected_image IS NOT NEW.cached_image
    THEN last_selected_image ELSE previous_image END,
  last_selected_image=NEW.cached_image
 WHERE id=NEW.id;
END;
