-- Two clocks, because there were always two questions.
--
-- ── What 20260813212851 got wrong ──────────────────────────────────────────
--
-- That migration added ONE deadline and the handler wrote it as
-- created_at + 300 s for every escalation. 300 s is the right bound for the
-- AGENT's wait: internal/sidecar/query.go polls that long and then proceeds
-- without an answer, and the whole point of the column was to make the row's
-- state and the agent's belief the same event instead of two events that
-- happened to agree.
--
-- It is the wrong bound for the HUMAN's. An escalation is a question aimed at a
-- person, and a CREDENTIAL escalation is a request for a secret that person has
-- to go and fetch. The queue expired five minutes after it was raised: the
-- operator saw the inbox item, walked to the password manager, came back seven
-- minutes later, clicked Approve — and got 409 for a question NOBODY HAD EVER
-- ANSWERED. Before that branch a PENDING escalation stayed answerable
-- indefinitely, so this was a regression that made the human approval queue
-- unusable for anything you cannot answer from your chair.
--
-- Worse for CREDENTIAL rows: only the resolve path disposes of the staged
-- PENDING_APPROVAL credential (approve → ACTIVE, reject → REJECTED +
-- deleted_at). Expiry and cancel never touched credential_id, so the encrypted
-- secret sat in the vault with the only route that could activate or reject it
-- answering 409 — and the live-name probe in escalation_credential.go counted
-- it, so every LATER proposal of that name came back as a conflict. One
-- unanswered question jammed auto-staging for that name forever.
--
-- ── The two clocks ─────────────────────────────────────────────────────────
--
--   deadline_at         the AGENT's wait window (unchanged: 300 s). Bounds the
--                       long poll and NOTHING ELSE. When it passes the wait
--                       ends, agent_gave_up_at is stamped, and the agent is
--                       told to continue with an explicit warning. The row
--                       stays PENDING: the agent giving up is not a decision.
--
--   answer_deadline_at  the HUMAN's answerability window (7 days). When THIS
--                       passes with no decision the row goes EXPIRED, and a
--                       staged credential is disposed of exactly the way a
--                       rejection disposes of it.
--
-- agent_gave_up_at is what keeps the half of the old design that was right: the
-- database still records that the agent stopped waiting, so an operator
-- answering late is told the asking run will not hear them. It is a timestamp
-- and not a status because it is not a transition — the question is still open.
--
-- ── Why one human window and not one per type ──────────────────────────────
--
-- A per-type deadline was considered. The obvious guess (give CREDENTIAL
-- longer, it's the one a human has to walk for) is backwards: a staged secret
-- sitting encrypted in the vault is a liability, so if anything CREDENTIAL
-- wants the SHORTER window. With disposal now correct on every terminal path
-- the liability is bounded either way, and a second number would be a second
-- thing to explain and tune for a difference nobody can state. One window.
--
-- 7 days is chosen against the human, not the machine: a weekend plus a day
-- either side. Past that an unanswered question is a stale record rather than
-- an open one, and expiring it is more honest than a queue that only grows.
--
-- ── NULL, and what is not backfilled ───────────────────────────────────────
--
-- NULL answer_deadline_at means no human deadline — answerable until somebody
-- resolves or cancels it, which is what escalations did before deadlines
-- existed. Every pre-existing row keeps NULL for the same reason 20260813212851
-- refused to backfill deadline_at: retro-expiring questions raised before the
-- concept existed would close rows a human may still intend to answer.
--
-- Rows the buggy window already flipped to EXPIRED are NOT resurrected. Their
-- agents continued minutes after being raised and are long gone, so un-expiring
-- them would reopen questions whose runs no longer exist. The half that IS
-- repaired below is the half that leaves damage behind: their stranded
-- credentials.

ALTER TABLE escalations ADD COLUMN answer_deadline_at TEXT;
ALTER TABLE escalations ADD COLUMN agent_gave_up_at TEXT;

-- The sweeper's predicate moved from deadline_at to answer_deadline_at, so the
-- index moves with it: equality column first, then the range, over a table
-- where all but a handful of rows are terminal.
CREATE INDEX IF NOT EXISTS idx_escalation_answer_deadline
    ON escalations(status, answer_deadline_at);

-- …and the old one is now dead weight. Nothing ranges on deadline_at any more:
-- the waiter reads it by primary key for one row it already has. Every insert
-- and every status transition would still pay to maintain it.
DROP INDEX IF EXISTS idx_escalation_deadline;

-- Retire the proposals the regression stranded. A PENDING_APPROVAL credential
-- is only reachable through its escalation's resolve path, so one whose
-- escalation is no longer PENDING can never be activated and never rejected —
-- it is an encrypted secret nobody can act on, holding its name hostage.
--
-- Scoped by NOT EXISTS over PENDING escalations rather than by a status list,
-- because the property that matters is reachability, not which terminal state
-- it reached. createPendingCredential is the only producer of PENDING_APPROVAL
-- rows (v119), so a row with no PENDING escalation behind it is unreachable by
-- construction — there is no other door.
--
-- REJECTED + deleted_at is precisely what ResolveEscalation's reject arm does.
-- The disposal is the same disposal; only the reason differs.
UPDATE credentials
   SET status     = 'REJECTED',
       deleted_at = COALESCE(deleted_at, datetime('now')),
       updated_at = datetime('now')
 WHERE status = 'PENDING_APPROVAL'
   AND deleted_at IS NULL
   AND NOT EXISTS (
       SELECT 1 FROM escalations e
        WHERE e.credential_id = credentials.id
          AND e.status = 'PENDING'
   );
