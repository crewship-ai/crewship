-- Canonical human authority generations, not a second permissions database.
-- Actual principal/RBAC checks remain mandatory at admission/renewal/commit.
-- This initial fence covers canonical human ACL/credential mutation. Agent
-- mandate/attempt and all content-path/response barriers require integration
-- before Mooring enablement. Closing a gate never claims a blocked ACK.
ALTER TABLE workspaces ADD COLUMN authority_revision INTEGER NOT NULL DEFAULT 1
 CHECK(typeof(authority_revision)='integer' AND authority_revision>0);
ALTER TABLE users ADD COLUMN authority_revision INTEGER NOT NULL DEFAULT 1
 CHECK(typeof(authority_revision)='integer' AND authority_revision>0);
-- Captured only from the original trusted admission receipt. Legacy empty
-- values must not be upgraded by a late run/token lookup.
ALTER TABLE artifact_run_principals ADD COLUMN user_authority_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_run_principals ADD COLUMN source_credential_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_run_principals ADD COLUMN source_credential_id TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_run_principals ADD COLUMN source_credential_expires_at TEXT NOT NULL DEFAULT '';
ALTER TABLE artifact_run_principals ADD COLUMN source_credential_binding TEXT NOT NULL DEFAULT '';
CREATE TRIGGER mooring_original_principal_fence_immutable BEFORE UPDATE OF
 user_authority_revision,source_credential_kind,source_credential_id,source_credential_expires_at,source_credential_binding
ON artifact_run_principals
WHEN OLD.user_authority_revision IS NOT NEW.user_authority_revision
 OR OLD.source_credential_kind IS NOT NEW.source_credential_kind
 OR OLD.source_credential_id IS NOT NEW.source_credential_id
 OR OLD.source_credential_expires_at IS NOT NEW.source_credential_expires_at
 OR OLD.source_credential_binding IS NOT NEW.source_credential_binding
BEGIN SELECT RAISE(ABORT,'original principal admission fence is immutable'); END;


CREATE TRIGGER mooring_workspace_authority_monotonic BEFORE UPDATE OF authority_revision ON workspaces
WHEN NEW.authority_revision < OLD.authority_revision
BEGIN SELECT RAISE(ABORT,'workspace authority revision cannot decrease'); END;
CREATE TRIGGER mooring_user_authority_monotonic BEFORE UPDATE OF authority_revision ON users
WHEN NEW.authority_revision < OLD.authority_revision
BEGIN SELECT RAISE(ABORT,'user authority revision cannot decrease'); END;

-- Signed-integer overflow becomes REAL in SQLite and fails the column CHECK,
-- rolling back the entire canonical mutation and every earlier trigger effect.
CREATE TRIGGER mooring_workspace_authority_close AFTER UPDATE OF authority_revision ON workspaces
WHEN OLD.authority_revision IS NOT NEW.authority_revision
BEGIN
 UPDATE mooring_delivery_gates SET closed=1 WHERE workspace_id=NEW.id;
END;
CREATE TRIGGER mooring_user_authority_fanout AFTER UPDATE OF authority_revision ON users
WHEN OLD.authority_revision IS NOT NEW.authority_revision
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1
 WHERE id IN (SELECT workspace_id FROM workspace_members WHERE user_id=NEW.id);
END;

CREATE TRIGGER mooring_epoch_page_grants_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_grants_update_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_folder_acl_insert AFTER INSERT ON page_folder_acl
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM page_folders WHERE id=NEW.folder_id);
END;

CREATE TRIGGER mooring_epoch_page_folder_acl_delete BEFORE DELETE ON page_folder_acl
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM page_folders WHERE id=OLD.folder_id);
END;

CREATE TRIGGER mooring_epoch_page_folder_acl_update_old BEFORE UPDATE OF folder_id,subject_type,subject_id,can_read,can_write ON page_folder_acl
WHEN OLD.folder_id IS NOT NEW.folder_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.can_read IS NOT NEW.can_read OR OLD.can_write IS NOT NEW.can_write
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM page_folders WHERE id=OLD.folder_id);
END;

