-- Per-workspace retention window for terminal approvals_queue rows (#2233).
--
-- Nothing ever deleted from approvals_queue before this: every terminal
-- transition (approve/deny, cancel, the timeout sweeper, the sync-gate
-- timeout) was an in-place UPDATE of status/decided_*, so the table only
-- ever grew. The sweep this column feeds is internal/harbormaster/retention.go.
--
-- Same shape as workspaces.run_retention_days (v158): a nullable INTEGER
-- override, with NULL or <= 0 meaning "use the product default"
-- (harbormaster.DefaultApprovalsRetentionDays = 90), and no separate
-- "0 = keep forever" sentinel. That is a deliberate divergence from the
-- audit_retention_days pair (20260810170000_audit_retention_windows.sql):
-- those tables are the compliance trail and an operator's retention
-- obligation must be expressible as "never delete". approvals_queue is not
-- that record — every terminal decision it holds is already durably
-- captured in journal_entries via AfterDecide, so there is no operator
-- intent a bare NULL-means-default can't already carry. See the retention.go
-- package comment for the full rationale.

ALTER TABLE workspaces
    ADD COLUMN approvals_retention_days INTEGER;
