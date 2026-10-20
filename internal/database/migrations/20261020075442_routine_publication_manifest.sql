-- Immutable authoring metadata, not an execution receipt. Existing history
-- and grants are unchanged; normal backup export includes the nullable column.
ALTER TABLE pipeline_versions ADD COLUMN publication_authority_json TEXT
    CHECK(publication_authority_json IS NULL OR
        (typeof(publication_authority_json)='text' AND json_valid(publication_authority_json)));
CREATE TRIGGER pipeline_versions_publication_manifest_immutable
BEFORE UPDATE OF publication_authority_json ON pipeline_versions
WHEN NEW.publication_authority_json IS NOT OLD.publication_authority_json
BEGIN SELECT RAISE(ABORT,'publication manifest is immutable'); END;
