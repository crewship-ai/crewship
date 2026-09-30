-- Every host provider service start, including legacy agent paths, participates
-- in maintenance admission before any attach/create/start operation.
CREATE TABLE service_operation_leases (
 token TEXT PRIMARY KEY CHECK(length(token)=32),
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 lease_until TEXT NOT NULL
);
CREATE INDEX service_operation_leases_crew ON service_operation_leases(crew_id,lease_until);
-- Maintenance stays durable indefinitely; this separate liveness lease only
-- prevents an explicit retry from stealing a still-running capture producer.
ALTER TABLE service_backup_fences ADD COLUMN producer_until TEXT NOT NULL DEFAULT '';
