-- Per-workspace retention windows for the two audit tables that had none.
--
-- `pipeline_runs` (v158), `inbox_items` and `journal_entries` (compaction) all
-- get pruned. `credential_audit` and `audit_logs` never did — nothing in the
-- tree deletes from either. They gain a row per credential read and per
-- entity mutation respectively, so on a long-lived instance they grow without
-- bound and never shrink. On a dev box with almost no real use,
-- `credential_audit` was already the second-largest table in the database.
--
-- Same shape as workspaces.run_retention_days: a nullable per-workspace
-- override, with the default applied in Go when the column is NULL or <= 0.
--
-- THE TWO DEFAULTS ARE DELIBERATELY DIFFERENT, and the difference is the
-- whole design:
--
--   credential_audit → 90 days by default (DefaultCredentialAuditRetentionDays,
--     matching DefaultRunRetentionDays). It is operational telemetry: who used
--     which credential, when, from which IP. The security-relevant summary an
--     operator actually reads — last_used_at and the last_used_ips ring — is
--     denormalised onto `credentials` itself and is NOT affected by this, so a
--     90-day timeline loses detail, not the answer to "is this credential
--     still in use".
--
--   audit_logs → UNLIMITED by default (DefaultAuditLogRetentionDays = 0,
--     meaning never delete). This is the compliance trail.
--     docs/security/gdpr.mdx states that audit records "have to survive
--     operator's own retention obligations", and Crewship is self-hosted: the
--     operator knows what their legal retention duty is and we do not.
--     Deleting compliance records by default would be a footgun that only
--     shows up at an audit. The mechanism ships; the decision to use it stays
--     with the operator, who sets the column.
--
-- Both are nullable INTEGERs rather than NOT NULL DEFAULT n, and NULL and 0
-- mean different things:
--
--   NULL → no opinion recorded; use the product default (and the default can
--          move later without rewriting every row).
--   0    → the operator's explicit "keep forever".
--   n>0  → keep n days.
--
-- That 0 is NOT a synonym for NULL diverges from workspaces.run_retention_days,
-- where "NULL or <= 0" both fall back to the default. Fine for pipeline runs,
-- which nobody has a legal duty to retain — but these are audit tables, and
-- "keep this forever" is a retention decision an operator has to be able to
-- express. Collapsing 0 into the default would make credential_audit pruning
-- impossible to switch off, which is not a choice to make on someone else's
-- behalf on a self-hosted product.

-- Existing workspaces are pinned to "keep forever" by the companion migration
-- 20260811082000_audit_retention_pin_existing_workspaces. Separate file for
-- the same reason the credential_audit pair is split: ADD COLUMN cannot be
-- re-applied (SQLite has no ADD COLUMN IF NOT EXISTS) while a backfill both
-- can and must be, so keeping them apart is what makes the backfill testable
-- and safe for a restore whose ledger was rolled back.

ALTER TABLE workspaces
    ADD COLUMN credential_audit_retention_days INTEGER;

ALTER TABLE workspaces
    ADD COLUMN audit_log_retention_days INTEGER;
