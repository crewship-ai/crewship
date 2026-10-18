-- A fresh human admission must explicitly include drafting. Existing consents
-- remain one-shot; creation provenance alone never enables continuation.
ALTER TABLE document_creation_consents ADD COLUMN drafting_allowed INTEGER NOT NULL DEFAULT 0 CHECK(drafting_allowed IN (0,1));
ALTER TABLE document_creation_consents ADD COLUMN draft_generation INTEGER CHECK(draft_generation IS NULL OR (drafting_allowed=1 AND draft_generation>=0));
ALTER TABLE document_creation_consents ADD COLUMN draft_rev INTEGER CHECK((draft_generation IS NULL)=(draft_rev IS NULL) AND (draft_rev IS NULL OR draft_rev>=0));

CREATE TRIGGER document_creation_drafting_purpose_immutable BEFORE UPDATE OF drafting_allowed ON document_creation_consents
WHEN NEW.drafting_allowed IS NOT OLD.drafting_allowed
BEGIN SELECT RAISE(ABORT,'original document drafting purpose is immutable'); END;

CREATE TRIGGER document_creation_draft_generation_immutable BEFORE UPDATE OF draft_generation ON document_creation_consents
WHEN OLD.draft_generation IS NOT NULL AND NEW.draft_generation IS NOT OLD.draft_generation
BEGIN SELECT RAISE(ABORT,'original document drafting generation is immutable'); END;

CREATE TRIGGER document_creation_draft_revision_monotonic BEFORE UPDATE OF draft_rev ON document_creation_consents
WHEN OLD.draft_rev IS NOT NULL AND (NEW.draft_rev IS NULL OR NEW.draft_rev<OLD.draft_rev)
BEGIN SELECT RAISE(ABORT,'document drafting revision cannot move backwards'); END;
