-- #5123: a grant on Page B retired every open native Word/Sheet and Design
-- session of the grant's recipients on every other Page. The grant triggers
-- moved the recipients' workspace-wide `user`/`agent` generation, and a
-- `workspace` or `crew` grant names every member. A grant on a Page changes
-- access to that Page only (canSeePage, mayEditSpec and grantsFor read the
-- grants of the Page itself), so its fence is the pair of that Page and that
-- recipient.
--
-- Same contract as mooring_authority_generations: not an ACL, no permission is
-- stored or inferred; a row is installed only by a fresh authenticated
-- admission after the canonical checks allowed it, and canonical mutations only
-- advance rows that already exist. An absent row under a captured proof is
-- uncertainty and denies. Tombstones never disappear and never decrease, and
-- they do not travel in a bundle (backup intent: runtime, target-local).
CREATE TABLE mooring_page_grant_generations (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 principal_kind TEXT NOT NULL CHECK(principal_kind IN ('user','agent')),
 principal_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision)='integer' AND revision>0),
 PRIMARY KEY(workspace_id,page_id,principal_kind,principal_id)
) WITHOUT ROWID;
CREATE TRIGGER mooring_page_grant_generation_key_immutable BEFORE UPDATE OF workspace_id,page_id,principal_kind,principal_id ON mooring_page_grant_generations
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.page_id IS NOT NEW.page_id OR OLD.principal_kind IS NOT NEW.principal_kind OR OLD.principal_id IS NOT NEW.principal_id
BEGIN SELECT RAISE(ABORT,'page grant generation identity is immutable'); END;
CREATE TRIGGER mooring_page_grant_generation_no_delete BEFORE DELETE ON mooring_page_grant_generations
BEGIN SELECT RAISE(ABORT,'page grant generation tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_page_grant_generation_no_replace BEFORE INSERT ON mooring_page_grant_generations
WHEN EXISTS(SELECT 1 FROM mooring_page_grant_generations WHERE workspace_id=NEW.workspace_id AND page_id=NEW.page_id AND principal_kind=NEW.principal_kind AND principal_id=NEW.principal_id)
BEGIN SELECT RAISE(ABORT,'page grant generation identity already exists'); END;
CREATE TRIGGER mooring_page_grant_generation_monotonic BEFORE UPDATE OF revision ON mooring_page_grant_generations
WHEN NEW.revision<OLD.revision
BEGIN SELECT RAISE(ABORT,'page grant generation cannot decrease'); END;

-- The four direct-grant triggers keep their recipient expansion and their
-- column lists; only the advanced key changes from the recipient's global
-- generation to the (Page, recipient) pair. The issuer fanout in the
-- membership and user triggers is unchanged and still moves recipients' user
-- generations (an issuer's standing is not Page-local).
DROP TRIGGER mooring_relevant_grant_insert;
DROP TRIGGER mooring_relevant_grant_delete;
DROP TRIGGER mooring_relevant_grant_old;
DROP TRIGGER mooring_relevant_grant_new;

CREATE TRIGGER mooring_relevant_grant_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_grant_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_page_grant_generations SET revision=revision+1 WHERE (workspace_id,page_id,principal_kind,principal_id) IN (SELECT workspace_id,page_id,principal_kind,principal_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;
