-- Private server execution payloads. Neither handles nor credentials belong here.
-- Workspace bundles must not restore executable attempts.
CREATE TABLE restricted_launches (
    attempt_id TEXT PRIMARY KEY REFERENCES access_attempts(id) ON DELETE CASCADE,
    command_json TEXT NOT NULL CHECK (json_valid(command_json) AND length(command_json) <= 131072)
);
CREATE TRIGGER restricted_launch_immutable BEFORE UPDATE ON restricted_launches BEGIN
    SELECT RAISE(ABORT, 'restricted launch is immutable');
END;
