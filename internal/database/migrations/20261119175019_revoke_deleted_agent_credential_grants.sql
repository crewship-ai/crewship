-- Revoke credential grants left behind by agents deleted before #5529.
--
-- Agent, crew and workspace delete tombstone agents (agents.deleted_at); the
-- ON DELETE CASCADE on agent_credentials.agent_id never fires for a tombstone,
-- so every grant such an agent held survived it. Those rows kept counting in
-- `credential list`, and `credential unassign` could not remove them because a
-- tombstoned agent is "not found" everywhere. The delete paths now revoke the
-- grants in their own transaction; this repairs the rows written before that.
--
-- What it removes: agent_credentials rows whose agent is tombstoned, or no
-- longer exists at all. Nothing else. A live agent's grant never matches
-- (deleted_at IS NULL and the row exists), and the credential itself — row,
-- encrypted material, bindings, crew links, audit history — is not touched.
--
-- Every removed grant whose credential still exists gets a REVOKE row on that
-- credential's audit timeline first, the same event the delete paths now
-- write, with reason "agent_deleted_repair". A grant whose agent row is gone
-- records agent_id NULL (the column references agents) and the original id in
-- the metadata. A grant whose credential row is gone cannot be audited
-- (credential_audit.credential_id is NOT NULL and references credentials); it
-- grants nothing and is removed with the rest.
--
-- Idempotent: once the rows are gone both statements match nothing, so a
-- restore that re-applies this migration writes no second audit row.

INSERT INTO credential_audit
    (id, credential_id, workspace_id, event_type, agent_id, metadata_json, occurred_at)
SELECT 'c' || lower(hex(randomblob(12))),
       ac.credential_id,
       cr.workspace_id,
       'REVOKE',
       a.id,
       json_object(
           'reason', 'agent_deleted_repair',
           'grant_source', 'explicit',
           'assignment_id', ac.id,
           'agent_id', ac.agent_id,
           'env_var_name', ac.env_var_name,
           'expires_at', ac.expires_at,
           'lease_source', ac.lease_source,
           'agent_deleted_at', a.deleted_at),
       strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
  FROM agent_credentials ac
  JOIN credentials cr ON cr.id = ac.credential_id
  LEFT JOIN agents a ON a.id = ac.agent_id
 WHERE a.id IS NULL OR a.deleted_at IS NOT NULL;

DELETE FROM agent_credentials
 WHERE NOT EXISTS (SELECT 1 FROM agents a
                    WHERE a.id = agent_credentials.agent_id AND a.deleted_at IS NULL);
