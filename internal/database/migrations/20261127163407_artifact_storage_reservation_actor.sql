-- The person (or agent run) holding an upload reservation, so one of them cannot take every
-- upload slot of a workspace. Reservations live two minutes; older rows simply have none.
ALTER TABLE artifact_storage_reservations ADD COLUMN actor TEXT;
CREATE INDEX artifact_storage_reservations_actor ON artifact_storage_reservations(actor);
