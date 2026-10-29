-- Existing rows retain their legacy encoding. Profiles are immutable: this is
-- not a conversion or a new content-custody system.
ALTER TABLE mooring_collab_documents ADD COLUMN format_profile TEXT NOT NULL DEFAULT 'mooring-causal-v1';
ALTER TABLE mooring_collab_documents ADD COLUMN engine_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE mooring_collab_documents ADD COLUMN engine_sha256 TEXT NOT NULL DEFAULT '';

CREATE TRIGGER mooring_profile_insert BEFORE INSERT ON mooring_collab_documents
WHEN NOT (
 (NEW.format_profile='mooring-causal-v1' AND NEW.engine_revision='' AND NEW.engine_sha256='')
 OR (NEW.application_id='wordcraft' AND NEW.format_profile='wordcraft-c1-v2'
     AND NEW.engine_revision='7ef2633a7e84956522d1ca47b99fe6feef16c1db'
     AND length(NEW.engine_sha256)=71 AND substr(NEW.engine_sha256,1,7)='sha256:'
     AND substr(NEW.engine_sha256,8) NOT GLOB '*[^0-9a-f]*')
)
BEGIN SELECT RAISE(ABORT,'Unsupported Mooring document profile'); END;

CREATE TRIGGER mooring_profile_immutable BEFORE UPDATE OF format_profile,engine_revision,engine_sha256 ON mooring_collab_documents
WHEN OLD.format_profile IS NOT NEW.format_profile OR OLD.engine_revision IS NOT NEW.engine_revision OR OLD.engine_sha256 IS NOT NEW.engine_sha256
BEGIN SELECT RAISE(ABORT,'Mooring document profile is immutable'); END;
