-- Live credentials retain a human custodian. Retired credentials may erase
-- that attribution without deleting their IDs or referenced audit history.
-- Keep the nullable creator FK and its original NO ACTION semantics.
CREATE TEMP TABLE credential_creator_expected(id TEXT PRIMARY KEY,old_creator TEXT);
INSERT INTO credential_creator_expected SELECT id,created_by FROM credentials;
ALTER TABLE credentials ADD COLUMN erasure_created_by TEXT REFERENCES users(id) ON DELETE NO ACTION;
UPDATE credentials SET erasure_created_by=created_by;
-- Never drop the old column unless every exact parent and creator survived
-- the copy. An existing IGNORE/delete/update trigger must abort this TX.
CREATE TEMP TABLE credential_creator_copy_guard(ack INTEGER CHECK(ack=1));
INSERT INTO credential_creator_copy_guard SELECT
    NOT EXISTS(SELECT 1 FROM credential_creator_expected e LEFT JOIN credentials c ON c.id=e.id
        WHERE c.id IS NULL OR c.created_by IS NOT e.old_creator OR c.erasure_created_by IS NOT e.old_creator)
    AND NOT EXISTS(SELECT 1 FROM credentials c LEFT JOIN credential_creator_expected e ON e.id=c.id WHERE e.id IS NULL);
DROP TABLE credential_creator_copy_guard;
DROP TABLE credential_creator_expected;
DROP INDEX idx_credential_created_by;
ALTER TABLE credentials DROP COLUMN created_by;
ALTER TABLE credentials RENAME COLUMN erasure_created_by TO created_by;
CREATE INDEX IF NOT EXISTS idx_credential_created_by ON credentials(created_by);

-- These direct constraints have no nested write whose IGNORE could suppress
-- validation while persisting an invalid live row. They also fence revival.
CREATE TRIGGER live_credential_creator_insert BEFORE INSERT ON credentials
WHEN NEW.created_by IS NULL AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT,'live credential requires creator');
END;
CREATE TRIGGER live_credential_creator_update BEFORE UPDATE OF created_by,deleted_at ON credentials
WHEN NEW.created_by IS NULL AND NEW.deleted_at IS NULL
BEGIN
    SELECT RAISE(ABORT,'live credential requires creator');
END;
