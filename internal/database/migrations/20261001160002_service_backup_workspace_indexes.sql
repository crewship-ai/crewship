-- Workspace deletion checks and admin maintenance status must not scan every
-- crew's backup fence or provider operation lease.
CREATE INDEX service_backup_fences_workspace ON service_backup_fences(workspace_id);
CREATE INDEX service_operation_leases_workspace ON service_operation_leases(workspace_id);
