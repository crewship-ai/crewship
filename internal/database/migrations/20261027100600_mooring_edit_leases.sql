-- Native single-editor ownership is separate from the runtime process owner.
-- Rows are retained on release so a delayed generation can never become current.
CREATE TABLE mooring_edit_leases (
 page_id TEXT PRIMARY KEY REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 actor_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 boot_id TEXT NOT NULL,
 expires_ns INTEGER NOT NULL CHECK(expires_ns>=0)
);
CREATE INDEX mooring_edit_leases_workspace ON mooring_edit_leases(workspace_id);
CREATE TABLE mooring_edit_lease_requests (
 page_id TEXT NOT NULL REFERENCES mooring_edit_leases(page_id) ON DELETE CASCADE,
 actor_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 PRIMARY KEY(page_id,actor_id,session_id)
);

-- A hard-deleted user cannot remain named by an expired lease. Retain the
-- fencing generation, and remove requests against that invalidated holder.
CREATE TRIGGER mooring_edit_leases_user_delete BEFORE DELETE ON users
BEGIN
 DELETE FROM mooring_edit_lease_requests
 WHERE actor_id IN (OLD.id,'user:'||OLD.id)
    OR page_id IN (SELECT page_id FROM mooring_edit_leases WHERE actor_id IN (OLD.id,'user:'||OLD.id));
 UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0
 WHERE actor_id IN (OLD.id,'user:'||OLD.id);
END;
