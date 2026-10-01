-- Explicit host-only signed identity proof; this confers neither model
-- entitlement, SIWC enrollment, nor a subscription spending guarantee.
CREATE TABLE codex_login_proofs (
 credential_id TEXT PRIMARY KEY REFERENCES credentials(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL CHECK(generation > 0),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 issuer TEXT NOT NULL, client_id TEXT NOT NULL, subject TEXT NOT NULL,
 account_id TEXT NOT NULL, user_id TEXT NOT NULL, plan TEXT NOT NULL,
 access_cipher_hash TEXT NOT NULL, refresh_cipher_hash TEXT NOT NULL,
 id_cipher_hash TEXT NOT NULL,
 access_expires_at TEXT NOT NULL, id_expires_at TEXT NOT NULL,
 verified_at TEXT NOT NULL
);
CREATE TRIGGER codex_login_proof_update AFTER UPDATE ON codex_login_proofs BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE id IN (
  WITH RECURSIVE compromised(id) AS (
   SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=OLD.credential_id
   UNION SELECT child.id FROM access_attempts child JOIN compromised parent ON child.parent_id=parent.id
  ) SELECT id FROM compromised
 );
END;
CREATE TRIGGER codex_login_proof_delete BEFORE DELETE ON codex_login_proofs BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE id IN (
  WITH RECURSIVE compromised(id) AS (
   SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=OLD.credential_id
   UNION SELECT child.id FROM access_attempts child JOIN compromised parent ON child.parent_id=parent.id
  ) SELECT id FROM compromised
 );
END;
CREATE TRIGGER codex_login_proof_insert AFTER INSERT ON codex_login_proofs BEGIN
 UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 WHERE id IN (
  WITH RECURSIVE compromised(id) AS (
   SELECT attempt_id FROM restricted_provider_bindings WHERE credential_id=NEW.credential_id
   UNION SELECT child.id FROM access_attempts child JOIN compromised parent ON child.parent_id=parent.id
  ) SELECT id FROM compromised
 );
END;
