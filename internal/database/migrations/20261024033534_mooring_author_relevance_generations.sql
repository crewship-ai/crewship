-- Author-relevant non-ABA generations, distinct from broad delivery epochs.
-- No ACL values or inferred permissions are stored: canonical readers remain
-- mandatory. Original admissions capture these generations once, never refresh.
-- Tombstones intentionally have no cascading foreign keys and never disappear
-- during logical workspace replacement. Physical restore requires host reset.
CREATE TABLE mooring_authority_generations (
 workspace_id TEXT NOT NULL,
 scope_kind TEXT NOT NULL CHECK(scope_kind IN ('user','agent','page','app')),
 scope_id TEXT NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1 CHECK(typeof(revision)='integer' AND revision>0),
 PRIMARY KEY(workspace_id,scope_kind,scope_id)
);
-- Baseline rows are installed ONLY by a fresh authenticated same-writer host
-- admission after canonical checks. Revalidation/old proofs never create rows.
-- An absent generation under a captured proof is an uncertain failure.
CREATE TRIGGER mooring_generation_key_immutable BEFORE UPDATE OF workspace_id,scope_kind,scope_id ON mooring_authority_generations
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.scope_kind IS NOT NEW.scope_kind OR OLD.scope_id IS NOT NEW.scope_id
BEGIN SELECT RAISE(ABORT,'author generation identity is immutable'); END;
CREATE TRIGGER mooring_generation_no_delete BEFORE DELETE ON mooring_authority_generations
BEGIN SELECT RAISE(ABORT,'author generation tombstones cannot be deleted'); END;
CREATE TRIGGER mooring_generation_no_replace BEFORE INSERT ON mooring_authority_generations
WHEN EXISTS(SELECT 1 FROM mooring_authority_generations WHERE workspace_id=NEW.workspace_id AND scope_kind=NEW.scope_kind AND scope_id=NEW.scope_id)
BEGIN SELECT RAISE(ABORT,'author generation identity already exists'); END;
CREATE TRIGGER mooring_generation_monotonic BEFORE UPDATE OF revision ON mooring_authority_generations
WHEN NEW.revision<OLD.revision
BEGIN SELECT RAISE(ABORT,'author generation cannot decrease'); END;
CREATE TRIGGER mooring_generation_delivery_close AFTER UPDATE OF revision ON mooring_authority_generations
WHEN NEW.revision IS NOT OLD.revision
BEGIN UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id=NEW.workspace_id; END;
-- Recipient expansion is vocabulary matching only, not live-grant evaluation.
-- Inert/potential grants must also retain ABA history across issuer restoration.
CREATE VIEW mooring_generation_grant_recipients(workspace_id,page_id,subject_type,subject_id,level,issuer,principal_kind,principal_id) AS
 SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='user'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'agent',g.subject_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id WHERE g.subject_type='agent'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',cm.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN crews c ON c.id=g.subject_id AND c.workspace_id=p.workspace_id JOIN crew_members cm ON cm.crew_id=c.id WHERE g.subject_type='crew'
 UNION SELECT p.workspace_id,g.page_id,g.subject_type,g.subject_id,g.level,g.granted_by_user_id,'user',m.user_id
 FROM page_grants g JOIN pages p ON p.id=g.page_id JOIN workspace_members m ON m.workspace_id=p.workspace_id WHERE g.subject_type='workspace';
CREATE VIEW mooring_generation_folder_recipients(workspace_id,folder_id,subject_type,subject_id,principal_kind,principal_id) AS
 SELECT f.workspace_id,a.folder_id,a.subject_type,a.subject_id,'user',a.subject_id FROM page_folder_acl a JOIN page_folders f ON f.id=a.folder_id WHERE a.subject_type='user'
 UNION SELECT f.workspace_id,a.folder_id,a.subject_type,a.subject_id,'user',cm.user_id FROM page_folder_acl a JOIN page_folders f ON f.id=a.folder_id JOIN crews c ON c.id=a.subject_id AND c.workspace_id=f.workspace_id JOIN crew_members cm ON cm.crew_id=c.id WHERE a.subject_type='crew'
 UNION SELECT f.workspace_id,a.folder_id,a.subject_type,a.subject_id,'user',m.user_id FROM page_folder_acl a JOIN page_folders f ON f.id=a.folder_id JOIN workspace_members m ON m.workspace_id=f.workspace_id WHERE a.subject_type='workspace';

