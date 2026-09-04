-- Seeds the "issue_deliveries" feature flag (§16.1's integration checklist:
-- "Seed the feature flag... IsEnabled silently returns false everywhere"
-- otherwise).
--
-- Gates the claim/consume path B2 adds to mentionRecorder.record
-- (internal/api/issue_deliveries.go, PRD-ISSUES-AND-ROUTINES-2026 §9.3,
-- #2337): creating the pending delivery row, broadcasting
-- issue.delivery.acked, and running the claim CAS before dispatch. It does
-- NOT gate mission_comment_mentions itself, which the mention path has
-- always written unconditionally — same split B1's issue_agent_sessions
-- flag makes for mission_activity's widening (that one is not behind a
-- flag either; a row missing seq is a correctness bug, not a feature to
-- roll out gradually).
--
-- Separate from issue_agent_sessions on purpose, matching F49's "pick one
-- canonical constant, do not repeat the existing duplication" read the
-- other way: two independent write paths (session bookkeeping, delivery
-- claim/ack) that happen to both live on the mention hot path get two
-- independent kill switches, so an operator who finds one misbehaving does
-- not have to disable the other to isolate it. Enabled at 100% by default
-- — B2's own accept line needs it running for every scenario it proves.
INSERT OR IGNORE INTO feature_flags (id, key, description, enabled, percentage)
VALUES ('ffl_issue_deliveries', 'issue_deliveries',
        'Claim/consume CAS and issue.delivery.acked on @mention dispatch (#2337)', 1, 100);
