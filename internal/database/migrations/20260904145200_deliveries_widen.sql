-- Generalise mission_comment_mentions into the delivery table (PRD-ISSUES-
-- AND-ROUTINES-2026 §9.3, work package B2, #2337). Do not create
-- agent_deliveries: this table is already 90% of the shape — dispatch_state,
-- assignment_id, a workspace-consistency trigger set, a unique index — the
-- widen is the same move B1 made for mission_activity → the event log.
--
-- ── What changes ────────────────────────────────────────────────────────
--
-- comment_id becomes NULLABLE. A delivery is now raised against an EVENT
-- (mission_activity row), not only a comment — §10.5's input-routing table
-- names several inputs (agent hand-off, human approval, STOP) that are not
-- comments at all, and a delivery table that can only reference a comment
-- cannot carry them once those producers exist. B2 itself only ever writes
-- comment_id (the mention path is still the only producer), but the column
-- has to admit NULL now or the day a second producer arrives is a second
-- rebuild.
--
-- event_id (new) references the mission_activity row that raised this
-- delivery — mentionRecorder.record calls issueEvents.logEvent (not log) so
-- it has this id in hand before it writes here. Nullable for the exact
-- reason comment_id now is: a future producer might not always have
-- allocated one yet at write time (unlikely, but the freedom this migration
-- exists to buy is "the schema does not force it").
--
-- state (new) answers a DIFFERENT question than dispatch_state:
-- dispatch_state says "did the dispatcher create an assignment"
-- (dispatched|refused|skipped|failed); state says "did a run consume this"
-- (pending|claimed|consumed|failed|superseded) — §10.2's delivery state
-- machine. Conflating them would lose the distinction between "never
-- dispatched" and "dispatched but never consumed", which is exactly the F4
-- blind spot §9.3 names. Both columns are kept.
--
-- claimed_by_run_id (new) is the assignment that won the claim CAS on this
-- delivery. Set by a plain UPDATE once insertCappedAssignment hands back an
-- id (issue_deliveries.go's attachDeliveryRun) — the claim CAS itself
-- (issue_deliveries.go's claimDelivery) cannot know the run id yet, because
-- the id does not exist until AFTER the claim is won and dispatch is
-- attempted. ON DELETE SET NULL, same reasoning as assignment_id below: the
-- delivery outlives the run that (attempted to) consume it.
--
-- priority (new) is §11.5's `stop > correction > normal` — modelled as a
-- column now so a future producer (interrupt-as-event) does not need a
-- fourth migration to add it. B2's only producer (the mention path) always
-- writes 'normal'; nothing in this PR reads the column for ordering yet.
--
-- UNIQUE(event_id, agent_id) replaces UNIQUE(comment_id, agent_id) — this
-- is invariant I1 in one line, and it is the reason "ten concurrent
-- identical deliveries of the same event produce one row" holds at the
-- schema level rather than only in application code (issue_deliveries_test.go
-- proves it under real goroutines and -race, mirroring
-- internal/missionactivity's TestEmit_SeqIsMonotonicUnderConcurrentWriters).
-- SQLite treats every NULL as distinct under a UNIQUE index, so this does
-- NOT dedupe rows whose event_id is NULL — which is fine: B2's only
-- producer always sets event_id, and the migrated legacy rows below (which
-- predate event_id existing) are the only NULL-event_id rows this database
-- will ever hold.
--
-- ── Migrated data ───────────────────────────────────────────────────────
--
-- event_id: NULL for every existing row. There is no event to backfill it
-- from — event_id did not exist before B1 (#2332) added seq/payload_json to
-- mission_activity, and re-deriving one after the fact by matching
-- mission_id+agent_id+action='mentioned' against created_at is a guess, not
-- a fact, for the same reason §9.9 refuses to re-parse comments for
-- mentions on restore. Zero rows are expected in practice (this table is
-- days old), and the non-zero backfill this would otherwise need is
-- deliberately not written for that reason — there is nothing correct to
-- backfill it WITH.
--
-- state: derived from the existing dispatch_state, because that is the only
-- history available. 'dispatched' rows already ran to completion under the
-- old synchronous flow (dispatchOne called DispatchMention directly, which
-- started a run before this migration's claim/consume path existed), so
-- they are marked 'consumed' — the row's lifecycle is over, nothing will
-- ever claim it. 'refused'/'skipped'/'failed' rows never produced a run and
-- never will (B2 has no retry loop — that is B4's lease sweep), so they are
-- marked 'failed' — a delivery whose only attempt is already spent, not
-- "awaiting a claim that will never come" ('pending' would be a lie: a
-- reader of that state, once B4's sweeper exists, would try to claim a row
-- from before the sweeper existed).
--
-- claimed_by_run_id, priority: NULL / 'normal' for every migrated row —
-- neither concept existed when these rows were written.
--
-- ── Triggers ────────────────────────────────────────────────────────────
--
-- DROP TABLE (below) drops trg_mission_comment_mentions_consistency_ins/upd
-- along with it — SQLite has no ALTER TRIGGER, and a trigger cannot outlive
-- the table it is attached to. Both are recreated here, in this same file,
-- with a `NEW.comment_id IS NOT NULL AND ...` guard: with comment_id now
-- nullable, the old unguarded form's subquery
-- `SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id`
-- yields NULL for a NULL comment_id, `NULL IS NOT NEW.mission_id` is TRUE,
-- and every non-comment delivery would abort on insert. Verified against
-- the live trigger body, not discovered in review (§9.3's own warning).
--
-- Column order matches the original table with the new columns appended,
-- matching mission_activity's widen migration for the same reason:
-- BackupTableIntent's positional expectations for any dump/restore code
-- that names columns explicitly stay undisturbed.

PRAGMA foreign_keys = OFF;

CREATE TABLE mission_comment_mentions_new (
    id           TEXT NOT NULL PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id)       ON DELETE CASCADE,
    mission_id   TEXT NOT NULL REFERENCES missions(id)         ON DELETE CASCADE,
    comment_id   TEXT REFERENCES mission_comments(id)          ON DELETE CASCADE,
    agent_id     TEXT NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,

    position INTEGER NOT NULL DEFAULT 0,

    dispatch_state  TEXT NOT NULL DEFAULT 'skipped',
    assignment_id   TEXT REFERENCES assignments(id) ON DELETE SET NULL,
    dispatch_detail TEXT,

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    event_id          TEXT REFERENCES mission_activity(id) ON DELETE SET NULL,
    state             TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','claimed','consumed','failed','superseded')),
    claimed_by_run_id TEXT REFERENCES assignments(id) ON DELETE SET NULL,
    priority          TEXT NOT NULL DEFAULT 'normal'
        CHECK (priority IN ('stop','correction','normal')),

    UNIQUE (event_id, agent_id)
);

INSERT INTO mission_comment_mentions_new
    (id, workspace_id, mission_id, comment_id, agent_id, position,
     dispatch_state, assignment_id, dispatch_detail, created_at,
     event_id, state, claimed_by_run_id, priority)
SELECT
    id, workspace_id, mission_id, comment_id, agent_id, position,
    dispatch_state, assignment_id, dispatch_detail, created_at,
    NULL,
    CASE WHEN dispatch_state = 'dispatched' THEN 'consumed' ELSE 'failed' END,
    NULL,
    'normal'
FROM mission_comment_mentions;

DROP TABLE mission_comment_mentions;
ALTER TABLE mission_comment_mentions_new RENAME TO mission_comment_mentions;

-- "who was mentioned on this issue" — the issue card's read path.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_mission
    ON mission_comment_mentions(mission_id);

-- "what was this agent mentioned on" — the agent-side read, and the one an
-- operator uses when an agent claims it was never asked.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_agent
    ON mission_comment_mentions(agent_id);

-- Workspace scoping for the backup dump's generic filter.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_workspace
    ON mission_comment_mentions(workspace_id);

-- "which deliveries did this run claim" — finishAssignment's consume-on-
-- completion path (issue_deliveries.go's consumeDeliveriesForRun) scans by
-- this column. Not a prefix of UNIQUE(event_id, agent_id) — claimed_by_run_id
-- is neither leading column of that index — so this is not redundant with it
-- (TestRedundantIndexPolicy).
-- The rebuild must not lose the two foreign keys the old table had indexed:
-- assignment_id led idx_mission_comment_mentions_assignment (a run's
-- deletion enforces ON DELETE SET NULL by scanning this table otherwise),
-- and comment_id was the leading column of the old UNIQUE(comment_id,
-- agent_id) — a comment's cascade needs it just as much now that the unique
-- key leads with event_id. TestForeignKeyIndexPolicy ratchets both.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_assignment
    ON mission_comment_mentions(assignment_id);
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_comment
    ON mission_comment_mentions(comment_id);

CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_claimed_by_run
    ON mission_comment_mentions(claimed_by_run_id);

-- Consistency triggers, recreated with the NEW.comment_id IS NOT NULL guard
-- this migration's header explains. Otherwise identical to
-- 20260807094500_mention_workspace_consistency.sql's trg_*_ins/upd.
CREATE TRIGGER IF NOT EXISTS trg_mission_comment_mentions_consistency_ins
BEFORE INSERT ON mission_comment_mentions
BEGIN
    SELECT RAISE(ABORT, 'mention comment must belong to the mention mission')
    WHERE NEW.comment_id IS NOT NULL
      AND (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id) IS NOT NEW.mission_id;

    SELECT RAISE(ABORT, 'mention mission must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'mentioned agent must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;

CREATE TRIGGER IF NOT EXISTS trg_mission_comment_mentions_consistency_upd
BEFORE UPDATE ON mission_comment_mentions
BEGIN
    SELECT RAISE(ABORT, 'mention comment must belong to the mention mission')
    WHERE NEW.comment_id IS NOT NULL
      AND (SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id) IS NOT NEW.mission_id;

    SELECT RAISE(ABORT, 'mention mission must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM missions WHERE id = NEW.mission_id) IS NOT NEW.workspace_id;

    SELECT RAISE(ABORT, 'mentioned agent must belong to the mention workspace')
    WHERE (SELECT workspace_id FROM agents WHERE id = NEW.agent_id) IS NOT NEW.workspace_id;
END;

PRAGMA foreign_keys = ON;
