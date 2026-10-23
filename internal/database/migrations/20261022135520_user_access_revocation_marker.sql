-- Durable marker written by every administrator session revocation, in the
-- same transaction. Guest-published runs accepted before it stop at their
-- next step even when the user had already logged out (no active session row
-- to revoke). Ordinary logout never writes it.
ALTER TABLE users ADD COLUMN access_revoked_at TEXT;
