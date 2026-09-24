-- Keeper approvals become consumable (#2574).
--
-- A human resolving an escalation as ALLOW used to change nothing the agent
-- could act on: the sidecar exposed no way to learn the outcome, and a retried
-- /keeper/request or /keeper/execute was judged afresh — where the tier floor
-- re-escalated every L4 read, so an approved production credential could never
-- actually execute. The fix lets the agent present the approved request id on
-- the retry; the server honours it exactly once.
--
-- Two columns make that safe:
--
--   resolved_by_user_id  WHO resolved the escalation. Non-NULL is the marker
--                         that a decision came off the human resolve surface —
--                         only such a row is ever consumable. A judge ALLOW
--                         (L1–L3, or any tier under --escalate-from=never)
--                         leaves it NULL, so a chain of agent retries can never
--                         mint self-renewing approvals out of the machine's own
--                         verdicts.
--   approval_consumed_at when the approval was spent. NULL on rows that are
--                         still consumable; set by the conditional UPDATE that
--                         hands the approval out (single use, race-safe), and
--                         set at INSERT time on the retry rows an approval
--                         creates, so a consumed approval's successor is born
--                         spent.
--
-- The validity window is resolved_by_user_id's sibling decided_at (written by
-- the same resolve transaction) plus a server-side TTL; it needs no column of
-- its own.

ALTER TABLE keeper_requests ADD COLUMN resolved_by_user_id TEXT;
ALTER TABLE keeper_requests ADD COLUMN approval_consumed_at TEXT;

-- The consumption UPDATE is by primary key; no additional index is needed.
