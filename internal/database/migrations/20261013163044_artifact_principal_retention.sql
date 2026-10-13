-- Runtime admission cleanup is bounded by expiry; historical provenance has no
-- foreign key to these ephemeral principals.
CREATE INDEX idx_artifact_run_principals_expiry ON artifact_run_principals(expires_at);
