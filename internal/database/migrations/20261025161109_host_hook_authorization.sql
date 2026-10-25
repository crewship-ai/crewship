-- Historical workspace ownership is not proof of instance authorization.
ALTER TABLE hooks_config ADD COLUMN host_authorization TEXT NOT NULL DEFAULT '';
-- Fail the migration if a database trigger suppresses a required receipt.
CREATE TEMP TABLE host_hook_audit_receipt_guard (
    actual INTEGER NOT NULL,
    expected INTEGER NOT NULL,
    CHECK(actual = expected)
);
INSERT INTO audit_logs (id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
SELECT lower(hex(randomblob(16))),workspace_id,NULL,'hook.host_review_required','hook',id,
       json_object('reason','historical instance authority unverified','previously_enabled',enabled),
       strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM hooks_config WHERE handler_kind='shell';
INSERT INTO host_hook_audit_receipt_guard(actual,expected)
SELECT changes(), count(*) FROM hooks_config WHERE handler_kind='shell';
UPDATE hooks_config SET enabled=0 WHERE handler_kind='shell';
INSERT INTO host_hook_audit_receipt_guard(actual,expected)
SELECT changes(), expected FROM host_hook_audit_receipt_guard LIMIT 1;
DROP TABLE host_hook_audit_receipt_guard;
