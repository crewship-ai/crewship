-- Seeds the "issue_agent_sessions" feature flag (§16.1's integration
-- checklist: "Seed the feature flag... IsEnabled silently returns false
-- everywhere" otherwise).
--
-- Gates exactly one thing in B1: whether DispatchMention resolves-or-creates
-- an issue_agent_sessions row and stamps assignments.session_id
-- (internal/api/issue_mentions.go). The mission_activity widening and its
-- seq allocation are NOT behind this flag — every producer moves onto the
-- shared emitter unconditionally, because a row missing seq is a correctness
-- bug (§9.1), not a feature to roll out gradually.
--
-- Session creation is the one part of B1 that touches a live write path
-- (DispatchMention, on the hot path of every @mention), so it gets the
-- kill switch: enabled at 100% by default because there is no known reason
-- to withhold it and every accept-line scenario needs it running, but an
-- operator who finds session bookkeeping misbehaving can flip it off without
-- a deploy while the write path it guards is new.
INSERT OR IGNORE INTO feature_flags (id, key, description, enabled, percentage)
VALUES ('ffl_issue_agent_sessions', 'issue_agent_sessions',
        'Resolve-or-create an issue_agent_sessions row on @mention dispatch (#2332)', 1, 100);
