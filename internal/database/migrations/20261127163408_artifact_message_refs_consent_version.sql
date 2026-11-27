-- A person's consent to let an agent change a Sheet names the exact revision
-- they saw (#5317). Additive: older rows and references to anything but
-- a native Sheet keep NULL. A binary from before this column never names it and
-- keeps working; the agent then refuses to write on a NULL consent.
ALTER TABLE artifact_message_refs ADD COLUMN consent_version INTEGER CHECK(consent_version IS NULL OR consent_version>0);

-- The same rule in the schema: a consent is recorded only for a native Sheet,
-- and only for the revision that is its latest at that moment. The row stays
-- immutable (artifact_message_refs_immutable).
CREATE TRIGGER IF NOT EXISTS artifact_message_refs_consent_is_the_head BEFORE INSERT ON artifact_message_refs
WHEN NEW.consent_version IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM artifact_version_heads h
 WHERE h.workspace_id=NEW.workspace_id AND h.page_id=NEW.page_id AND h.content_type='gridcraft' AND h.latest_version=NEW.consent_version)
BEGIN SELECT RAISE(ABORT,'artifact message consent must name the latest revision of a native Sheet'); END;
