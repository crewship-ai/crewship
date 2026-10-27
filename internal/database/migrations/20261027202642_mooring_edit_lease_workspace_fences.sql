-- Page deletion clears its holder row, but must not erase evidence that a
-- target workspace issued an editor generation to a possibly live process.
-- No identity or capability is retained here. This target-local marker is not
-- portable; a fresh/fork restore acquires its own marker on its first lease.
CREATE TABLE mooring_edit_lease_workspaces (
 workspace_id TEXT PRIMARY KEY NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE
);

INSERT INTO mooring_edit_lease_workspaces(workspace_id)
 SELECT DISTINCT workspace_id FROM mooring_edit_leases;

CREATE TRIGGER mooring_edit_lease_workspace_issued AFTER INSERT ON mooring_edit_leases
BEGIN
 INSERT OR IGNORE INTO mooring_edit_lease_workspaces(workspace_id) VALUES(NEW.workspace_id);
END;