-- Direct grant and folder ACL mutations touch recipients, not every author.
-- Folder setter/set_at are audit metadata and intentionally not dependencies.

CREATE TRIGGER mooring_relevant_grant_insert AFTER INSERT ON page_grants
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_grant_delete BEFORE DELETE ON page_grants
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_old BEFORE UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=OLD.page_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id AND level=OLD.level);
END;

CREATE TRIGGER mooring_relevant_grant_new AFTER UPDATE OF page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id ON page_grants
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.level IS NOT NEW.level OR OLD.panel_ids IS NOT NEW.panel_ids OR OLD.granted_by_user_id IS NOT NEW.granted_by_user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE page_id=NEW.page_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id AND level=NEW.level);
END;

CREATE TRIGGER mooring_relevant_folder_acl_insert AFTER INSERT ON page_folder_acl
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_folder_recipients WHERE folder_id=NEW.folder_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id);
END;

CREATE TRIGGER mooring_relevant_folder_acl_delete BEFORE DELETE ON page_folder_acl
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_folder_recipients WHERE folder_id=OLD.folder_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id);
END;

CREATE TRIGGER mooring_relevant_folder_acl_old BEFORE UPDATE OF folder_id,subject_type,subject_id,can_read,can_write ON page_folder_acl
WHEN OLD.folder_id IS NOT NEW.folder_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.can_read IS NOT NEW.can_read OR OLD.can_write IS NOT NEW.can_write
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_folder_recipients WHERE folder_id=OLD.folder_id AND subject_type=OLD.subject_type AND subject_id=OLD.subject_id);
END;

CREATE TRIGGER mooring_relevant_folder_acl_new AFTER UPDATE OF folder_id,subject_type,subject_id,can_read,can_write ON page_folder_acl
WHEN OLD.folder_id IS NOT NEW.folder_id OR OLD.subject_type IS NOT NEW.subject_type OR OLD.subject_id IS NOT NEW.subject_id OR OLD.can_read IS NOT NEW.can_read OR OLD.can_write IS NOT NEW.can_write
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_folder_recipients WHERE folder_id=NEW.folder_id AND subject_type=NEW.subject_type AND subject_id=NEW.subject_id);
END;

-- Issuer standing can change in another workspace (global restricted ceiling).
-- OLD fanout occurs before cascades destroy the source joins.

CREATE TRIGGER mooring_relevant_workspace_member_insert AFTER INSERT ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_delete BEFORE DELETE ON workspace_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_old BEFORE UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM workspace_members WHERE user_id=OLD.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_workspace_member_new AFTER UPDATE OF id,workspace_id,user_id,role,access_mode ON workspace_members
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.user_id IS NOT NEW.user_id OR OLD.role IS NOT NEW.role OR OLD.access_mode IS NOT NEW.access_mode
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM workspace_members WHERE user_id=NEW.user_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.user_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_insert AFTER INSERT ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_delete BEFORE DELETE ON crew_members
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_old BEFORE UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.user_id AS scope_id FROM crews WHERE id=OLD.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.user_id);
END;

CREATE TRIGGER mooring_relevant_crew_member_new AFTER UPDATE OF id,crew_id,user_id ON crew_members
WHEN OLD.id IS NOT NEW.id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.user_id IS NOT NEW.user_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.user_id AS scope_id FROM crews WHERE id=NEW.crew_id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.user_id);
END;

CREATE TRIGGER mooring_relevant_user_delete BEFORE DELETE ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_status AFTER UPDATE OF suspended_at ON users
WHEN OLD.suspended_at IS NOT NEW.suspended_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_insert AFTER INSERT ON users
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_old BEFORE UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,OLD.id AS scope_id FROM workspace_members WHERE user_id=OLD.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=OLD.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=OLD.id);
END;

