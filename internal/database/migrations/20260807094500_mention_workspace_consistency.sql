-- mission_comment_mentions: prove the four ids belong together, not merely
-- that each one exists.
--
-- The table already carries an FK per column, so every id resolves. Nothing
-- proved they resolved to the SAME tenant: a row could name a comment on one
-- workspace's issue, a mission on another's, and an agent from a third, and
-- every FK would be satisfied. The writer (mentionRecorder.record) derives all
-- four from one mentionContext, so today they always agree — but that is a
-- guarantee held by one function, and this whole change exists because
-- guarantees held by one place turn out not to be held once a second caller
-- appears. A mention is the trigger that starts an agent run; the row that
-- records it should not be the weakest statement in the chain.
--
-- Three checks, chained so the whole shape is pinned by comparing against
-- NEW.workspace_id:
--
--   1. the comment belongs to the mission the row names — this is the link
--      that makes "which issue was this mention on" answerable;
--   2. the mission belongs to the row's workspace;
--   3. the mentioned agent belongs to the row's workspace — the one an
--      unscoped resolve would have broken, and the reason a mention of a
--      foreign-workspace agent has to be a probe rather than a row.
--
-- `IS NOT` rather than `<>` throughout: a missing parent yields NULL, and
-- `NULL <> x` is NULL, which does not fire RAISE. `IS NOT` compares NULL
-- honestly, so a dangling id aborts instead of passing quietly. Same reasoning
-- and same shape as trg_credential_bindings_workspace_check (v-20260728135240),
-- which is the precedent in this repo for exactly this class of invariant.
--
-- Shaped as BEFORE INSERT and BEFORE UPDATE. UPDATE matters as much as INSERT
-- here: dispatch_state is written after the row exists, and an UPDATE that also
-- moved workspace_id would otherwise be unchecked.

CREATE TRIGGER IF NOT EXISTS trg_mission_comment_mentions_consistency_ins
BEFORE INSERT ON mission_comment_mentions
BEGIN
    SELECT RAISE(ABORT, 'mention comment must belong to the mention mission')
    WHERE (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id) IS NOT NEW.mission_id;

    SELECT RAISE(ABORT, 'mention mission must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'mentioned agent must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_mission_comment_mentions_consistency_upd
BEFORE UPDATE ON mission_comment_mentions
BEGIN
    SELECT RAISE(ABORT, 'mention comment must belong to the mention mission')
    WHERE (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id) IS NOT NEW.mission_id;

    SELECT RAISE(ABORT, 'mention mission must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'mentioned agent must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;
