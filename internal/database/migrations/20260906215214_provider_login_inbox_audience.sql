-- Provider-account metadata is admin-only, including persisted notifications
-- from development builds predating #2428's final RBAC review. A personal
-- target bypasses the inbox role predicate after a user is demoted, so clear
-- it rather than merely adding an ADMIN role. Preserve stricter OWNER rows.
UPDATE inbox_items
SET target_user_id = NULL,
    target_role = CASE WHEN target_role = 'OWNER' THEN 'OWNER' ELSE 'ADMIN' END,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind = 'message'
  AND sender_type = 'system'
  AND source_id GLOB 'provider-login-relogin:*';
