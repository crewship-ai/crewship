-- issue_counters: key the counter on the namespace it feeds (#1797).
--
-- The table was
--
--     CREATE TABLE issue_counters (
--         crew_id TEXT PRIMARY KEY NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
--         next_number INTEGER NOT NULL DEFAULT 1
--     )
--
-- so every crew counted privately from 1. But the namespace those numbers feed
-- is per WORKSPACE: an identifier is `<prefix>-<n>` and missions carry
-- UNIQUE(workspace_id, identifier) (idx_mission_workspace_identifier, #1733).
-- The prefix is crews.issue_prefix, or the first three letters of the crew slug
-- upper-cased when that is empty — which is how the collision arrives without
-- anyone typing a prefix at all: `engineering` and `engine` both derive ENG.
-- Two such crews in one workspace each mint ENG-1, and the loser's insert is
-- rejected by that unique index.
--
-- It is not a transient 500. The counter upsert and the mission INSERT share one
-- transaction, and the handler returns on the rejected insert without
-- committing, so the counter increment rolls back with it: the losing crew's
-- next_number never advances, the next create retries the identical identifier,
-- and the crew can never create an issue again. Rekeying is what makes the
-- collision impossible rather than merely rarer — two crews sharing a prefix now
-- share one sequence and simply interleave.
--
-- What the counter costs when it is lost is worth restating here, because the
-- rebuild below moves every row (20260820074400_issue_counters_crew_not_null):
-- next_number is what stops a crew from re-issuing identifiers it already used.
-- Come back with the row missing and the crew starts again at ENG-1, on top of
-- the ENG-1 that is already there. That is why the collapse below takes MAX,
-- never SUM and never first-wins, and why it also folds in the high-water mark
-- of identifiers already minted.
--
-- Existing data cannot block this migration. Duplicate identifiers ALREADY
-- cannot exist — idx_mission_workspace_identifier is what has been rejecting
-- them, and that rejection IS the bug — so missions are clean by construction.
-- What CAN exist is two crews in one workspace with a colliding effective
-- prefix, one of them wedged at next_number = 1; the GROUP BY merges those two
-- rows into one and, by taking the max, unwedges the losing crew.
--
-- SQLite cannot re-key a table in place, so this is the standard rebuild. It is
-- safe inside the migration runner's wrapper transaction — the recipe that is
-- NOT (see migrate.go's fnNoTx contract) fails because DROP TABLE fires the
-- DEPENDENTS' foreign keys, and issue_counters has no dependents: nothing in the
-- schema references it.
--
-- Three consequences of the new key, all deliberate:
--
--   * crew_id is gone. It named one crew, and a collapsed row belongs to two.
--     The table now carries workspace_id as a real, NOT NULL column, which also
--     retires #1973's hazard for it outright: the backup scoper no longer has to
--     reach this table's workspace through a foreign key at all.
--   * The ON DELETE CASCADE from crews is gone with it, so deleting a crew no
--     longer deletes the counter. That is the safer direction: a replacement
--     crew with the same prefix continues the sequence instead of restarting it
--     on top of identifiers that outlived their crew.
--   * A crew that CHANGES its issue_prefix strands its old row and starts a new
--     one. Seeding that new row from 1 would walk straight back into the
--     collision, so both the backfill here and the runtime allocator seed from
--     the highest number already minted under that prefix.
--
-- # Rows that do not make the trip
--
-- 20260820074400_issue_counters_crew_not_null lists two unreachable classes it
-- drops; the JOIN to crews below drops the same two (a NULL crew_id and a
-- crew_id naming a crew that is gone). There is a THIRD here, because the new
-- table's workspace_id is NOT NULL and carries its own FK: a crew or a mission
-- whose WORKSPACE no longer exists. Like the others it survives only where the
-- row outlived its parent under `PRAGMA foreign_keys=OFF`, and like the others
-- it must be dropped rather than handed to the FK checker — the copy runs with
-- foreign_keys ON, so it would fail with SQLITE_CONSTRAINT_FOREIGNKEY (787) and
-- take the whole boot down, naming neither the table nor the row. That is the
-- explicit EXISTS in the outer WHERE, which is the guard the precedent uses and
-- the reason this migration can claim to follow it.
--
-- # Two derivations of the high-water mark, and why they agree
--
-- This backfill reads the high-water mark out of missions.number; the runtime
-- allocator (nextIssueIdentifierTx, internal/api/issue_create_core.go) reads it
-- out of the identifier TEXT — MAX(CAST(SUBSTR(identifier, LENGTH(prefix)+2) AS
-- INTEGER)) — because at runtime the question is literally "does <prefix>-<n>
-- already exist", which is what idx_mission_workspace_identifier checks. The
-- two can only disagree for a row whose identifier tail is not its number.
--
-- No such row is produced. missions.identifier has exactly two writers —
-- issue_create_core.go's insertIssueTx and issue_handler_create.go's REST
-- create — and both take `identifier, number` from one nextIssueIdentifierTx
-- call in the same INSERT, where the identifier IS prefix + "-" + number.
-- Nothing else writes the column, and no path updates it afterwards. The
-- reconstruction test in the WHERE clause below is what makes that assumption
-- checkable rather than assumed: an identifier that does not rebuild from its
-- own number is discarded here instead of contributing a wrong prefix.
--
-- The backfill uses number rather than the text because it is a one-shot scan
-- over every identified mission in the database, and comparing an indexed
-- integer beats parsing a substring per row. The allocator cannot make that
-- trade: it is asking about ONE prefix, and a crew that changed prefix has
-- identifiers under a prefix its number sequence says nothing about.

CREATE TABLE issue_counters_v3 (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    prefix TEXT NOT NULL,
    next_number INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (workspace_id, prefix)
);

-- next_number holds the LAST number handed out (the allocator increments, then
-- returns), and missions.number holds one such value, so the two sides of the
-- UNION ALL are on the same scale and MAX over them is the high-water mark.
INSERT INTO issue_counters_v3 (workspace_id, prefix, next_number)
SELECT ws, prefix, MAX(n)
FROM (
    -- Carry the per-crew counters over under each crew's effective prefix,
    -- derived exactly as the allocator derives it: issue_prefix, else the first
    -- three characters of the slug upper-cased. Counters whose crew is gone are
    -- dropped by the JOIN — the same two unreachable classes the previous
    -- rebuild dropped. The third class, a row that reaches a workspace which is
    -- itself gone, is dropped by the EXISTS below.
    SELECT c.workspace_id AS ws,
           COALESCE(NULLIF(c.issue_prefix, ''), UPPER(SUBSTR(c.slug, 1, 3))) AS prefix,
           ic.next_number AS n
    FROM issue_counters ic
    JOIN crews c ON c.id = ic.crew_id

    UNION ALL

    -- The identifiers already in the ground. This is what protects a crew that
    -- changed its prefix at some point in the past: its old identifiers live
    -- under a prefix no counter row names any more, and without this the new
    -- key would start at 1 and collide with them on the very first create.
    -- The prefix is recovered by removing the "-<number>" suffix, and the
    -- reconstruction test in the WHERE clause discards any identifier that is
    -- not actually shaped that way.
    SELECT m.workspace_id,
           SUBSTR(m.identifier, 1, LENGTH(m.identifier) - LENGTH('-' || m.number)),
           m.number
    FROM missions m
    WHERE m.identifier IS NOT NULL
      AND m.number IS NOT NULL
      AND LENGTH(m.identifier) > LENGTH('-' || m.number)
      AND m.identifier = SUBSTR(m.identifier, 1, LENGTH(m.identifier) - LENGTH('-' || m.number))
                         || '-' || m.number
)
-- workspace_id is NOT NULL and carries a real FK on the target, and this runs
-- on a connection with foreign_keys ON. A crew or a mission whose workspace is
-- already gone would therefore fail the copy with SQLITE_CONSTRAINT_FOREIGNKEY
-- (787) and abort the boot, naming neither the table nor the row. One EXISTS
-- covers both arms of the UNION ALL — see the note above the CREATE.
WHERE EXISTS (SELECT 1 FROM workspaces w WHERE w.id = ws)
GROUP BY ws, prefix;

DROP TABLE issue_counters;

ALTER TABLE issue_counters_v3 RENAME TO issue_counters;
