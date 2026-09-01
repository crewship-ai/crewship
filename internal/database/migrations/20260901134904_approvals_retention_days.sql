-- Per-workspace retention window for terminal approvals_queue rows (#2233).
--
-- Nothing ever deleted from approvals_queue before this: every terminal
-- transition (approve/deny, cancel, the timeout sweeper, the sync-gate
-- timeout) was an in-place UPDATE of status/decided_*, so the table only
-- ever grew. The sweep this column feeds is internal/harbormaster/retention.go.
--
-- Same shape as the audit_retention_days pair
-- (20260810170000_audit_retention_windows.sql), NOT workspaces.run_retention_days:
-- a nullable INTEGER override where NULL means "use the product default"
-- (harbormaster.DefaultApprovalsRetentionDays = 90) and an explicit 0 means
-- "keep forever". This column sits on the same `workspace update` command as
-- credential_audit_retention_days / audit_log_retention_days, where 0
-- already means keep-forever — giving it different semantics here would be
-- a footgun (an operator setting 0 out of habit with its neighbours would
-- silently get 90-day deletion instead of the "never delete" they asked
-- for). It is also a real operator need on its own: AfterDecide's journal
-- entry does not carry `reason` or the full `payload`, only approval_id/
-- kind/comment, so approvals_queue is the only place those survive at all.
-- See the retention.go package comment for the full rationale.
--
-- Existing workspaces are pinned to "keep forever" by the companion
-- migration 20260901140000_approvals_retention_pin_existing_workspaces —
-- without it, the sweeper's immediate first sweep at boot would resolve
-- every pre-existing workspace's NULL to the 90-day default and delete
-- terminal approval history nobody asked to prune. See that file.

ALTER TABLE workspaces
    ADD COLUMN approvals_retention_days INTEGER;