CREATE TRIGGER mooring_relevant_user_identity_new AFTER UPDATE OF id ON users
WHEN OLD.id IS NOT NEW.id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'user' AS scope_kind,NEW.id AS scope_id FROM workspace_members WHERE user_id=NEW.id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE scope_kind='user' AND scope_id=NEW.id UNION SELECT workspace_id,principal_kind AS scope_kind,principal_id AS scope_id FROM mooring_generation_grant_recipients WHERE issuer=NEW.id);
END;

-- Target policy and applicable app policy are separate from recipient ACLs.

CREATE TRIGGER mooring_relevant_page_insert AFTER INSERT ON pages
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'page' AS scope_kind,NEW.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_page_delete BEFORE DELETE ON pages
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'page' AS scope_kind,OLD.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_page_old BEFORE UPDATE OF id,workspace_id,owner_user_id,owner_crew_id,private_audience,folder_id ON pages
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.private_audience IS NOT NEW.private_audience OR OLD.folder_id IS NOT NEW.folder_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'page' AS scope_kind,OLD.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_page_new AFTER UPDATE OF id,workspace_id,owner_user_id,owner_crew_id,private_audience,folder_id ON pages
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_user_id IS NOT NEW.owner_user_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.private_audience IS NOT NEW.private_audience OR OLD.folder_id IS NOT NEW.folder_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'page' AS scope_kind,NEW.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_folder_insert AFTER INSERT ON page_folders
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE folder_id=NEW.id);
END;

CREATE TRIGGER mooring_relevant_folder_delete BEFORE DELETE ON page_folders
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE folder_id=OLD.id);
END;

CREATE TRIGGER mooring_relevant_folder_old BEFORE UPDATE OF id,workspace_id,owner_crew_id ON page_folders
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE folder_id=OLD.id);
END;

CREATE TRIGGER mooring_relevant_folder_new AFTER UPDATE OF id,workspace_id,owner_crew_id ON page_folders
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE folder_id=NEW.id);
END;

CREATE TRIGGER mooring_relevant_crew_insert AFTER INSERT ON crews
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'app' AS scope_kind,NEW.id AS scope_id  UNION SELECT NEW.workspace_id AS workspace_id,'agent' AS scope_kind,id AS scope_id FROM agents WHERE crew_id=NEW.id UNION SELECT NEW.workspace_id AS workspace_id,'user' AS scope_kind,user_id AS scope_id FROM crew_members WHERE crew_id=NEW.id UNION SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE owner_crew_id=NEW.id OR folder_id IN (SELECT id FROM page_folders WHERE owner_crew_id=NEW.id));
END;

CREATE TRIGGER mooring_relevant_crew_delete BEFORE DELETE ON crews
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'app' AS scope_kind,OLD.id AS scope_id  UNION SELECT OLD.workspace_id AS workspace_id,'agent' AS scope_kind,id AS scope_id FROM agents WHERE crew_id=OLD.id UNION SELECT OLD.workspace_id AS workspace_id,'user' AS scope_kind,user_id AS scope_id FROM crew_members WHERE crew_id=OLD.id UNION SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE owner_crew_id=OLD.id OR folder_id IN (SELECT id FROM page_folders WHERE owner_crew_id=OLD.id));
END;

CREATE TRIGGER mooring_relevant_crew_old BEFORE UPDATE OF id,workspace_id,deleted_at ON crews
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'app' AS scope_kind,OLD.id AS scope_id  UNION SELECT OLD.workspace_id AS workspace_id,'agent' AS scope_kind,id AS scope_id FROM agents WHERE crew_id=OLD.id UNION SELECT OLD.workspace_id AS workspace_id,'user' AS scope_kind,user_id AS scope_id FROM crew_members WHERE crew_id=OLD.id UNION SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE owner_crew_id=OLD.id OR folder_id IN (SELECT id FROM page_folders WHERE owner_crew_id=OLD.id));
END;

