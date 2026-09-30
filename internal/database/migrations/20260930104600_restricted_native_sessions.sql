CREATE TABLE restricted_native_sessions (
    attempt_id TEXT PRIMARY KEY REFERENCES access_attempts(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    context_revision INTEGER NOT NULL,
    instructions TEXT NOT NULL,
    initial_input TEXT NOT NULL,
    client_prefix_json TEXT NOT NULL DEFAULT '[]',
    history_json TEXT NOT NULL DEFAULT '[]',
    in_flight_lease TEXT,
    FOREIGN KEY(workspace_id) REFERENCES workspaces(id),
    FOREIGN KEY(principal_id) REFERENCES users(id)
);

CREATE TRIGGER restricted_native_sessions_immutable
BEFORE UPDATE OF attempt_id,workspace_id,principal_id,agent_id,scope_id,context_revision,instructions,initial_input
ON restricted_native_sessions
BEGIN SELECT RAISE(ABORT,'restricted native context is immutable'); END;

CREATE TRIGGER restricted_native_sessions_delete_revokes
BEFORE DELETE ON restricted_native_sessions
BEGIN
  UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) WHERE id=OLD.attempt_id;
END;