CREATE TRIGGER mooring_epoch_page_folder_acl_update_new AFTER UPDATE OF folder_id,subject_type,subject_id,can_read,can_write ON page_folder_acl
WHEN OLD.folder_id IS NOT NEW.folder_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.can_read IS NOT NEW.can_read OR OLD.can_write IS NOT NEW.can_write
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM page_folders WHERE id=NEW.folder_id);
END;

CREATE TRIGGER mooring_epoch_page_project_live_insert AFTER INSERT ON page_project_live
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_project_live_delete BEFORE DELETE ON page_project_live
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_project_live_update_old BEFORE UPDATE OF page_id,version ON page_project_live
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_project_live_update_new AFTER UPDATE OF page_id,version ON page_project_live
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_pages_insert AFTER INSERT ON pages
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_pages_delete BEFORE DELETE ON pages
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_pages_update_old BEFORE UPDATE OF id,workspace_id,owner_user_id,owner_crew_id,private_audience,folder_id ON pages
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.private_audience IS NOT NEW.private_audience OR OLD.folder_id IS NOT NEW.folder_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_pages_update_new AFTER UPDATE OF id,workspace_id,owner_user_id,owner_crew_id,private_audience,folder_id ON pages
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.private_audience IS NOT NEW.private_audience OR OLD.folder_id IS NOT NEW.folder_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_page_folders_insert AFTER INSERT ON page_folders
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_page_folders_delete BEFORE DELETE ON page_folders
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_page_folders_update_old BEFORE UPDATE OF id,workspace_id,owner_crew_id ON page_folders
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_page_folders_update_new AFTER UPDATE OF id,workspace_id,owner_crew_id ON page_folders
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_crew_members_insert AFTER INSERT ON crew_members
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM crews WHERE id=NEW.crew_id);
END;

CREATE TRIGGER mooring_epoch_crew_members_delete BEFORE DELETE ON crew_members
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM crews WHERE id=OLD.crew_id);
END;

CREATE TRIGGER mooring_epoch_crew_members_update_old BEFORE UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM crews WHERE id=OLD.crew_id);
END;

CREATE TRIGGER mooring_epoch_crew_members_update_new AFTER UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM crews WHERE id=NEW.crew_id);
END;

CREATE TRIGGER mooring_epoch_crews_insert AFTER INSERT ON crews
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_crews_delete BEFORE DELETE ON crews
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_crews_update_old BEFORE UPDATE OF id,workspace_id,deleted_at ON crews
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_crews_update_new AFTER UPDATE OF id,workspace_id,deleted_at ON crews
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_members_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id;
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=NEW.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_delete BEFORE DELETE ON workspace_members
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=OLD.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_update_old BEFORE UPDATE OF id,workspace_id,user_id,role,capabilities,access_mode,access_revision ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode OR OLD.access_revision IS NOT NEW.access_revision
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=OLD.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_members_update_new AFTER UPDATE OF id,workspace_id,user_id,role,capabilities,access_mode,access_revision ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.capabilities IS NOT NEW.capabilities OR OLD.access_mode IS NOT NEW.access_mode OR OLD.access_revision IS NOT NEW.access_revision
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id;
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id OR id IN (SELECT workspace_id FROM workspace_members WHERE user_id=NEW.user_id);
END;

CREATE TRIGGER mooring_epoch_workspace_delete BEFORE DELETE ON workspaces
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.id;
END;

CREATE TRIGGER mooring_epoch_workspace_deleted_state AFTER UPDATE OF deleted_at ON workspaces
WHEN OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.id;
END;

CREATE TRIGGER mooring_epoch_user_status AFTER UPDATE OF suspended_at,access_revoked_at ON users
WHEN OLD.suspended_at IS NOT NEW.suspended_at OR OLD.access_revoked_at IS NOT NEW.access_revoked_at
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.id;
END;

CREATE TRIGGER mooring_epoch_user_delete BEFORE DELETE ON users
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.id;
END;

CREATE TRIGGER mooring_epoch_user_sessions_delete BEFORE DELETE ON user_sessions
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
END;

