-- Populate credential_audit.workspace_id for rows written before the column
-- existed. Companion to 20260810153104_credential_audit_workspace_column,
-- which added it.
--
-- Separate file, deliberately. ADD COLUMN cannot be re-applied — SQLite has no
-- ADD COLUMN IF NOT EXISTS, so re-running that migration is an error — while a
-- backfill both can and must be re-runnable: a restore whose ledger was rolled
-- back re-applies it, and the second run has to be a no-op. Splitting them is
-- what lets the backfill be tested by clearing its ledger row and migrating
-- again, the same way 20260802155412_backfill_crew_container_sizes is tested.
--
-- `WHERE workspace_id IS NULL` is what makes it idempotent, and it is also
-- what makes it safe: a row whose workspace was already written — by the
-- writer, or by a previous run of this migration — is never rewritten. The
-- value is read from the credential the row already points at, so this cannot
-- invent a tenant for a row that has none.
--
-- Every row resolves. credential_id is NOT NULL and cascades on delete, so a
-- surviving audit row always has a live credential to read the workspace from.
-- If that ever stopped being true the sub-SELECT would write NULL rather than
-- fail, which is the honest outcome: a row we cannot attribute stays
-- unattributed and simply does not appear in a workspace-scoped view.

UPDATE credential_audit
   SET workspace_id = (
        SELECT cr.workspace_id
          FROM credentials cr
         WHERE cr.id = credential_audit.credential_id
   )
 WHERE workspace_id IS NULL;
