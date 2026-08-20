-- issue_counters.crew_id: say NOT NULL, because PRIMARY KEY does not (#1973).
--
-- The table was declared
--
--     CREATE TABLE issue_counters (
--         crew_id TEXT PRIMARY KEY REFERENCES crews(id) ON DELETE CASCADE,
--         next_number INTEGER NOT NULL DEFAULT 1
--     )
--
-- and everybody read that first line as "crew_id is required". SQLite does not.
-- A rowid table's PRIMARY KEY column accepts NULL unless it is INTEGER PRIMARY
-- KEY — a documented, deliberately-kept compatibility quirk. So the schema
-- permits rows this feature has no meaning for, and `PRAGMA table_info` reports
-- notnull=0, which is the answer any tool that asks gets.
--
-- One such tool is the backup scoper. issue_counters has no workspace_id: it
-- reaches its workspace only through crew_id, and a backup filter on a nullable
-- column omits every row where that column is NULL — silently. The bundle
-- succeeds, verifies, and comes back short. That is what #1973 is about, and
-- for this table the fix is not a different path (there is no other FK to take)
-- but making the one path total.
--
-- What the counter costs when it is lost is worth stating plainly: next_number
-- is what stops a restored crew from re-issuing identifiers it already used.
-- Come back with the row missing and the crew starts again at ENG-1, on top of
-- the ENG-1 that restored alongside it.
--
-- SQLite cannot add a column constraint in place, so this is the standard
-- rebuild. It is safe inside the migration runner's wrapper transaction —
-- the recipe that is NOT (see migrate.go's fnNoTx contract) fails because
-- DROP TABLE fires the DEPENDENTS' foreign keys, and issue_counters has no
-- dependents: nothing in the schema references it.
--
-- Two classes of row do not make the trip, and neither is reachable data:
--
--   * crew_id IS NULL — no crew, therefore no workspace, no issue prefix and
--     no code path that reads it. The INSERT ... SELECT below cannot carry it
--     into a NOT NULL column anyway.
--   * crew_id naming a crew that no longer exists — an orphan the declared
--     ON DELETE CASCADE says should already be gone. It survives only where
--     the row outlived its crew under `PRAGMA foreign_keys=OFF`. Copying it
--     would fail the FK check on a target that has foreign_keys ON and take
--     the whole boot down with it.

CREATE TABLE issue_counters_v2 (
    crew_id TEXT PRIMARY KEY NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
    next_number INTEGER NOT NULL DEFAULT 1
);

INSERT INTO issue_counters_v2 (crew_id, next_number)
SELECT ic.crew_id, ic.next_number
FROM issue_counters ic
WHERE ic.crew_id IS NOT NULL
  AND EXISTS (SELECT 1 FROM crews c WHERE c.id = ic.crew_id);

DROP TABLE issue_counters;

ALTER TABLE issue_counters_v2 RENAME TO issue_counters;