CREATE TRIGGER mooring_epoch_user_sessions_update_old BEFORE UPDATE OF id,user_id,expires_at,revoked_at ON user_sessions
WHEN OLD.id IS NOT NEW.id OR OLD.user_id IS NOT NEW.user_id OR OLD.expires_at IS NOT NEW.expires_at OR OLD.revoked_at IS NOT NEW.revoked_at
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
END;

CREATE TRIGGER mooring_epoch_user_sessions_update_new AFTER UPDATE OF user_id ON user_sessions
WHEN OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id;
END;

CREATE TRIGGER mooring_epoch_cli_tokens_delete BEFORE DELETE ON cli_tokens
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
END;

CREATE TRIGGER mooring_epoch_cli_tokens_update_old BEFORE UPDATE OF id,user_id,token_hash,tier,expires_at,revoked_at,scopes ON cli_tokens
WHEN OLD.id IS NOT NEW.id OR OLD.user_id IS NOT NEW.user_id OR OLD.token_hash IS NOT NEW.token_hash OR OLD.tier IS NOT NEW.tier OR OLD.expires_at IS NOT NEW.expires_at OR OLD.revoked_at IS NOT NEW.revoked_at OR OLD.scopes IS NOT NEW.scopes
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=OLD.user_id;
END;

CREATE TRIGGER mooring_epoch_cli_tokens_update_new AFTER UPDATE OF user_id ON cli_tokens
WHEN OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE users SET authority_revision=authority_revision+1 WHERE id=NEW.user_id;
END;

-- A hybrid attachment/producer change cannot leave a previously admitted
-- collaboration page reachable under its former whole-document authority.

CREATE TRIGGER mooring_epoch_page_panels_insert AFTER INSERT ON page_panels
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_page_panels_delete BEFORE DELETE ON page_panels
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_panels_update_old BEFORE UPDATE OF id,page_id,owner_crew_id,producer_kind,producer_ref ON page_panels
WHEN OLD.id IS NOT NEW.id OR OLD.page_id IS NOT NEW.page_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.producer_kind IS NOT NEW.producer_kind OR OLD.producer_ref IS NOT NEW.producer_ref
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_epoch_page_panels_update_new AFTER UPDATE OF id,page_id,owner_crew_id,producer_kind,producer_ref ON page_panels
WHEN OLD.id IS NOT NEW.id OR OLD.page_id IS NOT NEW.page_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.producer_kind IS NOT NEW.producer_kind OR OLD.producer_ref IS NOT NEW.producer_ref
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=(SELECT workspace_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_policy_insert AFTER INSERT ON workspace_artifact_creation_policy
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_policy_delete BEFORE DELETE ON workspace_artifact_creation_policy
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_policy_update_old BEFORE UPDATE OF workspace_id,enabled,version ON workspace_artifact_creation_policy
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.enabled IS NOT NEW.enabled OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_policy_update_new AFTER UPDATE OF workspace_id,enabled,version ON workspace_artifact_creation_policy
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.enabled IS NOT NEW.enabled OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_crews_insert AFTER INSERT ON workspace_artifact_creation_crews
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_crews_delete BEFORE DELETE ON workspace_artifact_creation_crews
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_crews_update_old BEFORE UPDATE OF workspace_id,crew_id ON workspace_artifact_creation_crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;

CREATE TRIGGER mooring_epoch_workspace_artifact_creation_crews_update_new AFTER UPDATE OF workspace_id,crew_id ON workspace_artifact_creation_crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;

-- The canonical grant reader deactivates non-owner-user write grants when
-- workbook presence changes. Watch identity/presence, never content revisions.
CREATE TRIGGER mooring_epoch_gridcraft_heads_insert AFTER INSERT ON gridcraft_heads
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;
CREATE TRIGGER mooring_epoch_gridcraft_heads_delete BEFORE DELETE ON gridcraft_heads
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;
CREATE TRIGGER mooring_epoch_gridcraft_heads_update_old BEFORE UPDATE OF page_id,workspace_id ON gridcraft_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=OLD.workspace_id;
END;
CREATE TRIGGER mooring_epoch_gridcraft_heads_update_new AFTER UPDATE OF page_id,workspace_id ON gridcraft_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id;
END;
