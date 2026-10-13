-- Existing revisions have no authoritative size metadata. NULL explicitly
-- denotes legacy content; reads may derive a manifest from verified bytes but
-- never pretend that it was recorded or silently mutate the old revision.
ALTER TABLE page_artifact_revisions ADD COLUMN manifest_json TEXT
 CHECK(manifest_json IS NULL OR (json_valid(manifest_json) AND length(manifest_json)<=2048));
CREATE TRIGGER artifact_revision_manifest_immutable BEFORE UPDATE OF manifest_json ON page_artifact_revisions
WHEN NEW.manifest_json IS NOT OLD.manifest_json
BEGIN SELECT RAISE(ABORT,'artifact revision manifest is immutable'); END;
