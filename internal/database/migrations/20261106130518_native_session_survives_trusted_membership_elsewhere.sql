-- RC f3035b06c: an owner creating a workspace elsewhere retired every native Craft
-- editor they held (403 on the next save). Rights in one workspace do not depend on a
-- trusted membership row of another one, so a trusted row's insert, delete or role or
-- capability change no longer moves the global user revision, and it moves the
-- author-relevant generations only in its own workspace.
--
-- A session in the row's own workspace is still fenced: membership identity and
-- access_revision (access_member_update) move for removal, rejoin, role and capability
-- changes, and its generations move here. Anything restricted, and any change of the
-- row's identity, stays global: restriction anywhere is a ceiling everywhere.

-- A deleted member id is remembered, without any other data. access_revision restarts
-- at 1 on a new row, so a row restored under the same id could reach the revision of a
-- receipt taken before its removal, and id, revision and role would all match again.
-- The API always mints a new id, but a backup restore inserts the dumped row with its
-- id and revision, so reusing a retired id keeps the global bump.
CREATE TABLE workspace_member_retired_ids (id TEXT PRIMARY KEY) WITHOUT ROWID;

DROP TRIGGER mooring_epoch_workspace_members_insert;
DROP TRIGGER mooring_epoch_workspace_members_delete;
DROP TRIGGER mooring_epoch_workspace_members_update_old;
DROP TRIGGER mooring_epoch_workspace_members_update_new;
DROP TRIGGER mooring_relevant_workspace_member_insert;
DROP TRIGGER mooring_relevant_workspace_member_delete;
DROP TRIGGER mooring_relevant_workspace_member_old;
DROP TRIGGER mooring_relevant_workspace_member_new;

CREATE TRIGGER mooring_epoch_workspace_members_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id AND (NEW.access_mode IS NOT 'trusted' OR NEW.id IN (SELECT id FROM workspace_member_retired_ids));
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=NEW.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_delete BEFORE DELETE ON workspace_members
BEGIN
 INSERT OR IGNORE INTO workspace_member_retired_ids(id) VALUES(OLD.id);
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id AND OLD.access_mode IS NOT 'trusted';
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=OLD.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_update_old BEFORE UPDATE OF id,workspace_id,user_id,role,capabilities,access_mode,access_revision ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode OR OLD.access_revision IS NOT NEW.access_revision
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id AND (OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id);
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=OLD.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_update_new AFTER UPDATE OF id,workspace_id,user_id,role,capabilities,access_mode,access_revision ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode OR OLD.access_revision IS NOT NEW.access_revision
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id AND (OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id);
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (NEW.access_mode IS NOT 'trusted' OR workspace_id=NEW.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_delete BEFORE DELETE ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (OLD.access_mode IS NOT 'trusted' OR workspace_id=OLD.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_old BEFORE UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE ((OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id) OR workspace_id=OLD.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_new AFTER UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE ((OLD.access_mode IS NOT 'trusted' OR NEW.access_mode IS NOT 'trusted' OR OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id) OR workspace_id=NEW.workspace_id) AND (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;