CREATE TRIGGER mooring_relevant_crew_new AFTER UPDATE OF id,workspace_id,deleted_at ON crews
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'app' AS scope_kind,NEW.id AS scope_id  UNION SELECT NEW.workspace_id AS workspace_id,'agent' AS scope_kind,id AS scope_id FROM agents WHERE crew_id=NEW.id UNION SELECT NEW.workspace_id AS workspace_id,'user' AS scope_kind,user_id AS scope_id FROM crew_members WHERE crew_id=NEW.id UNION SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE owner_crew_id=NEW.id OR folder_id IN (SELECT id FROM page_folders WHERE owner_crew_id=NEW.id));
END;

CREATE TRIGGER mooring_relevant_agent_insert AFTER INSERT ON agents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'agent' AS scope_kind,NEW.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_agent_delete BEFORE DELETE ON agents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'agent' AS scope_kind,OLD.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_agent_old BEFORE UPDATE OF id,workspace_id,crew_id,deleted_at,expired_at ON agents
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.expired_at IS NOT NEW.expired_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'agent' AS scope_kind,OLD.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_agent_new AFTER UPDATE OF id,workspace_id,crew_id,deleted_at,expired_at ON agents
WHEN OLD.id IS NOT NEW.id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id OR OLD.deleted_at IS NOT NEW.deleted_at OR OLD.expired_at IS NOT NEW.expired_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'agent' AS scope_kind,NEW.id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_app_policy_insert AFTER INSERT ON workspace_artifact_creation_policy
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'app' AS scope_kind,id AS scope_id FROM crews WHERE workspace_id=NEW.workspace_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=NEW.workspace_id AND scope_kind='app');
END;

CREATE TRIGGER mooring_relevant_app_policy_delete BEFORE DELETE ON workspace_artifact_creation_policy
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'app' AS scope_kind,id AS scope_id FROM crews WHERE workspace_id=OLD.workspace_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=OLD.workspace_id AND scope_kind='app');
END;

CREATE TRIGGER mooring_relevant_app_policy_old BEFORE UPDATE OF workspace_id,enabled,version ON workspace_artifact_creation_policy
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.enabled IS NOT NEW.enabled OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'app' AS scope_kind,id AS scope_id FROM crews WHERE workspace_id=OLD.workspace_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=OLD.workspace_id AND scope_kind='app');
END;

CREATE TRIGGER mooring_relevant_app_policy_new AFTER UPDATE OF workspace_id,enabled,version ON workspace_artifact_creation_policy
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.enabled IS NOT NEW.enabled OR OLD.version IS NOT NEW.version
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'app' AS scope_kind,id AS scope_id FROM crews WHERE workspace_id=NEW.workspace_id UNION SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=NEW.workspace_id AND scope_kind='app');
END;

CREATE TRIGGER mooring_relevant_app_crew_insert AFTER INSERT ON workspace_artifact_creation_crews
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'app' AS scope_kind,NEW.crew_id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_app_crew_delete BEFORE DELETE ON workspace_artifact_creation_crews
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'app' AS scope_kind,OLD.crew_id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_app_crew_old BEFORE UPDATE OF workspace_id,crew_id ON workspace_artifact_creation_crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT OLD.workspace_id AS workspace_id,'app' AS scope_kind,OLD.crew_id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_app_crew_new AFTER UPDATE OF workspace_id,crew_id ON workspace_artifact_creation_crews
WHEN OLD.workspace_id IS NOT NEW.workspace_id OR OLD.crew_id IS NOT NEW.crew_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT NEW.workspace_id AS workspace_id,'app' AS scope_kind,NEW.crew_id AS scope_id );
END;

