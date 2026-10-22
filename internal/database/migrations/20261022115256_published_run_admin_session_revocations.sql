-- Published Guest runs stop when an administrator revokes the user's sessions
-- after acceptance. Ordinary logout is not a stop reason, so only the admin
-- revocation reasons are indexed for the per-step authority check.
CREATE INDEX IF NOT EXISTS idx_user_sessions_admin_revoked ON user_sessions(user_id,revoked_at)
    WHERE revoked_reason IN ('admin_revoke','admin_invalidate');
