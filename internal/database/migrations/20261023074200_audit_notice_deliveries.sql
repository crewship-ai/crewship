-- Delivery receipts outlive inbox retention/purge. They contain no notification
-- body, email, credentials or provider output. Deleting a user removes receipts.
CREATE TABLE instance_audit_notice_deliveries (
    incident_key TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    delivered_at TEXT NOT NULL,
    PRIMARY KEY (incident_key, user_id)
);