CREATE TRIGGER mooring_relevant_publication_insert AFTER INSERT ON page_project_live
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_publication_delete BEFORE DELETE ON page_project_live
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_publication_old BEFORE UPDATE OF page_id,version,published ON page_project_live
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.version IS NOT NEW.version OR OLD.published IS NOT NEW.published
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_publication_new AFTER UPDATE OF page_id,version,published ON page_project_live
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.version IS NOT NEW.version OR OLD.published IS NOT NEW.published
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_panels_insert AFTER INSERT ON page_panels
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_panels_delete BEFORE DELETE ON page_panels
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_panels_old BEFORE UPDATE OF id,page_id,owner_crew_id,producer_kind,producer_ref ON page_panels
WHEN OLD.id IS NOT NEW.id OR OLD.page_id IS NOT NEW.page_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.producer_kind IS NOT NEW.producer_kind OR OLD.producer_ref IS NOT NEW.producer_ref
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_panels_new AFTER UPDATE OF id,page_id,owner_crew_id,producer_kind,producer_ref ON page_panels
WHEN OLD.id IS NOT NEW.id OR OLD.page_id IS NOT NEW.page_id OR OLD.owner_crew_id IS NOT NEW.owner_crew_id OR OLD.producer_kind IS NOT NEW.producer_kind OR OLD.producer_ref IS NOT NEW.producer_ref
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_workspace_delete BEFORE DELETE ON workspaces
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=OLD.id);
END;

CREATE TRIGGER mooring_relevant_workspace_state AFTER UPDATE OF deleted_at ON workspaces
WHEN OLD.deleted_at IS NOT NEW.deleted_at
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id,scope_kind,scope_id FROM mooring_authority_generations WHERE workspace_id=NEW.id);
END;

-- Installed native-content presence and identity are target policy. Ordinary
-- checkpoint/head payload writes do not renew or revoke author permission.

CREATE TRIGGER mooring_relevant_collab_content_insert AFTER INSERT ON mooring_collab_documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_collab_content_delete BEFORE DELETE ON mooring_collab_documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_collab_content_old BEFORE UPDATE OF page_id,workspace_id,application_id ON mooring_collab_documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.application_id IS NOT NEW.application_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_collab_content_new AFTER UPDATE OF page_id,workspace_id,application_id ON mooring_collab_documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.application_id IS NOT NEW.application_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_document_content_insert AFTER INSERT ON documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_document_content_delete BEFORE DELETE ON documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_document_content_old BEFORE UPDATE OF page_id,workspace_id ON documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_document_content_new AFTER UPDATE OF page_id,workspace_id ON documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_slides_content_insert AFTER INSERT ON slides_heads
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_slides_content_delete BEFORE DELETE ON slides_heads
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_slides_content_old BEFORE UPDATE OF page_id,workspace_id ON slides_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_slides_content_new AFTER UPDATE OF page_id,workspace_id ON slides_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_gridcraft_content_insert AFTER INSERT ON gridcraft_heads
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_gridcraft_content_delete BEFORE DELETE ON gridcraft_heads
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_gridcraft_content_old BEFORE UPDATE OF page_id,workspace_id ON gridcraft_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_gridcraft_content_new AFTER UPDATE OF page_id,workspace_id ON gridcraft_heads
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_generic_content_insert AFTER INSERT ON mooring_documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_generic_content_delete BEFORE DELETE ON mooring_documents
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_generic_content_old BEFORE UPDATE OF page_id,workspace_id,app_id ON mooring_documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.app_id IS NOT NEW.app_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_generic_content_new AFTER UPDATE OF page_id,workspace_id,app_id ON mooring_documents
WHEN OLD.page_id IS NOT NEW.page_id OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.app_id IS NOT NEW.app_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_project_draft_insert AFTER INSERT ON page_project_drafts
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;

CREATE TRIGGER mooring_relevant_project_draft_delete BEFORE DELETE ON page_project_drafts
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_project_draft_old BEFORE UPDATE OF page_id ON page_project_drafts
WHEN OLD.page_id IS NOT NEW.page_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=OLD.page_id);
END;

CREATE TRIGGER mooring_relevant_project_draft_new AFTER UPDATE OF page_id ON page_project_drafts
WHEN OLD.page_id IS NOT NEW.page_id
BEGIN
 UPDATE mooring_authority_generations SET revision=revision+1 WHERE (workspace_id,scope_kind,scope_id) IN (SELECT workspace_id AS workspace_id,'page' AS scope_kind,id AS scope_id FROM pages WHERE id=NEW.page_id);
END;
